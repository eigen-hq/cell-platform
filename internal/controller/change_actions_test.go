package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
)

// approvedChange creates a change that an admin already approved (in a
// cell, the API records the decision in a call of its own).
func approvedChange(t *testing.T, name string, spec cellv1.ChangeRequestSpec) *cellv1.ChangeRequest {
	t.Helper()
	spec.Requester = cellv1.Requester{Kind: "Human", Name: "alice"}
	spec.Approval = cellv1.Approval{Required: true, Decision: "Approved", By: "bob"}
	cr := &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
	if err := k8s.Create(context.Background(), cr); err != nil {
		t.Fatal(err)
	}
	return cr
}

func plainSpec(subdomain string) cellv1.AppSpec {
	return cellv1.AppSpec{
		Version: "1.0",
		Source:  cellv1.Source{Image: &cellv1.ImageSource{Repository: "example/plain"}},
		Access:  cellv1.Access{Subdomain: subdomain, Port: 80, Auth: cellv1.Auth{Mode: "none"}},
		Health:  cellv1.Health{TimeoutSeconds: 60},
		Backup:  cellv1.BackupPolicy{Schedule: "none"},
	}
}

func rawJSON(t *testing.T, v any) *apiextensionsv1.JSON {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return &apiextensionsv1.JSON{Raw: raw}
}

// M3: installing is a change: it waits for approval, creates the app held
// by the change, and ends with the app's first health check. A second
// install of the same name fails. Removing without deleteData keeps the
// namespace for a reinstall.
func TestInstallAndRemoveChanges(t *testing.T) {
	ctx := context.Background()
	cr := &cellv1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "install-inst"},
		Spec: cellv1.ChangeRequestSpec{
			App: "inst", Action: "install", Reason: "new app",
			Requester: cellv1.Requester{Kind: "Agent", Name: "ops"},
			Patch:     rawJSON(t, plainSpec("inst")),
			Approval:  cellv1.Approval{Required: true},
		},
	}
	if err := k8s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	changePhase(t, "install-inst", cellv1.RunWaitingForApproval)
	if err := k8s.Get(ctx, client.ObjectKey{Name: "inst"}, &cellv1.App{}); !apierrors.IsNotFound(err) {
		t.Fatalf("app installed before approval: %v", err)
	}
	eventually(t, func() error {
		_ = k8s.Get(ctx, client.ObjectKey{Name: "install-inst"}, cr)
		cr.Spec.Approval.Decision, cr.Spec.Approval.By = "Approved", "bob"
		return k8s.Update(ctx, cr)
	})
	var app cellv1.App
	eventually(t, func() error { return k8s.Get(ctx, client.ObjectKey{Name: "inst"}, &app) })
	if app.Annotations[lockKey] != "ChangeRequest/install-inst" {
		t.Errorf("app not held by its install: %v", app.Annotations)
	}

	approvedChange(t, "install-inst-again", cellv1.ChangeRequestSpec{App: "inst", Action: "install", Patch: rawJSON(t, plainSpec("inst"))})
	if again := changePhase(t, "install-inst-again", cellv1.RunFailed); !strings.Contains(again.Status.Message, "already installed") {
		t.Errorf("second install: %s", again.Status.Message)
	}

	kubelet(t, "app-inst", "inst")
	healthy.Store("inst", true)
	issued(t, "inst", "inst.cell.localhost")
	done := changePhase(t, "install-inst", cellv1.RunSucceeded)
	if done.Status.AppUID != string(app.UID) || done.Status.Backup != "none" {
		t.Errorf("install record %+v", done.Status)
	}
	eventually(t, func() error {
		_ = k8s.Get(ctx, client.ObjectKey{Name: "inst"}, &app)
		if h := app.Annotations[lockKey]; h != "" {
			return fmt.Errorf("still held by %s", h)
		}
		return nil
	})

	// An app without data is removed without a backup; its namespace stays.
	approvedChange(t, "remove-inst", cellv1.ChangeRequestSpec{App: "inst", Action: "remove", Reason: "not needed"})
	gone(t, &app)
	removed := changePhase(t, "remove-inst", cellv1.RunSucceeded)
	if !strings.Contains(removed.Status.Message, "data stays") {
		t.Errorf("removal message %q", removed.Status.Message)
	}
	var ns corev1.Namespace
	if err := k8s.Get(ctx, client.ObjectKey{Name: "app-inst"}, &ns); err != nil || !ns.DeletionTimestamp.IsZero() || ns.Labels[cellv1.LabelRetained] != "true" {
		t.Errorf("namespace not retained: %v %v", err, ns.Labels)
	}
}

// M3: removing with deleteData backs the app up, removes it and then
// deletes the namespace with its data.
func TestRemoveChangeDeletesData(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "purge")
	approvedChange(t, "remove-purge", cellv1.ChangeRequestSpec{App: "purge", Action: "remove", DeleteData: true})
	completeBackup(t, "app-purge", "remove-purge-pre", "pre-p", "snap-p")
	gone(t, app)
	var ns corev1.Namespace
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKey{Name: "app-purge"}, &ns); err != nil {
			return err
		}
		if ns.DeletionTimestamp.IsZero() {
			return fmt.Errorf("namespace not being deleted")
		}
		return nil
	})
	changePhase(t, "remove-purge", cellv1.RunRunning)
	// A reinstall waits for the removal instead of adopting the data.
	approvedChange(t, "install-purge", cellv1.ChangeRequestSpec{App: "purge", Action: "install", Patch: rawJSON(t, plainSpec("purge"))})
	waiting := changePhase(t, "install-purge", cellv1.RunPending)
	if !strings.Contains(waiting.Status.Message, "remove-purge") {
		t.Errorf("install waits with %q", waiting.Status.Message)
	}
	if err := k8s.Get(ctx, client.ObjectKey{Name: "purge"}, &cellv1.App{}); !apierrors.IsNotFound(err) {
		t.Fatalf("reinstalled during the removal: %v", err)
	}
	// envtest has no namespace controller: finish the deletion by hand.
	ns.Spec.Finalizers = nil
	if err := k8s.SubResource("finalize").Update(ctx, &ns); err != nil {
		t.Fatal(err)
	}
	done := changePhase(t, "remove-purge", cellv1.RunSucceeded)
	if !strings.Contains(done.Status.Message, "data removed") || done.Status.Backup != "remove-purge-pre" {
		t.Errorf("removal record %q %q", done.Status.Message, done.Status.Backup)
	}
	var again cellv1.App
	eventually(t, func() error { return k8s.Get(ctx, client.ObjectKey{Name: "purge"}, &again) })
	changePhase(t, "install-purge", cellv1.RunRunning)
	removeApp(t, &again)
}

// A running install whose app the cache does not show yet keeps waiting
// for its health check: it neither ends as a removal nor without a check.
func TestInstallWaitsForCache(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	cr := &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "install-late", Finalizers: []string{changeFinalizer}},
		Spec: cellv1.ChangeRequestSpec{App: "late", Action: "install", Patch: rawJSON(t, plainSpec("late"))},
		Status: cellv1.ChangeRequestStatus{Phase: cellv1.RunRunning, Backup: "none", AppUID: "uid-late",
			AppliedGeneration: 1, AppliedAt: ptr(metav1.NewTime(start))}}
	c := fakeClient(k8s.Scheme(), cr)
	now := start
	r := &ChangeReconciler{Client: c, Now: func() time.Time { return now }}
	step := func() *cellv1.ChangeRequest {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)}); err != nil {
			t.Fatal(err)
		}
		var got cellv1.ChangeRequest
		if err := c.Get(ctx, client.ObjectKeyFromObject(cr), &got); err != nil {
			t.Fatal(err)
		}
		return &got
	}
	if got := step(); got.Status.Phase != cellv1.RunRunning {
		t.Fatalf("install before the cache shows its app: %s %q", got.Status.Phase, got.Status.Message)
	}
	now = start.Add(2 * time.Minute)
	if got := step(); got.Status.Phase != cellv1.RunFailed || !strings.Contains(got.Status.Message, "disappeared") {
		t.Fatalf("install whose app stays gone: %s %q", got.Status.Phase, got.Status.Message)
	}
}

// adoptBeforeDelete plays a reinstall that adopts the retained namespace
// between the purge's check and its delete.
type adoptBeforeDelete struct {
	client.Client
	adopt func()
}

func (c adoptBeforeDelete) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.adopt()
	return c.Client.Delete(ctx, obj, opts...)
}

// A purge never deletes a namespace that a reinstall (by any path) adopted
// after the purge checked it.
func TestDeleteDataYieldsToReinstall(t *testing.T) {
	ctx := context.Background()
	app := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "race"}}
	operator := &AppReconciler{Client: k8s}
	if _, err := operator.apply(ctx, namespaceObj(app, true)); err != nil {
		t.Fatal(err)
	}
	r := &ChangeReconciler{Client: adoptBeforeDelete{Client: k8s, adopt: func() {
		if _, err := operator.apply(ctx, namespaceObj(app, false)); err != nil {
			t.Error(err)
		}
	}}}
	cr := &cellv1.ChangeRequest{Spec: cellv1.ChangeRequestSpec{App: "race", Action: "remove", DeleteData: true}}
	set := func(phase cellv1.RunPhase, msg string, _ time.Duration) (ctrl.Result, error) {
		return ctrl.Result{}, nil
	}
	end := func(phase cellv1.RunPhase, msg string) (ctrl.Result, error) {
		t.Errorf("ended %s: %s", phase, msg)
		return ctrl.Result{}, nil
	}
	if _, err := r.deleteData(ctx, cr, set, end); !apierrors.IsConflict(err) {
		t.Fatalf("delete despite adoption: %v", err)
	}
	var ns corev1.Namespace
	if err := k8s.Get(ctx, client.ObjectKey{Name: "app-race"}, &ns); err != nil || !ns.DeletionTimestamp.IsZero() {
		t.Fatalf("adopted namespace deleted: %v", err)
	}
}

// A restore whose app does not become healthy again ends after the app's
// health timeout instead of holding the app forever.
func TestRestoreChangeEndsWhenUnhealthy(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "sick")
	defer removeApp(t, app)
	keep := &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "sick-keep"}, Spec: cellv1.BackupSpec{App: "sick", Reason: "manual"}}
	if err := k8s.Create(ctx, keep); err != nil {
		t.Fatal(err)
	}
	completeBackup(t, "app-sick", "sick-keep", "keep-s", "snap-s")
	approvedChange(t, "restore-sick", cellv1.ChangeRequestSpec{App: "sick", Action: "restore", Backup: "sick-keep"})
	completeBackup(t, "app-sick", "restore-sick-pre", "pre-s", "snap-pre-s")
	// Play a restore that finished while the app stays down.
	healthy.Store("sick", false)
	eventually(t, func() error {
		var rs cellv1.Restore
		if err := k8s.Get(ctx, client.ObjectKey{Name: "restore-sick-restore"}, &rs); err != nil {
			return err
		}
		rs.Status.Phase, rs.Status.CompletedAt = cellv1.RunCompleted, ptr(metav1.Now())
		return k8s.Status().Update(ctx, &rs)
	})
	changePhase(t, "restore-sick", cellv1.RunRunning)
	skew.Store(int64(2 * time.Minute)) // past the app's health timeout
	defer skew.Store(0)
	failed := changePhase(t, "restore-sick", cellv1.RunFailed)
	if !strings.Contains(failed.Status.Message, "not healthy after") || !strings.Contains(failed.Status.Message, "restore-sick-pre") {
		t.Errorf("change log %q", failed.Status.Message)
	}
	eventually(t, func() error {
		var a cellv1.App
		_ = k8s.Get(ctx, client.ObjectKey{Name: "sick"}, &a)
		if h := a.Annotations[lockKey]; h != "" {
			return fmt.Errorf("still held by %s", h)
		}
		return nil
	})
}

// M3: a restore is a change: the current state is backed up first, then
// the named backup comes back under the change's hold.
func TestRestoreChange(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "vault")
	defer removeApp(t, app)
	keep := &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "vault-keep"}, Spec: cellv1.BackupSpec{App: "vault", Reason: "manual"}}
	if err := k8s.Create(ctx, keep); err != nil {
		t.Fatal(err)
	}
	completeBackup(t, "app-vault", "vault-keep", "keep-1", "snap-keep")

	approvedChange(t, "restore-vault", cellv1.ChangeRequestSpec{App: "vault", Action: "restore", Backup: "vault-keep"})
	completeBackup(t, "app-vault", "restore-vault-pre", "pre-v", "snap-pre")
	var rs cellv1.Restore
	eventually(t, func() error { return k8s.Get(ctx, client.ObjectKey{Name: "restore-vault-restore"}, &rs) })
	if rs.Spec.Change != "restore-vault" || rs.Spec.Backup != "vault-keep" {
		t.Errorf("restore %+v", rs.Spec)
	}
	phase(t, "vault", cellv1.PhaseRestoring)
	eventually(t, func() error {
		var d appsv1.Deployment
		_ = k8s.Get(ctx, client.ObjectKey{Namespace: "app-vault", Name: "vault"}, &d)
		if d.Spec.Replicas == nil || *d.Spec.Replicas != 0 {
			return fmt.Errorf("app not stopped for restore")
		}
		return nil
	})
	job := completeJob(t, "app-vault", jobName("restore", "restore-vault-restore"), "[]")
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "SNAPSHOT" && e.Value != "snap-keep" {
			t.Errorf("restores snapshot %s, want snap-keep", e.Value)
		}
	}
	var recovered *unstructured.Unstructured
	eventually(t, func() error {
		c, err := get(ctx, gvkCluster, "app-vault", "db")
		if err != nil {
			return err
		}
		_ = k8s.Get(ctx, client.ObjectKey{Name: "restore-vault-restore"}, &rs)
		if c.GetAnnotations()[restoreAnnotation] != string(rs.UID) {
			return fmt.Errorf("database not recreated yet")
		}
		recovered = c
		return nil
	})
	setReady(t, recovered)
	eventually(t, func() error {
		var d appsv1.Deployment
		_ = k8s.Get(ctx, client.ObjectKey{Namespace: "app-vault", Name: "vault"}, &d)
		if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 {
			return fmt.Errorf("app not started again")
		}
		return nil
	})
	kubelet(t, "app-vault", "vault")
	done := changePhase(t, "restore-vault", cellv1.RunSucceeded)
	if !strings.Contains(done.Status.Message, "vault-keep restored") {
		t.Errorf("change log %q", done.Status.Message)
	}
	phase(t, "vault", cellv1.PhaseReady)
}

// M3: only listed groups may use an app; a group the identity does not
// know keeps the app from becoming ready.
func TestAccessGroups(t *testing.T) {
	ctx := context.Background()
	spec := plainSpec("crew")
	spec.Access.Auth.Mode = "oidc"
	spec.Access.Groups = []string{"staff"}
	app := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "crew"}, Spec: spec}
	if err := k8s.Create(ctx, app); err != nil {
		t.Fatal(err)
	}
	defer removeApp(t, app)
	eventually(t, func() error {
		ident.mu.Lock()
		defer ident.mu.Unlock()
		if got := ident.access["crew"]; !slices.Equal(got, []string{"staff"}) {
			return fmt.Errorf("access %v", got)
		}
		return nil
	})
	eventually(t, func() error {
		_ = k8s.Get(ctx, client.ObjectKey{Name: "crew"}, app)
		app.Spec.Access.Groups = []string{"missing"}
		return k8s.Update(ctx, app)
	})
	eventually(t, func() error {
		_ = k8s.Get(ctx, client.ObjectKey{Name: "crew"}, app)
		c := apimeta.FindStatusCondition(app.Status.Conditions, condReady)
		if c == nil || !strings.Contains(c.Message, `group "missing"`) {
			return fmt.Errorf("ready condition %+v", c)
		}
		return nil
	})

	err := k8s.Create(ctx, &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "cell-api"}, Spec: plainSpec("cellapi")})
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "belong to the platform") {
		t.Errorf("app named like the platform's own clients: %v", err)
	}
	hijack := plainSpec("auth")
	err = k8s.Create(ctx, &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "fake-login"}, Spec: hijack})
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "belong to the cell") {
		t.Errorf("app on the identity's subdomain: %v", err)
	}

	noLogin := plainSpec("open")
	noLogin.Access.Groups = []string{"staff"}
	err = k8s.Create(ctx, &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "open"}, Spec: noLogin})
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "groups need a login") {
		t.Errorf("groups without login: %v", err)
	}
}

// The API server rejects actions with the wrong fields.
func TestChangeActionValidation(t *testing.T) {
	ctx := context.Background()
	for name, spec := range map[string]cellv1.ChangeRequestSpec{
		"install-without-spec":   {Action: "install"},
		"update-without-patch":   {},
		"restore-without-backup": {Action: "restore"},
		"restore-latest":         {Action: "restore", Backup: "latest"},
		"update-with-backup":     {Patch: rawJSON(t, map[string]string{"version": "2"}), Backup: "b"},
		"install-deletes-data":   {Action: "install", Patch: rawJSON(t, plainSpec("x")), DeleteData: true},
	} {
		spec.App, spec.Requester = "x", cellv1.Requester{Kind: "Human", Name: "alice"}
		err := k8s.Create(ctx, &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec})
		if !apierrors.IsInvalid(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	cr := &cellv1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "action-fixed"},
		Spec: cellv1.ChangeRequestSpec{App: "nothing", Action: "remove", Requester: cellv1.Requester{Kind: "Human", Name: "alice"},
			Approval: cellv1.Approval{Required: true}},
	}
	if err := k8s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	cr.Spec.Action, cr.Spec.Backup = "restore", "b1"
	if err := k8s.Update(ctx, cr); !apierrors.IsInvalid(err) {
		t.Errorf("action changed: %v", err)
	}
}
