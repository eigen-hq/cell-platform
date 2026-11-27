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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// AgentIdentity sets up an agent's account; implemented by
// *authentik.Client.
type AgentIdentity interface {
	EnsureServiceAccount(ctx context.Context, username string, groups ...string) error
	CreateAppPassword(ctx context.Context, username string) (string, error)
	RevokeAppPasswords(ctx context.Context, username string) error
}

// AgentReconciler runs each Agent (E-97): its account in the identity (a
// service account in the agents' group, so the API gives it the agent
// role), its credentials as Secret cell-agent-<name> (its password and its
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

// agentFinalizer revokes a deleted Agent's passwords before its name is
// free again: an Agent of the same name starts with none of them.
var agentFinalizer = cellv1.GroupVersion.Group + "/agent"

func (r *AgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	a := &cellv1.Agent{}
	if err := r.Get(ctx, req.NamespacedName, a); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// ponytail: a deleted Agent's account stays in the identity, without
	// passwords and without rights in the API (E-97); delete it here too
	// once leftover accounts get in the way.
	name := cell.AgentPrefix + a.Name
	orig := a.DeepCopy()
	if !a.DeletionTimestamp.IsZero() {
		if !controllerutil.RemoveFinalizer(a, agentFinalizer) {
			return ctrl.Result{}, nil
		}
		if err := r.Identity.RevokeAppPasswords(ctx, name); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, a, client.MergeFrom(orig)))
	}
	if controllerutil.AddFinalizer(a, agentFinalizer) {
		if err := r.Patch(ctx, a, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.Identity.EnsureServiceAccount(ctx, name, cell.AgentGroup); err != nil {
		return ctrl.Result{}, err
	}
	owner := *metav1.NewControllerRef(a, cellv1.GroupVersion.WithKind("Agent"))
	s := &corev1.Secret{}
	err := r.Get(ctx, system(name), s)
	switch {
	case apierrors.IsNotFound(err):
		// Whoever held the last credentials (a deleted Agent of this name,
		// a removed Secret) loses them.
		if err := r.Identity.RevokeAppPasswords(ctx, name); err != nil {
			return ctrl.Result{}, err
		}
		password, err := r.Identity.CreateAppPassword(ctx, name)
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
			Data:       map[string][]byte{"username": []byte(name), "password": []byte(password), cell.AgentModelKey: []byte(hex.EncodeToString(key))},
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
	}
	_, err = serverApply(ctx, r.Client, agentDeployment(name, r.Image, a.Spec.Paused, owner, string(s.UID)))
	return ctrl.Result{}, err
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
