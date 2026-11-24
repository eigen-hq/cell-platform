package controller

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
	"github.com/marcelheidebrecht/cell-platform/internal/authentik"
)

// Controller tests against a real API server (envtest). Nothing runs pods:
// the tests play CloudNativePG, Flux and the kubelet by writing status.

var (
	k8s     client.Client
	ident   = &fakeIdentity{oidc: map[string]authentik.OIDC{}, proxy: map[string]authentik.Proxy{}, access: map[string][]string{}}
	healthy sync.Map // app name -> bool
	skew    atomic.Int64
	// outpostReady plays authentik's outpost serving proxy apps.
	outpostReady atomic.Bool
)

type fakeIdentity struct {
	mu      sync.Mutex
	oidc    map[string]authentik.OIDC
	proxy   map[string]authentik.Proxy
	access  map[string][]string
	deleted []string
}

func (f *fakeIdentity) EnsureAccess(_ context.Context, slug string, groups []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, oidc := f.oidc[slug]
	_, proxy := f.proxy[slug]
	if !oidc && !proxy {
		return fmt.Errorf("application %q: %w", slug, authentik.ErrNotFound)
	}
	for _, g := range groups {
		if g == "missing" {
			return fmt.Errorf("group %q: %w", g, authentik.ErrNotFound)
		}
	}
	f.access[slug] = groups
	return nil
}

func (f *fakeIdentity) EnsureOIDC(_ context.Context, o authentik.OIDC) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.oidc[o.Slug] = o
	return nil
}

func (f *fakeIdentity) EnsureProxy(_ context.Context, p authentik.Proxy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proxy[p.Slug] = p
	return nil
}

func (f *fakeIdentity) ProxyClientID(_ context.Context, slug string) (string, error) {
	return "client-" + slug, nil
}

func (f *fakeIdentity) Delete(_ context.Context, slug string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, slug)
	return nil
}

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		fmt.Fprintln(os.Stderr, "KUBEBUILDER_ASSETS is not set: run `make test`")
		os.Exit(1)
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../deploy/crd", "testdata"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		panic(err)
	}
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = cellv1.AddToScheme(scheme)
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		panic(err)
	}
	r := &AppReconciler{
		Client:      mgr.GetClient(),
		Identity:    ident,
		SystemCAs:   []byte("SYSTEM ROOTS"),
		BridgeImage: "bridge:test",
		Probe: func(_ context.Context, url, host string) error {
			for name := range healthy.Range {
				if ok, _ := healthy.Load(name); ok == true && host == name.(string)+".cell.localhost" {
					return nil
				}
			}
			return errors.New("connection refused")
		},
		Now: func() time.Time { return time.Now().Add(time.Duration(skew.Load())) },
		ForwardAuth: func(_ context.Context, host, clientID string) error {
			if outpostReady.Load() && strings.HasPrefix(clientID, "client-") {
				return nil
			}
			return errors.New("HTTP 404")
		},
	}
	if err := r.SetupWithManager(mgr); err != nil {
		panic(err)
	}
	now := func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	if err := (&BackupReconciler{Client: mgr.GetClient(), ResticImage: "restic:test", Now: now}).SetupWithManager(mgr); err != nil {
		panic(err)
	}
	if err := (&RestoreReconciler{Client: mgr.GetClient(), ResticImage: "restic:test", Now: now, Restored: r.ForgetLogin}).SetupWithManager(mgr); err != nil {
		panic(err)
	}
	if err := (&ChangeReconciler{Client: mgr.GetClient(), Now: now}).SetupWithManager(mgr); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := mgr.Start(ctx); err != nil {
			panic(err)
		}
	}()
	if k8s, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		panic(err)
	}
	for _, o := range []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cell-system"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cell-config", Namespace: "cell-system"},
			Data: map[string]string{"domain": "cell.localhost", "tls": "acme", "storage": "local-path"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "backup-target", Namespace: "cell-system"},
			StringData: map[string]string{"endpoint": "http://s3.test:8080", "bucket": "backups", "region": "x",
				"accessKey": "ak", "secretKey": "sk", "password": "pw", "prefix": "cell.localhost"}},
	} {
		if err := k8s.Create(ctx, o); err != nil {
			panic(err)
		}
	}
	code := m.Run()
	cancel()
	_ = env.Stop()
	os.Exit(code)
}

func eventually(t *testing.T, f func() error) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		err := f()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func get(ctx context.Context, gvk schema.GroupVersionKind, ns, name string) (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u, k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, u)
}

func setReady(t *testing.T, u *unstructured.Unstructured) {
	t.Helper()
	setReadyObserved(t, u, u.GetGeneration())
}

// setReadyObserved writes Ready=True as a controller would that has seen
// the given generation of the object.
func setReadyObserved(t *testing.T, u *unstructured.Unstructured, generation int64) {
	t.Helper()
	_ = unstructured.SetNestedField(u.Object, generation, "status", "observedGeneration")
	_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{
		"type": "Ready", "status": "True", "reason": "Test", "message": "", "observedGeneration": generation,
		"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
	}}, "status", "conditions")
	if err := k8s.Status().Update(context.Background(), u); err != nil {
		t.Fatal(err)
	}
}

var gvkCertificate = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}

// issued plays cert-manager: the app's certificate is ready.
func issued(t *testing.T, app, host string) {
	t.Helper()
	var cert *unstructured.Unstructured
	eventually(t, func() (err error) {
		if cert, err = get(context.Background(), gvkCertificate, "app-"+app, app+"-tls"); err != nil {
			return err
		}
		if names, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames"); len(names) != 1 || names[0] != host {
			return fmt.Errorf("certificate for %v, want %s", names, host)
		}
		return nil
	})
	setReady(t, cert)
}

// source returns the chart source the app's HelmRelease points to.
func source(t *testing.T, ns, name string) *unstructured.Unstructured {
	t.Helper()
	ctx := context.Background()
	hr, err := get(ctx, gvkHelmRelease, ns, name)
	if err != nil {
		t.Fatal(err)
	}
	if ref, ok, _ := unstructured.NestedString(hr.Object, "spec", "chartRef", "name"); ok {
		src, err := get(ctx, gvkOCIRepo, ns, ref)
		if err != nil {
			t.Fatal(err)
		}
		return src
	}
	ref, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "sourceRef", "name")
	src, err := get(ctx, gvkHelmRepo, ns, ref)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// deployed plays source- and helm-controller: the OCI source holds the chart
// with the given digest and the release deployed it.
func deployed(t *testing.T, ns, name, chartVersion, digest string) {
	t.Helper()
	ctx := context.Background()
	src := source(t, ns, name)
	if digest != "" {
		_ = unstructured.SetNestedField(src.Object, chartVersion+"@sha256:"+digest+"0000", "status", "artifact", "revision")
		chartVersion += "+" + digest
	}
	setReady(t, src)
	hr, err := get(ctx, gvkHelmRelease, ns, name)
	if err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedSlice(hr.Object, []any{map[string]any{
		"chartName": "forgejo", "chartVersion": chartVersion, "status": "deployed",
		"name": name, "namespace": ns, "version": int64(1),
	}}, "status", "history")
	_ = unstructured.SetNestedField(hr.Object, ns, "status", "storageNamespace")
	helmRecord(t, ns, name, 1, "")
	setReady(t, hr)
}

// helmRecord writes the release record Helm keeps for each revision.
func helmRecord(t *testing.T, ns, release string, version int, manifest string) {
	t.Helper()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if err := json.NewEncoder(zw).Encode(map[string]any{"name": release, "manifest": manifest}); err != nil {
		t.Fatal(err)
	}
	_ = zw.Close()
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: fmt.Sprintf("sh.helm.release.v1.%s.v%d", release, version)},
		Type: "helm.sh/release.v1", Data: map[string][]byte{"release": []byte(base64.StdEncoding.EncodeToString(gz.Bytes()))}}
	if err := k8s.Create(context.Background(), s); apierrors.IsAlreadyExists(err) {
		err = k8s.Update(context.Background(), s)
	} else if err != nil {
		t.Fatal(err)
	}
}

// kubelet plays the Deployment controller: one available, updated pod.
func kubelet(t *testing.T, ns, name string) *appsv1.Deployment {
	t.Helper()
	var d appsv1.Deployment
	eventually(t, func() error { return k8s.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &d) })
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	if err := k8s.Status().Update(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

func phase(t *testing.T, name string, want cellv1.Phase) *cellv1.App {
	t.Helper()
	var app cellv1.App
	eventually(t, func() error {
		if err := k8s.Get(context.Background(), client.ObjectKey{Name: name}, &app); err != nil {
			return err
		}
		if app.Status.Phase != want {
			return fmt.Errorf("phase %q, want %q (conditions %+v)", app.Status.Phase, want, app.Status.Conditions)
		}
		return nil
	})
	return &app
}

func gone(t *testing.T, objs ...client.Object) {
	t.Helper()
	for _, o := range objs {
		eventually(t, func() error {
			err := k8s.Get(context.Background(), client.ObjectKeyFromObject(o), o)
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("%T %s still exists (%v)", o, o.GetName(), err)
		})
	}
}

// An image app with database, volume, generated secret, OIDC login and
// files comes up from its manifest alone, and removal keeps its data.
func TestImageAppLifecycle(t *testing.T) {
	ctx := context.Background()
	app := &cellv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "notes"},
		Spec: cellv1.AppSpec{
			Version: "1.0",
			Source: cellv1.Source{Image: &cellv1.ImageSource{
				Repository: "example/notes",
				Env: []corev1.EnvVar{
					{Name: "DB_HOST", Value: "{{ .database.host }}"},
					{Name: "ISSUER", Value: "{{ .auth.issuer }}"},
					{Name: "ADMIN_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "{{ .secrets.admin }}"}, Key: "password"}}},
				},
				Files: []cellv1.File{{Path: "/hooks/setup.sh", Content: "echo {{ .access.url }}", Executable: true}},
			}},
			Needs: cellv1.Needs{
				Database: &cellv1.Database{Engine: "postgres"},
				Volumes:  []cellv1.Volume{{Name: "data", Size: resource.MustParse("1Gi"), MountPath: "/data"}},
				Secrets:  []string{"admin"},
			},
			Access: cellv1.Access{Subdomain: "notes", Port: 8080, Auth: cellv1.Auth{Mode: "oidc", RedirectPaths: []string{"/cb"}}},
			Health: cellv1.Health{Path: "/health", TimeoutSeconds: 300},
			Backup: cellv1.BackupPolicy{Schedule: "none"}, // a running backup would hold the removal
			MCP:    &cellv1.MCP{Path: "/mcp"},
		},
	}
	if err := k8s.Create(ctx, app); err != nil {
		t.Fatal(err)
	}
	ns := "app-notes"

	var cluster *unstructured.Unstructured
	eventually(t, func() (err error) { cluster, err = get(ctx, gvkCluster, ns, "db"); return err })
	if db, _, _ := unstructured.NestedString(cluster.Object, "spec", "bootstrap", "initdb", "database"); db != "notes" {
		t.Errorf("database name %q", db)
	}
	phase(t, "notes", cellv1.PhaseInstalling)
	var d appsv1.Deployment
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: "notes"}, &d); !apierrors.IsNotFound(err) {
		t.Fatalf("workload created before the database was ready: %v", err)
	}

	setReady(t, cluster)
	dep := kubelet(t, ns, "notes")
	pod := dep.Spec.Template.Spec
	env := map[string]corev1.EnvVar{}
	for _, e := range pod.Containers[0].Env {
		env[e.Name] = e
	}
	if env["DB_HOST"].Value != "db-rw.app-notes.svc.cluster.local" ||
		env["ISSUER"].Value != "https://auth.cell.localhost/application/o/notes/" ||
		env["ADMIN_PASSWORD"].ValueFrom.SecretKeyRef.Name != "secret-admin" {
		t.Errorf("placeholders not filled: %+v", env)
	}
	if pod.Containers[0].Image != "example/notes:1.0" {
		t.Errorf("image %s", pod.Containers[0].Image)
	}
	if pod.EnableServiceLinks == nil || *pod.EnableServiceLinks {
		t.Error("service links would put NOTES_PORT=tcp://... into the app's env")
	}
	if len(pod.Containers) != 2 || pod.Containers[1].Image != "bridge:test" {
		t.Error("local cell app without loopback bridge")
	}
	var files corev1.ConfigMap
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: "files"}, &files); err != nil || files.Data["f0"] != "echo https://notes.cell.localhost" {
		t.Errorf("files: %v %v", err, files.Data)
	}
	ident.mu.Lock()
	o := ident.oidc["notes"]
	ident.mu.Unlock()
	if !slices.Equal(o.RedirectURIs, []string{"https://notes.cell.localhost/cb"}) || o.ClientSecret == "" {
		t.Errorf("oidc client: %+v", o)
	}

	healthy.Store("notes", true)
	// Healthy, but the certificate is not issued yet: not ready.
	time.Sleep(time.Second)
	phase(t, "notes", cellv1.PhaseInstalling)
	issued(t, "notes", "notes.cell.localhost")
	ready := phase(t, "notes", cellv1.PhaseReady)
	if ready.Status.MCPEndpoint != "http://notes.app-notes.svc.cluster.local:8080/mcp" {
		t.Errorf("MCP endpoint %q", ready.Status.MCPEndpoint)
	}
	if ready.Status.URL != "https://notes.cell.localhost" || ready.Status.Version != "1.0" {
		t.Errorf("status %+v", ready.Status)
	}
	var ing networkingv1.Ingress
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: "notes"}, &ing); err != nil || ing.Spec.TLS[0].Hosts[0] != "notes.cell.localhost" {
		t.Errorf("ingress: %v %+v", err, ing.Spec)
	}

	// New subdomain: the certificate must be reissued before the app is
	// ready again; its old Ready condition is for the old generation.
	ready.Spec.Access.Subdomain = "notes2"
	if err := k8s.Update(ctx, ready); err != nil {
		t.Fatal(err)
	}
	healthy.Store("notes2", true)
	eventually(t, func() error {
		var a cellv1.App
		_ = k8s.Get(ctx, client.ObjectKey{Name: "notes"}, &a)
		if a.Status.ObservedGeneration != a.Generation || apimeta.IsStatusConditionTrue(a.Status.Conditions, "Ready") {
			return fmt.Errorf("ready before reissue: %+v", a.Status.Conditions)
		}
		return nil
	})
	time.Sleep(500 * time.Millisecond)
	kubelet(t, ns, "notes") // rendered env changed: new rollout
	issued(t, "notes", "notes2.cell.localhost")
	eventually(t, func() error {
		_ = k8s.Get(ctx, client.ObjectKey{Name: "notes"}, ready)
		if !apimeta.IsStatusConditionTrue(ready.Status.Conditions, "Ready") || ready.Status.URL != "https://notes2.cell.localhost" {
			return fmt.Errorf("not ready after reissue: %+v", ready.Status)
		}
		return nil
	})

	// Update: the new version counts only once the Deployment has rolled out.
	ready.Spec.Version = "1.1"
	if err := k8s.Update(ctx, ready); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		var a cellv1.App
		_ = k8s.Get(ctx, client.ObjectKey{Name: "notes"}, &a)
		if a.Status.ObservedGeneration != a.Generation || apimeta.IsStatusConditionTrue(a.Status.Conditions, "Ready") {
			return fmt.Errorf("update not yet seen as rollout: %+v", a.Status)
		}
		if a.Status.Version != "1.0" {
			return fmt.Errorf("version %s reported before rollout", a.Status.Version)
		}
		return nil
	})
	kubelet(t, ns, "notes")
	eventually(t, func() error {
		var a cellv1.App
		_ = k8s.Get(ctx, client.ObjectKey{Name: "notes"}, &a)
		if a.Status.Version != "1.1" || !apimeta.IsStatusConditionTrue(a.Status.Conditions, "Ready") {
			return fmt.Errorf("after rollout: %+v", a.Status)
		}
		return nil
	})
	if err := k8s.Get(ctx, client.ObjectKey{Name: "notes"}, app); err != nil {
		t.Fatal(err)
	}

	if err := k8s.Delete(ctx, app); err != nil {
		t.Fatal(err)
	}
	gone(t, &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "notes"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "notes"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "notes"}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "notes"}})
	cluster, _ = get(ctx, gvkCluster, ns, "db")
	if cluster.GetAnnotations()[hibernateKey] != "on" {
		t.Error("database not hibernated")
	}
	var nsObj corev1.Namespace
	_ = k8s.Get(ctx, client.ObjectKey{Name: ns}, &nsObj)
	if nsObj.Labels[cellv1.LabelRetained] != "true" {
		t.Error("namespace not marked as retained")
	}
	for _, o := range []client.Object{
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "data"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "db-credentials"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "secret-admin"}},
	} {
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(o), o); err != nil {
			t.Errorf("data removed: %v", err)
		}
	}
	ident.mu.Lock()
	if !slices.Contains(ident.deleted, "notes") {
		t.Error("login client not deleted")
	}
	ident.mu.Unlock()
}

// A Helm app becomes a Flux OCIRepository and HelmRelease with filled
// values, and its chart's volumes are kept on uninstall.
func TestHelmApp(t *testing.T) {
	ctx := context.Background()
	app := &cellv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "git"},
		Spec: cellv1.AppSpec{
			Version: "15.0.11",
			Source: cellv1.Source{Helm: &cellv1.HelmSource{
				Repo: "oci://code.example/charts", Chart: "forgejo", Version: "17.3.2",
				Values: jsonValue(t, `{"image":{"tag":"{{ .app.version }}"},"url":"{{ .access.url }}"}`),
			}},
			Access: cellv1.Access{Subdomain: "git", Port: 3000, Service: "git-http", Auth: cellv1.Auth{Mode: "none"}},
			Backup: cellv1.BackupPolicy{Schedule: "none"},
		},
	}
	if err := k8s.Create(ctx, app); err != nil {
		t.Fatal(err)
	}
	var hr *unstructured.Unstructured
	eventually(t, func() (err error) { hr, err = get(ctx, gvkHelmRelease, "app-git", "git"); return err })
	repo := source(t, "app-git", "git")
	if url, _, _ := unstructured.NestedString(repo.Object, "spec", "url"); url != "oci://code.example/charts/forgejo" {
		t.Errorf("chart url %s", url)
	}
	values, _, _ := unstructured.NestedMap(hr.Object, "spec", "values")
	if values["url"] != "https://git.cell.localhost" || values["image"].(map[string]any)["tag"] != "15.0.11" {
		t.Errorf("values %v", values)
	}
	if prs, _, _ := unstructured.NestedSlice(hr.Object, "spec", "postRenderers"); len(prs) != 1 {
		t.Error("no post-renderer keeping volumes")
	}
	// A Ready condition for an older generation of the release is stale.
	healthy.Store("git", true)
	setReadyObserved(t, hr, hr.GetGeneration()-1)
	time.Sleep(time.Second)
	if a := phase(t, "git", cellv1.PhaseInstalling); a.Status.Version != "" {
		t.Errorf("stale release counted as ready: %+v", a.Status)
	}
	deployed(t, "app-git", "git", "17.3.2", "aaaa11112222")
	issued(t, "git", "git.cell.localhost")
	phase(t, "git", cellv1.PhaseReady)

	// A new OCI chart version changes only the source: the old release's
	// Ready status must not count for it.
	var a cellv1.App
	if err := k8s.Get(ctx, client.ObjectKey{Name: "git"}, &a); err != nil {
		t.Fatal(err)
	}
	a.Spec.Source.Helm.Version = "17.3.3"
	if err := k8s.Update(ctx, &a); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		_ = k8s.Get(ctx, client.ObjectKey{Name: "git"}, &a)
		if apimeta.IsStatusConditionTrue(a.Status.Conditions, "Ready") || a.Status.ObservedGeneration != a.Generation {
			return fmt.Errorf("still ready after chart change: %+v", a.Status.Conditions)
		}
		return nil
	})
	deployed(t, "app-git", "git", "17.3.3", "bbbb11112222")
	readyAgain := func() {
		eventually(t, func() error {
			_ = k8s.Get(ctx, client.ObjectKey{Name: "git"}, &a)
			if !apimeta.IsStatusConditionTrue(a.Status.Conditions, "Ready") {
				return fmt.Errorf("not ready with new chart deployed: %+v", a.Status.Conditions)
			}
			return nil
		})
	}
	readyAgain()

	// Another repository with the same chart version: the source changes,
	// the release must come from its artifact again.
	_ = k8s.Get(ctx, client.ObjectKey{Name: "git"}, &a)
	a.Spec.Source.Helm.Repo = "oci://mirror.example/charts"
	if err := k8s.Update(ctx, &a); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		_ = k8s.Get(ctx, client.ObjectKey{Name: "git"}, &a)
		if apimeta.IsStatusConditionTrue(a.Status.Conditions, "Ready") || a.Status.ObservedGeneration != a.Generation {
			return fmt.Errorf("still ready after repository change: %+v", a.Status.Conditions)
		}
		return nil
	})
	deployed(t, "app-git", "git", "17.3.3", "cccc11112222")
	readyAgain()

	// Flux keeps a finalizer until the chart is uninstalled; the app must
	// not disappear before that.
	repo = source(t, "app-git", "git")
	var sources unstructured.UnstructuredList
	sources.SetGroupVersionKind(gvkOCIRepo.GroupVersion().WithKind("OCIRepositoryList"))
	if err := k8s.List(ctx, &sources, client.InNamespace("app-git")); err != nil || len(sources.Items) != 1 {
		t.Errorf("stale chart sources left: %d (%v)", len(sources.Items), err)
	}
	hr, _ = get(ctx, gvkHelmRelease, "app-git", "git")
	hr.SetFinalizers([]string{"finalizers.fluxcd.io"})
	if err := k8s.Update(ctx, hr); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Delete(ctx, app); err != nil {
		t.Fatal(err)
	}
	phase(t, "git", cellv1.PhaseRemoving)
	time.Sleep(2 * time.Second)
	if err := k8s.Get(ctx, client.ObjectKey{Name: "git"}, &cellv1.App{}); err != nil {
		t.Fatalf("app finalized while the chart was still installed: %v", err)
	}
	hr, _ = get(ctx, gvkHelmRelease, "app-git", "git")
	hr.SetFinalizers(nil)
	if err := k8s.Update(ctx, hr); err != nil {
		t.Fatal(err)
	}
	gone(t, &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "git"}}, repo)
}

// Equal key names would put the client secret into the public client ID.
func TestSecretKeyNamesMustDiffer(t *testing.T) {
	app := &cellv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "leak"},
		Spec: cellv1.AppSpec{
			Version: "1",
			Source:  cellv1.Source{Image: &cellv1.ImageSource{Repository: "x"}},
			Access: cellv1.Access{Subdomain: "leak", Port: 80, Auth: cellv1.Auth{
				Mode: "oidc", SecretKeys: cellv1.SecretKeys{ClientSecret: "clientId"},
			}},
		},
	}
	if err := k8s.Create(context.Background(), app); err == nil || !apierrors.IsInvalid(err) {
		t.Errorf("API accepted equal secret keys: %v", err)
	}
	if _, _, err := secretKeyNames(app); err == nil {
		t.Error("controller accepted equal secret keys")
	}
}

// An app without own login sits behind authentik forward auth.
func TestProxyApp(t *testing.T) {
	ctx := context.Background()
	app := &cellv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "board"},
		Spec: cellv1.AppSpec{
			Version: "1.12.0",
			Source:  cellv1.Source{Image: &cellv1.ImageSource{Repository: "traefik/whoami", Tag: "v{{ .app.version }}"}},
			Access:  cellv1.Access{Subdomain: "board", Port: 80, Auth: cellv1.Auth{Mode: "proxy"}},
		},
	}
	if err := k8s.Create(ctx, app); err != nil {
		t.Fatal(err)
	}
	dep := kubelet(t, "app-board", "board")
	if img := dep.Spec.Template.Spec.Containers[0].Image; img != "traefik/whoami:v1.12.0" {
		t.Errorf("image %s", img)
	}
	healthy.Store("board", true)
	issued(t, "board", "board.cell.localhost")
	// Healthy app, but the outpost does not serve it yet: not ready.
	time.Sleep(time.Second)
	phase(t, "board", cellv1.PhaseInstalling)
	outpostReady.Store(true)
	phase(t, "board", cellv1.PhaseReady)
	var ing networkingv1.Ingress
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "app-board", Name: "board"}, &ing); err != nil ||
		ing.Annotations["traefik.ingress.kubernetes.io/router.middlewares"] != "app-board-authentik@kubernetescrd" {
		t.Errorf("ingress without forward auth: %v %v", err, ing.Annotations)
	}
	if _, err := get(ctx, gvkMiddleware, "app-board", "authentik"); err != nil {
		t.Error(err)
	}
	outpost := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "outpost-board"}}
	if err := k8s.Get(ctx, client.ObjectKeyFromObject(outpost), outpost); err != nil {
		t.Error(err)
	}
	// Switching the source kind would leave the old workload running.
	var b cellv1.App
	_ = k8s.Get(ctx, client.ObjectKey{Name: "board"}, &b)
	b.Spec.Source = cellv1.Source{Helm: &cellv1.HelmSource{Repo: "https://charts.example.org", Chart: "board", Version: "1"}}
	if err := k8s.Update(ctx, &b); !apierrors.IsInvalid(err) {
		t.Errorf("source kind change accepted: %v", err)
	}
	// Switching the login mode would collide with the old provider.
	_ = k8s.Get(ctx, client.ObjectKey{Name: "board"}, &b)
	b.Spec.Access.Auth.Mode = "oidc"
	if err := k8s.Update(ctx, &b); !apierrors.IsInvalid(err) {
		t.Errorf("login mode change accepted: %v", err)
	}
	ident.mu.Lock()
	p := ident.proxy["board"]
	ident.mu.Unlock()
	if p.ExternalHost != "https://board.cell.localhost" || p.AuthentikURL != "https://auth.cell.localhost" {
		t.Errorf("proxy provider %+v", p)
	}
	if err := k8s.Delete(ctx, app); err != nil {
		t.Fatal(err)
	}
	gone(t, outpost)
}

// An app that stays unhealthy past its timeout needs attention.
func TestNeedsAttention(t *testing.T) {
	ctx := context.Background()
	app := &cellv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "broken"},
		Spec: cellv1.AppSpec{
			Version: "1",
			Source:  cellv1.Source{Image: &cellv1.ImageSource{Repository: "example/broken"}},
			Access:  cellv1.Access{Subdomain: "broken", Port: 80, Auth: cellv1.Auth{Mode: "none"}},
			Health:  cellv1.Health{TimeoutSeconds: 60},
		},
	}
	if err := k8s.Create(ctx, app); err != nil {
		t.Fatal(err)
	}
	kubelet(t, "app-broken", "broken")
	phase(t, "broken", cellv1.PhaseInstalling)
	skew.Store(int64(2 * time.Minute))
	defer skew.Store(0)
	// Touch the app to reconcile now instead of after the requeue delay.
	eventually(t, func() error {
		var a cellv1.App
		if err := k8s.Get(ctx, client.ObjectKey{Name: "broken"}, &a); err != nil {
			return err
		}
		a.Annotations = map[string]string{"touch": time.Now().String()}
		return k8s.Update(ctx, &a)
	})
	phase(t, "broken", cellv1.PhaseNeedsAttention)
}

// HTTP chart repositories: a different repository with the same chart
// version must be deployed again before the app counts as ready.
func TestHelmRepositoryChange(t *testing.T) {
	ctx := context.Background()
	app := &cellv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "wiki"},
		Spec: cellv1.AppSpec{
			Version: "1",
			Source:  cellv1.Source{Helm: &cellv1.HelmSource{Repo: "https://charts.example.org", Chart: "wiki", Version: "2.0.0"}},
			Access:  cellv1.Access{Subdomain: "wiki", Port: 80, Auth: cellv1.Auth{Mode: "none"}},
			Backup:  cellv1.BackupPolicy{Schedule: "none"},
		},
	}
	if err := k8s.Create(ctx, app); err != nil {
		t.Fatal(err)
	}
	healthy.Store("wiki", true)
	eventually(t, func() error { _, err := get(ctx, gvkHelmRelease, "app-wiki", "wiki"); return err })
	deployed(t, "app-wiki", "wiki", "2.0.0", "")
	issued(t, "wiki", "wiki.cell.localhost")
	phase(t, "wiki", cellv1.PhaseReady)

	var a cellv1.App
	_ = k8s.Get(ctx, client.ObjectKey{Name: "wiki"}, &a)
	a.Spec.Source.Helm.Repo = "https://mirror.example.org"
	if err := k8s.Update(ctx, &a); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		_ = k8s.Get(ctx, client.ObjectKey{Name: "wiki"}, &a)
		if apimeta.IsStatusConditionTrue(a.Status.Conditions, "Ready") || a.Status.ObservedGeneration != a.Generation {
			return fmt.Errorf("still ready after repository change: %+v", a.Status.Conditions)
		}
		return nil
	})
	if url, _, _ := unstructured.NestedString(source(t, "app-wiki", "wiki").Object, "spec", "url"); url != "https://mirror.example.org" {
		t.Fatalf("release points to %s", url)
	}
	deployed(t, "app-wiki", "wiki", "2.0.0", "")
	eventually(t, func() error {
		_ = k8s.Get(ctx, client.ObjectKey{Name: "wiki"}, &a)
		if !apimeta.IsStatusConditionTrue(a.Status.Conditions, "Ready") {
			return fmt.Errorf("not ready after redeploy: %+v", a.Status.Conditions)
		}
		return nil
	})
	if err := k8s.Delete(ctx, &a); err != nil {
		t.Fatal(err)
	}
	gone(t, &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "wiki"}})
}
