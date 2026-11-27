package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// AgentIdentity sets up an agent's account; implemented by
// *authentik.Client.
type AgentIdentity interface {
	EnsureServiceAccount(ctx context.Context, username string, groups ...string) error
	CreateAppPassword(ctx context.Context, username string) (string, error)
	RevokeAppPasswords(ctx context.Context, username string) error
	RemoveServiceAccount(ctx context.Context, username string) error
}

// AgentReconciler runs each Agent (E-97): its account in the identity
// (cell-agent-<name>.<uid>, a service account in the agents' group, so the
// API gives it the agent role), its credentials as Secret cell-agent-<name> (its password and its
// key to the model gateway, made once) and Deployment cell-agent-<name>.
// Both belong to the Agent and go with it. The agent has no rights in the
// cluster: it works through the API alone, which checks each of its calls
// against the Agent.
type AgentReconciler struct {
	client.Client
	Identity AgentIdentity
	// Image is the platform image, which holds the agent.
	Image string
}

func (r *AgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cellv1.Agent{}).
		Owns(&corev1.Secret{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

// agentFinalizer deletes a deleted Agent's account, and with it every
// password and token it holds, before the Agent is gone.
var agentFinalizer = cellv1.GroupVersion.Group + "/agent"

func (r *AgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	a := &cellv1.Agent{}
	if err := r.Get(ctx, req.NamespacedName, a); err != nil {
		if apierrors.IsNotFound(err) {
			// Gone: what its agent was working on fails. An error comes
			// back through the queue.
			return ctrl.Result{}, failTasks(ctx, r.Client, req.Name)
		}
		return ctrl.Result{}, err
	}
	name := cell.AgentPrefix + a.Name
	account := cell.AgentAccount(a.Name, string(a.UID))
	orig := a.DeepCopy()
	if !a.DeletionTimestamp.IsZero() {
		if !controllerutil.RemoveFinalizer(a, agentFinalizer) {
			return ctrl.Result{}, nil
		}
		if err := failTasks(ctx, r.Client, a.Name); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Identity.RemoveServiceAccount(ctx, account); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, a, client.MergeFrom(orig)))
	}
	if controllerutil.AddFinalizer(a, agentFinalizer) {
		// A new Agent: what an earlier one of the name, lost without its
		// finalizer, was working on fails.
		if err := failTasks(ctx, r.Client, a.Name); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Patch(ctx, a, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.Identity.EnsureServiceAccount(ctx, account, cell.AgentGroup); err != nil {
		return ctrl.Result{}, err
	}
	owner := *metav1.NewControllerRef(a, cellv1.GroupVersion.WithKind("Agent"))
	s := &corev1.Secret{}
	err := r.Get(ctx, system(name), s)
	switch {
	case apierrors.IsNotFound(err):
		// Whoever held the account's last credentials (a removed Secret)
		// loses them.
		if err := r.Identity.RevokeAppPasswords(ctx, account); err != nil {
			return ctrl.Result{}, err
		}
		password, err := r.Identity.CreateAppPassword(ctx, account)
		if err != nil {
			return ctrl.Result{}, err
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return ctrl.Result{}, err
		}
		s = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: cell.SystemNamespace, Name: name, OwnerReferences: []metav1.OwnerReference{owner}},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"username": []byte(account), "password": []byte(password), cell.AgentModelKey: []byte(hex.EncodeToString(key))},
		}
		if err := r.Create(ctx, s); err != nil {
			return ctrl.Result{}, err
		}
	case err != nil:
		return ctrl.Result{}, err
	case !metav1.IsControlledBy(s, a):
		// Left by an Agent of the same name deleted a moment ago (the
		// garbage collector removes it), or no agent's at all.
		return ctrl.Result{}, fmt.Errorf("secret %s/%s is not agent %s's", cell.SystemNamespace, name, a.Name)
	case string(s.Data["username"]) != account:
		// Credentials of another account, which only a hand edit makes: no
		// release made agent accounts without the UID. The API refuses them;
		// the operator may not delete Secrets here, so it says what to do.
		return ctrl.Result{}, fmt.Errorf("secret %s/%s holds the credentials of %q, not of %s: delete it for new ones", cell.SystemNamespace, name, s.Data["username"], account)
	}
	_, err = serverApply(ctx, r.Client, agentDeployment(name, r.Image, a.Spec.Paused, owner, string(s.UID)))
	return ctrl.Result{}, err
}

// held tells whether task is an agent's open work: running, waiting for a
// person's reply, or pending with a reply for that agent (E-99).
func held(t *cellv1.Task) bool {
	_, _, ok := cell.AgentOf(t.Status.Assignee)
	switch t.Status.Phase {
	case cellv1.RunRunning, cellv1.RunPending, cellv1.RunWaitingForInput:
		return ok
	}
	return false
}

// AgentTaskReconciler fails each task that becomes the open work of an
// account whose Agent is gone, being deleted or made again, as a claim or
// a reply that raced the deletion; AgentReconciler fails those already
// open.
type AgentTaskReconciler struct {
	client.Client
}

func (r *AgentTaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	agents := func(o client.Object) bool { return held(o.(*cellv1.Task)) }
	claimed := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return agents(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			o, n := e.ObjectOld.(*cellv1.Task), e.ObjectNew.(*cellv1.Task)
			return agents(n) && (o.Status.Phase != n.Status.Phase || o.Status.Assignee != n.Status.Assignee)
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	return ctrl.NewControllerManagedBy(mgr).Named("agent-tasks").
		For(&cellv1.Task{}, builder.WithPredicates(claimed)).Complete(r)
}

func (r *AgentTaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, failTask(ctx, r.Client, req.NamespacedName)
}

// failTasks fails the open tasks of Agent name's accounts that failTask
// fails.
func failTasks(ctx context.Context, c client.Client, name string) error {
	var tasks cellv1.TaskList
	if err := c.List(ctx, &tasks); err != nil {
		return err
	}
	for _, t := range tasks.Items {
		if n, _, ok := cell.AgentOf(t.Status.Assignee); ok && n == name && held(&t) {
			if err := failTask(ctx, c, client.ObjectKeyFromObject(&t)); err != nil {
				return err
			}
		}
	}
	return nil
}

// failTask fails an open task whose agent can never finish it: the Agent
// of its account is gone, being deleted or made again (another UID), so the
// API refuses the account. The admin sees why and can ask again.
func failTask(ctx context.Context, c client.Client, key client.ObjectKey) error {
	// Update checks the task's version: one its agent finished meanwhile
	// is read again and stays as it is.
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var t cellv1.Task
		if err := c.Get(ctx, key, &t); err != nil {
			return client.IgnoreNotFound(err)
		}
		name, uid, _ := cell.AgentOf(t.Status.Assignee)
		if !held(&t) {
			return nil
		}
		// Cached: the Agent whose credentials the claim used was in the
		// cache before its Secret was made.
		var a cellv1.Agent
		err := c.Get(ctx, client.ObjectKey{Name: name}, &a)
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		if err == nil && string(a.UID) == uid && a.DeletionTimestamp.IsZero() {
			return nil
		}
		now := metav1.Now()
		t.Status.Phase, t.Status.FinishedAt = cellv1.RunFailed, &now
		t.Status.Result = fmt.Sprintf("interrupted: agent %s was deleted or made again while it worked on this; ask again", name)
		t.Status.Answer = nil
		return c.Status().Update(ctx, &t)
	})
}

// agentDeployment runs an agent with its credentials and nothing else: no
// service account token, no writable file system. New credentials (another
// Secret) restart it: the old ones no longer log in.
func agentDeployment(name, image string, paused bool, owner metav1.OwnerReference, credentials string) *appsv1.Deployment {
	replicas := int32(1)
	if paused {
		replicas = 0
	}
	labels := map[string]string{"app.kubernetes.io/name": name}
	secret := func(env, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: env, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key}}}
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: cell.SystemNamespace, Name: name, Labels: labels, OwnerReferences: []metav1.OwnerReference{owner}},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			// One agent works on a task at a time: never two of them.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{cellv1.GroupVersion.Group + "/credentials": credentials}},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr(false),
					// The platform image is built on the first machine, for its architecture.
					NodeSelector: map[string]string{"node-role.kubernetes.io/control-plane": "true"},
					Containers: []corev1.Container{{
						Name: "agent", Image: image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/agent"},
						// The key to the model gateway is a Bearer token, as
						// every app sends its key (E-81); the provider's key
						// stays in the gateway.
						Env: []corev1.EnvVar{secret("CELL_AGENT_USERNAME", "username"), secret("CELL_AGENT_PASSWORD", "password"), secret("CELL_MODELS_KEY", cell.AgentModelKey)},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("32Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr(false), ReadOnlyRootFilesystem: ptr(true),
							Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
				},
			},
		},
	}
}

// askAgent hands an app that needs attention to the ops agent: one
// diagnose task per cause, each with its own identity. Causes are a
// failed restore that holds the app, a failed change, and failed health
// checks for longer than the manifest allows.
func (r *AppReconciler) askAgent(ctx context.Context, app *cellv1.App, act activity) error {
	if app.Status.Phase != cellv1.PhaseNeedsAttention {
		return nil
	}
	var causes []string
	if act.reason != "" {
		causes = append(causes, act.reason) // the app is stopped: nothing else to ask about
	} else {
		if act.failedChange != "" {
			causes = append(causes, act.failedChange)
		}
		if since, long := r.unhealthyTooLong(app); long {
			causes = append(causes, "health checks fail since "+since.UTC().Format(time.RFC3339))
		}
	}
	var seen []string
	for _, c := range app.Status.Conditions {
		if c.Status == metav1.ConditionFalse {
			seen = append(seen, fmt.Sprintf("%s=False (%s): %s", c.Type, c.Reason, c.Message))
		}
	}
	for _, cause := range causes {
		id := fnv.New32a()
		_, _ = id.Write([]byte(cause))
		task := &cellv1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-diagnose-%08x", app.Name, id.Sum32())},
			Spec: cellv1.TaskSpec{Kind: "diagnose", App: app.Name, Input: "Cause: " + cause + "\n\n" + strings.Join(seen, "\n"),
				Requester: cellv1.Requester{Kind: "Operator", Name: "cell-operator"}},
		}
		if err := r.Get(ctx, client.ObjectKeyFromObject(task), &cellv1.Task{}); !apierrors.IsNotFound(err) {
			if err != nil {
				return err
			}
			continue
		}
		if err := r.Create(ctx, task); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	return nil
}
