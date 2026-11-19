// Package controller contains the App operator: it turns an App (manifest
// plus customer choices) into a running, reachable app with database, login
// and certificate, keeps it running, and removes it while keeping its data.
package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
	"github.com/marcelheidebrecht/cell-platform/internal/authentik"
	"github.com/marcelheidebrecht/cell-platform/internal/cell"
)

const (
	finalizer  = "cell.internal/app"
	fieldOwner = "cell-operator"

	condDatabase = "DatabaseReady"
	condLogin    = "LoginReady"
	condWorkload = "WorkloadReady"
	condReady    = "Ready"
)

var (
	gvkCluster     = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}
	gvkHelmRelease = schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}
	gvkHelmRepo    = schema.GroupVersionKind{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "HelmRepository"}
	gvkOCIRepo     = schema.GroupVersionKind{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "OCIRepository"}
	gvkMiddleware  = schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "Middleware"}
)

// Identity manages login clients; implemented by *authentik.Client.
type Identity interface {
	EnsureOIDC(ctx context.Context, o authentik.OIDC) error
	EnsureProxy(ctx context.Context, p authentik.Proxy) error
	Delete(ctx context.Context, slug string) error
}

type AppReconciler struct {
	client.Client
	// loginSynced remembers when each app's login client was last written
	// (app name -> loginSync), so the identity is not written on every
	// reconcile but still heals drift and operator restarts.
	loginSynced sync.Map
	Identity    Identity
	// SystemCAs are the public roots of the operator image (PEM).
	SystemCAs []byte
	// BridgeImage runs the loopback bridge in local (*.localhost) cells.
	BridgeImage string
	// Probe checks an app's health endpoint; nil means HTTP GET.
	Probe func(ctx context.Context, url, host string) error
	// ForwardAuth checks that the identity's outpost serves a proxy app's
	// host; nil means asking the outpost over HTTP.
	ForwardAuth func(ctx context.Context, host string) error
	Now         func() time.Time
}

func (r *AppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	byLabel := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		if name := o.GetLabels()[cellv1.LabelApp]; name != "" {
			return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: name}}}
		}
		return nil
	})
	watch := func(gvk schema.GroupVersionKind) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		return u
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&cellv1.App{}).
		Watches(&appsv1.Deployment{}, byLabel).
		Watches(watch(gvkCluster), byLabel).
		Watches(watch(gvkHelmRelease), byLabel).
		Complete(r)
}

// +kubebuilder:rbac:groups=cell.internal,resources=apps;apps/status;apps/finalizers,verbs=*

func (r *AppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var app cellv1.App
	if err := r.Get(ctx, req.NamespacedName, &app); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !app.DeletionTimestamp.IsZero() {
		return r.remove(ctx, &app)
	}
	if controllerutil.AddFinalizer(&app, finalizer) {
		if err := r.Update(ctx, &app); err != nil {
			return ctrl.Result{}, err
		}
	}

	before := app.Status.DeepCopy()
	healthy, msg, err := r.install(ctx, &app)
	if err != nil {
		msg = err.Error()
	}
	r.setPhase(&app, healthy, msg)
	app.Status.ObservedGeneration = app.Generation
	if !equality.Semantic.DeepEqual(before, &app.Status) {
		if uerr := r.Status().Update(ctx, &app); uerr != nil {
			return ctrl.Result{}, uerr
		}
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if healthy {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *AppReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// setPhase derives the phase: Ready when healthy, NeedsAttention when
// unhealthy for longer than the manifest allows, Installing before that.
func (r *AppReconciler) setPhase(app *cellv1.App, healthy bool, msg string) {
	if healthy {
		r.condition(app, condReady, true, "Healthy", "health check passed")
		app.Status.Phase = cellv1.PhaseReady
		app.Status.Version = app.Spec.Version
		return
	}
	r.condition(app, condReady, false, "Unhealthy", msg)
	since := apimeta.FindStatusCondition(app.Status.Conditions, condReady).LastTransitionTime.Time
	timeout := time.Duration(app.Spec.Health.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 300 * time.Second
	}
	switch {
	case r.now().Sub(since) > timeout:
		app.Status.Phase = cellv1.PhaseNeedsAttention
	case app.Status.Phase == "":
		app.Status.Phase = cellv1.PhaseInstalling
	}
}

func (r *AppReconciler) condition(app *cellv1.App, typ string, ok bool, reason, msg string) {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}
	apimeta.SetStatusCondition(&app.Status.Conditions, metav1.Condition{
		Type: typ, Status: status, Reason: reason, Message: msg,
		ObservedGeneration: app.Generation, LastTransitionTime: metav1.NewTime(r.now()),
	})
}

// install brings every part of the app to its desired state, in order, and
// reports whether the app answers its health check.
func (r *AppReconciler) install(ctx context.Context, app *cellv1.App) (bool, string, error) {
	cfg, err := cell.LoadConfig(ctx, r)
	if err != nil {
		return false, "", err
	}
	ns := Namespace(app.Name)
	host := cfg.Host(app.Spec.Access.Subdomain)
	cellCA, err := cell.LoadCA(ctx, r, cfg)
	if err != nil {
		return false, "", err
	}
	if _, err := r.apply(ctx, namespaceObj(app, false), caBundle(app, cellCA, r.SystemCAs)); err != nil {
		return false, "", err
	}

	data := map[string]any{
		"app":    map[string]any{"name": app.Name, "version": app.Spec.Version, "namespace": ns},
		"cell":   map[string]any{"domain": cfg.Domain, "caConfigMap": caConfigMap},
		"access": map[string]any{"host": host, "url": "https://" + host},
	}
	secrets := map[string]any{}
	for _, name := range app.Spec.Needs.Secrets {
		if _, err := r.ensureSecret(ctx, ns, "secret-"+name, corev1.SecretTypeOpaque, map[string]string{"password": token()}); err != nil {
			return false, "", err
		}
		secrets[name] = "secret-" + name
	}
	data["secrets"] = secrets
	volumes := map[string]any{}
	for _, v := range app.Spec.Needs.Volumes {
		volumes[v.Name] = map[string]any{"claim": v.Name}
	}
	data["volumes"] = volumes

	if app.Spec.Needs.Database != nil {
		ready, msg, err := r.ensureDatabase(ctx, app)
		if err != nil || !ready {
			r.condition(app, condDatabase, false, "Waiting", msg)
			return false, "database: " + msg, err
		}
		r.condition(app, condDatabase, true, "Ready", "")
		data["database"] = map[string]any{
			"host": dbCluster + "-rw." + ns + ".svc.cluster.local", "port": "5432",
			"name": dbName(app), "user": dbName(app), "secretName": dbCredentials,
		}
	}

	if err := r.ensureLogin(ctx, app, cfg, host, data); err != nil {
		r.condition(app, condLogin, false, "Error", err.Error())
		return false, "login: " + err.Error(), err
	}

	ready, msg, err := r.ensureWorkload(ctx, app, cfg, data)
	if err != nil || !ready {
		r.condition(app, condWorkload, false, "Waiting", msg)
		return false, "workload: " + msg, err
	}
	r.condition(app, condWorkload, true, "Ready", "")
	svc := app.Name
	if app.Spec.Access.Service != "" {
		if svc, err = render(app.Spec.Access.Service, data); err != nil {
			return false, err.Error(), nil
		}
	}
	published, err := r.apply(ctx, ingress(app, host, svc)...)
	if err != nil {
		return false, "", err
	}
	app.Status.URL = "https://" + host
	if ok, msg, err := readyCondition(findKind(published, "Certificate")); err != nil || !ok {
		return false, "certificate: " + msg, err
	}

	path := app.Spec.Health.Path
	if path == "" {
		path = "/"
	}
	url := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s", svc, ns, app.Spec.Access.Port, path)
	if err := r.probe(ctx, url, host); err != nil {
		return false, "health check: " + err.Error(), nil
	}
	if app.Spec.Access.Auth.Mode == "proxy" {
		if err := r.forwardAuth(ctx, host); err != nil {
			return false, "login proxy: " + err.Error(), nil
		}
	}
	return true, "", nil
}

// forwardAuth asks the outpost the way Traefik does. It answers with a
// redirect to the login (or 401) once it serves the host, 404 before that.
func (r *AppReconciler) forwardAuth(ctx context.Context, host string) error {
	if r.ForwardAuth != nil {
		return r.ForwardAuth(ctx, host)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+authentikSvc+"."+cell.SystemNamespace+".svc.cluster.local/outpost.goauthentik.io/auth/traefik", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Forwarded-Host", host)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Method", http.MethodGet)
	req.Header.Set("X-Forwarded-Uri", "/")
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusFound, http.StatusTemporaryRedirect, http.StatusUnauthorized:
		return nil
	}
	return fmt.Errorf("outpost does not serve %s yet (HTTP %d)", host, resp.StatusCode)
}

func (r *AppReconciler) ensureDatabase(ctx context.Context, app *cellv1.App) (bool, string, error) {
	ns := Namespace(app.Name)
	if _, err := r.ensureSecret(ctx, ns, dbCredentials, corev1.SecretTypeBasicAuth,
		map[string]string{"username": dbName(app), "password": token()}); err != nil {
		return false, "", err
	}
	applied, err := r.apply(ctx, database(app, false))
	if err != nil {
		return false, "", err
	}
	return readyCondition(applied[0])
}

// ensureLogin registers the app with the identity once per spec generation.
func (r *AppReconciler) ensureLogin(ctx context.Context, app *cellv1.App, cfg cell.Config, host string, data map[string]any) error {
	ns := Namespace(app.Name)
	authURL := "https://" + cfg.Host("auth")
	issuer := authURL + "/application/o/" + app.Name + "/"
	mode := app.Spec.Access.Auth.Mode
	if mode == "oidc" {
		idKey, secretKey, err := secretKeyNames(app)
		if err != nil {
			return err
		}
		creds, err := r.ensureSecret(ctx, ns, oidcSecret, corev1.SecretTypeOpaque,
			map[string]string{idKey: app.Name, secretKey: token()})
		if err != nil {
			return err
		}
		clientID, clientSecret := string(creds[idKey]), string(creds[secretKey])
		if clientID == "" || clientSecret == "" {
			return fmt.Errorf("secret %s/%s lacks keys %s and %s", ns, oidcSecret, idKey, secretKey)
		}
		data["auth"] = map[string]any{
			"issuer": issuer, "discoveryUrl": issuer + ".well-known/openid-configuration",
			"clientId": clientID, "secretName": oidcSecret,
		}
		if r.loginCurrent(app) {
			return nil
		}
		var redirects []string
		for _, p := range app.Spec.Access.Auth.RedirectPaths {
			redirects = append(redirects, "https://"+host+p)
		}
		if err := r.Identity.EnsureOIDC(ctx, authentik.OIDC{
			Slug: app.Name, Title: app.Spec.Title, LaunchURL: "https://" + host,
			ClientID: clientID, ClientSecret: clientSecret, RedirectURIs: redirects,
		}); err != nil {
			return err
		}
	} else if mode == "proxy" {
		if r.loginCurrent(app) {
			return nil
		}
		if err := r.Identity.EnsureProxy(ctx, authentik.Proxy{
			Slug: app.Name, Title: app.Spec.Title, ExternalHost: "https://" + host, AuthentikURL: authURL,
		}); err != nil {
			return err
		}
	}
	r.loginSynced.Store(app.Name, loginSync{generation: app.Generation, at: r.now()})
	r.condition(app, condLogin, true, "Ready", mode)
	return nil
}

// secretKeyNames returns the key names of the OIDC client secret. Equal
// names would store the client secret as the (public) client ID.
func secretKeyNames(app *cellv1.App) (id, secret string, err error) {
	id, secret = app.Spec.Access.Auth.SecretKeys.ClientID, app.Spec.Access.Auth.SecretKeys.ClientSecret
	if id == "" {
		id = "clientId"
	}
	if secret == "" {
		secret = "clientSecret"
	}
	if id == secret {
		return "", "", fmt.Errorf("access.auth.secretKeys: clientId and clientSecret are both %q", id)
	}
	return id, secret, nil
}

type loginSync struct {
	generation int64
	at         time.Time
}

// loginResync is how often a login client is rewritten without spec changes.
const loginResync = 10 * time.Minute

func (r *AppReconciler) loginCurrent(app *cellv1.App) bool {
	v, ok := r.loginSynced.Load(app.Name)
	if !ok {
		return false
	}
	s := v.(loginSync)
	return s.generation == app.Generation && r.now().Sub(s.at) < loginResync
}

func (r *AppReconciler) ensureWorkload(ctx context.Context, app *cellv1.App, cfg cell.Config, data map[string]any) (bool, string, error) {
	src := app.Spec.Source
	if src.Image != nil {
		img, err := render(*src.Image, data)
		if err != nil {
			return false, err.Error(), nil
		}
		bridge := ""
		if cell.InDomain(cfg.Domain, "localhost") {
			bridge = r.BridgeImage
		}
		applied, err := r.apply(ctx, imageWorkload(app, img, bridge)...)
		if err != nil {
			return false, "", err
		}
		var d appsv1.Deployment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(findKind(applied, "Deployment").Object, &d); err != nil {
			return false, "", err
		}
		s := d.Status
		if s.ObservedGeneration < d.Generation || s.UpdatedReplicas < 1 || s.AvailableReplicas < s.UpdatedReplicas || s.Replicas > s.UpdatedReplicas {
			return false, fmt.Sprintf("deployment rolling out: %d updated, %d available", s.UpdatedReplicas, s.AvailableReplicas), nil
		}
		return true, "", nil
	}
	helm, err := render(*src.Helm, data)
	if err != nil {
		return false, err.Error(), nil
	}
	objs, err := helmWorkload(app, helm)
	if err != nil {
		return false, err.Error(), nil
	}
	applied, err := r.apply(ctx, objs...)
	if err != nil {
		return false, "", err
	}
	source := findKind(applied, "OCIRepository")
	if source == nil {
		source = findKind(applied, "HelmRepository")
	}
	if err := r.deleteOtherSources(ctx, app, source.GetName()); err != nil {
		return false, "", err
	}
	return helmReady(source, findKind(applied, "HelmRelease"), helm.Version)
}

// deleteOtherSources removes chart sources the app no longer uses.
func (r *AppReconciler) deleteOtherSources(ctx context.Context, app *cellv1.App, keep string) error {
	for _, gvk := range []schema.GroupVersionKind{gvkOCIRepo, gvkHelmRepo} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := r.List(ctx, list, client.InNamespace(Namespace(app.Name)), client.MatchingLabels{cellv1.LabelApp: app.Name}); err != nil {
			return err
		}
		for i := range list.Items {
			if o := &list.Items[i]; o.GetName() != keep {
				if err := r.Delete(ctx, o); client.IgnoreNotFound(err) != nil {
					return err
				}
			}
		}
	}
	return nil
}

// helmReady requires the chart source and the release to be reconciled for
// their current specs and the latest deployed release to be the desired
// chart: a new OCI chart version or repository changes only the source, not
// the HelmRelease. For OCI sources the deployed chart must also come from
// the source's current artifact (Flux puts its digest into the version as
// build metadata, 1.2.3+<digest prefix>).
func helmReady(source, hr *unstructured.Unstructured, chartVersion string) (bool, string, error) {
	if ready, msg, err := readyCondition(source); err != nil || !ready {
		return false, "chart source: " + msg, err
	}
	if ready, msg, err := readyCondition(hr); err != nil || !ready {
		return ready, msg, err
	}
	history, _, _ := unstructured.NestedSlice(hr.Object, "status", "history")
	if len(history) == 0 {
		return false, "no Helm release yet", nil
	}
	latest, _ := history[0].(map[string]any)
	deployedVersion, digest, _ := strings.Cut(fmt.Sprint(latest["chartVersion"]), "+")
	if deployedVersion != chartVersion || latest["status"] != "deployed" {
		return false, fmt.Sprintf("Helm release has chart %v (%v), want %s", latest["chartVersion"], latest["status"], chartVersion), nil
	}
	if source.GetKind() == "OCIRepository" {
		revision, _, _ := unstructured.NestedString(source.Object, "status", "artifact", "revision")
		_, artifact, _ := strings.Cut(revision, "@sha256:")
		if digest == "" || artifact == "" || !strings.HasPrefix(artifact, digest) {
			return false, fmt.Sprintf("deployed chart %v is not the source artifact %s", latest["chartVersion"], revision), nil
		}
	}
	return true, "", nil
}

// readyCondition reads status.conditions[Ready] of any resource. A status
// or condition written for an older generation does not count.
func readyCondition(u *unstructured.Unstructured) (bool, string, error) {
	if observed, found, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration"); found && observed < u.GetGeneration() {
		return false, fmt.Sprintf("%s generation %d not observed yet", u.GetKind(), u.GetGeneration()), nil
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, raw := range conds {
		c, _ := raw.(map[string]any)
		if c["type"] != "Ready" {
			continue
		}
		// Some controllers (cert-manager) record the generation per condition.
		if observed, found, _ := unstructured.NestedInt64(c, "observedGeneration"); found && observed < u.GetGeneration() {
			return false, fmt.Sprintf("%s generation %d not observed yet", u.GetKind(), u.GetGeneration()), nil
		}
		return c["status"] == "True", fmt.Sprint(c["message"]), nil
	}
	return false, u.GetKind() + " not ready yet", nil
}

func (r *AppReconciler) probe(ctx context.Context, url, host string) error {
	if r.Probe != nil {
		return r.Probe(ctx, url, host)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Host = host
	req.Header.Set("X-Forwarded-Proto", "https")
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// remove deletes the running parts of an app and its login client but keeps
// its data: volumes stay, the database is hibernated, and the namespace is
// marked as retained. Installing the app again picks the data up.
func (r *AppReconciler) remove(ctx context.Context, app *cellv1.App) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(app, finalizer) {
		return ctrl.Result{}, nil
	}
	if app.Status.Phase != cellv1.PhaseRemoving {
		app.Status.Phase = cellv1.PhaseRemoving
		if err := r.Status().Update(ctx, app); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.Identity.Delete(ctx, app.Name); err != nil {
		return ctrl.Result{}, err
	}
	r.loginSynced.Delete(app.Name)
	ns := Namespace(app.Name)
	obj := func(gvk schema.GroupVersionKind, ns, name string) client.Object {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		u.SetNamespace(ns)
		u.SetName(name)
		return u
	}
	if err := r.deleteOtherSources(ctx, app, ""); err != nil && !apimeta.IsNoMatchError(err) {
		return ctrl.Result{}, err
	}
	for _, o := range []client.Object{
		obj(gvkHelmRelease, ns, app.Name),
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: app.Name}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: app.Name}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: filesConfig}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: app.Name}},
		obj(gvkMiddleware, ns, authMiddle),
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: cell.SystemNamespace, Name: outpostIngress(app)}},
	} {
		if err := r.Delete(ctx, o); err != nil && !apierrors.IsNotFound(err) && !apimeta.IsNoMatchError(err) {
			return ctrl.Result{}, fmt.Errorf("delete %T %s: %w", o, o.GetName(), err)
		}
	}
	// Flux uninstalls the chart before the HelmRelease disappears. Until then
	// the app keeps running and must not be reported as removed.
	hr := &unstructured.Unstructured{}
	hr.SetGroupVersionKind(gvkHelmRelease)
	err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: app.Name}, hr)
	switch {
	case err == nil:
		r.condition(app, condWorkload, false, "Uninstalling", "waiting for the Helm release to be uninstalled")
		if uerr := r.Status().Update(ctx, app); uerr != nil {
			return ctrl.Result{}, uerr
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	case !apierrors.IsNotFound(err) && !apimeta.IsNoMatchError(err):
		return ctrl.Result{}, err
	}
	if app.Spec.Needs.Database != nil {
		if _, err := r.apply(ctx, database(app, true)); err != nil {
			return ctrl.Result{}, err
		}
	}
	if _, err := r.apply(ctx, namespaceObj(app, true)); err != nil {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(app, finalizer)
	return ctrl.Result{}, r.Update(ctx, app)
}

// apply server-side applies objects with the operator as field owner and
// returns the API server's answer for each (current generation and status,
// never a cached copy).
func (r *AppReconciler) apply(ctx context.Context, objs ...client.Object) ([]*unstructured.Unstructured, error) {
	var applied []*unstructured.Unstructured
	for _, o := range objs {
		u, ok := o.(*unstructured.Unstructured)
		if !ok {
			m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
			if err != nil {
				return nil, err
			}
			gvk, err := apiutil.GVKForObject(o, r.Scheme())
			if err != nil {
				return nil, err
			}
			u = &unstructured.Unstructured{Object: m}
			u.SetGroupVersionKind(gvk)
			unstructured.RemoveNestedField(u.Object, "status")
			unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
			unstructured.RemoveNestedField(u.Object, "spec", "template", "metadata", "creationTimestamp")
		}
		gvk := u.GroupVersionKind()
		if err := r.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
			return nil, fmt.Errorf("apply %s %s/%s: %w", gvk.Kind, u.GetNamespace(), u.GetName(), err)
		}
		u.SetGroupVersionKind(gvk)
		applied = append(applied, u)
	}
	return applied, nil
}

func findKind(objs []*unstructured.Unstructured, kind string) *unstructured.Unstructured {
	for _, o := range objs {
		if o.GetKind() == kind {
			return o
		}
	}
	return nil
}

// ensureSecret creates a secret with the given data once and returns the
// data of the existing secret ever after (credentials never rotate silently).
func (r *AppReconciler) ensureSecret(ctx context.Context, ns, name string, typ corev1.SecretType, data map[string]string) (map[string][]byte, error) {
	var s corev1.Secret
	err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &s)
	if err == nil {
		return s.Data, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	s = corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{cellv1.LabelApp: strings.TrimPrefix(ns, "app-")}},
		Type:       typ,
		StringData: data,
	}
	if err := r.Create(ctx, &s); err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for k, v := range data {
		out[k] = []byte(v)
	}
	return out, nil
}

func token() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b)
}
