package controller

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
	"github.com/marcelheidebrecht/cell-platform/internal/cell"
)

// AgentIdentity sets up the ops agent's account; implemented by
// *authentik.Client.
type AgentIdentity interface {
	EnsureServiceAccount(ctx context.Context, username string, groups ...string) error
	CreateAppPassword(ctx context.Context, username string) (string, error)
}

// EnsureAgentAccount gives the ops agent its account in the identity (a
// service account in the agents' group, so the API gives it the agent
// role) and its credentials as Secret cell-system/cell-agent, once. The
// agent has no rights in the cluster: it works through the API alone.
func EnsureAgentAccount(ctx context.Context, c client.Client, id AgentIdentity) error {
	if err := id.EnsureServiceAccount(ctx, cell.AgentAccount, cell.AgentGroup); err != nil {
		return err
	}
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: cell.SystemNamespace, Name: cell.AgentAccount}}
	err := c.Get(ctx, client.ObjectKeyFromObject(s), s)
	switch {
	case err == nil && len(s.Data["password"]) > 0:
		return nil
	case err != nil && !apierrors.IsNotFound(err):
		return err
	}
	password, perr := id.CreateAppPassword(ctx, cell.AgentAccount)
	if perr != nil {
		return perr
	}
	s.Type = corev1.SecretTypeOpaque
	s.Data = map[string][]byte{"username": []byte(cell.AgentAccount), "password": []byte(password)}
	if apierrors.IsNotFound(err) {
		return c.Create(ctx, s)
	}
	return c.Update(ctx, s)
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
