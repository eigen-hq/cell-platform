package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// Names of objects the operator creates in an app namespace.
const (
	caConfigMap   = "cell-ca"
	dbCluster     = "db"
	dbCredentials = "db-credentials"
	oidcSecret    = "oidc"
	filesConfig   = "files"
	authMiddle    = "authentik"
	authentikSvc  = "authentik-server"
	hibernateKey  = "cnpg.io/hibernation"
	helmDeployer  = "helm-deployer"
	appNetwork    = "cell-ingress"
)

// Namespace of an app.
func Namespace(app string) string { return "app-" + app }

func labels(app *cellv1.App) map[string]string {
	return map[string]string{
		cellv1.LabelApp:                app.Name,
		"app.kubernetes.io/name":       app.Name,
		"app.kubernetes.io/managed-by": "cell-operator",
	}
}

func meta(app *cellv1.App, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: Namespace(app.Name), Labels: labels(app)}
}

// grants give the operator and the API their rights in the app's namespace
// (E-70): the operator's ClusterRole holds no writes in it, the API's no reads.
func grants(app *cellv1.App) []client.Object {
	bind := func(account, role string) client.Object {
		return &rbacv1.RoleBinding{
			ObjectMeta: meta(app, account),
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: account, Namespace: cell.SystemNamespace}},
		}
	}
	return []client.Object{bind("cell-operator", "cell-operator-app"), bind("cell-api", "cell-api-app")}
}

// namespaceObj is the app's namespace. Pods in it may not be privileged or
// use the host's paths, network or processes, whoever writes them (the
// operator or a chart): an app cannot reach past its namespace.
func namespaceObj(app *cellv1.App, retained bool) *corev1.Namespace {
	l := labels(app)
	l["pod-security.kubernetes.io/enforce"] = "baseline"
	if retained {
		l[cellv1.LabelRetained] = "true"
	}
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(app.Name), Labels: l}}
}

// network lets into an app's pods only its own namespace, the ingress
// (kube-system), the platform (cell-system: MCP gateway, operator) and the
// database operator. Other apps cannot reach it, so the login proxy's
// headers only ever come through the ingress or the gateway.
func network(app *cellv1.App) *networkingv1.NetworkPolicy {
	from := []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}
	for _, ns := range []string{"kube-system", cell.SystemNamespace, "cnpg-system"} {
		from = append(from, networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{corev1.LabelMetadataName: ns},
		}})
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: meta(app, appNetwork),
		Spec: networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: from}},
		},
	}
}

// caBundle holds the cell CA alone (ca.crt) and together with the public
// roots of the operator image (ca-certificates.crt), so apps trust both.
func caBundle(app *cellv1.App, cellCA, systemCAs []byte) *corev1.ConfigMap {
	bundle := string(systemCAs)
	if len(cellCA) > 0 {
		bundle += "\n" + string(cellCA)
	}
	return &corev1.ConfigMap{
		ObjectMeta: meta(app, caConfigMap),
		Data:       map[string]string{"ca.crt": string(cellCA), "ca-certificates.crt": bundle},
	}
}

func dbName(app *cellv1.App) string { return strings.ReplaceAll(app.Name, "-", "_") }

// database is the app's single-instance CloudNativePG cluster as created.
// The owner role is managed, so its password always follows the
// db-credentials secret (also after restoring another cell's database).
// archive names the WAL archive; empty means the cell has no backup target.
func database(app *cellv1.App, archive string) *unstructured.Unstructured {
	size := app.Spec.Needs.Database.Size.String()
	if app.Spec.Needs.Database.Size.IsZero() {
		size = "1Gi"
	}
	spec := map[string]any{
		"instances": int64(1),
		"storage":   map[string]any{"size": size},
		"bootstrap": map[string]any{"initdb": map[string]any{
			"database": dbName(app), "owner": dbName(app),
			"secret": map[string]any{"name": dbCredentials},
		}},
		"managed": map[string]any{"roles": []any{ownerRole(app)}},
	}
	if archive != "" {
		spec["plugins"] = []any{archiverPlugin(archive)}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": dbCluster, "namespace": Namespace(app.Name), "labels": toAny(labels(app))},
		"spec":       spec,
	}}
}

func ownerRole(app *cellv1.App) map[string]any {
	return map[string]any{
		"name": dbName(app), "ensure": "present", "login": true,
		"passwordSecret": map[string]any{"name": dbCredentials},
	}
}

// recoveryDatabase is the cluster recovered to the end of a base backup,
// archiving into a new WAL archive.
func recoveryDatabase(app *cellv1.App, from cellv1.DatabaseBackup, archive, restoreUID string) *unstructured.Unstructured {
	u := database(app, archive)
	u.SetAnnotations(map[string]string{restoreAnnotation: restoreUID})
	spec := u.Object["spec"].(map[string]any)
	spec["bootstrap"] = map[string]any{"recovery": map[string]any{
		"source": "origin", "database": dbName(app), "owner": dbName(app),
		"secret":         map[string]any{"name": dbCredentials},
		"recoveryTarget": map[string]any{"backupID": from.BackupID, "targetImmediate": true},
	}}
	spec["externalClusters"] = []any{map[string]any{
		"name": "origin",
		"plugin": map[string]any{"name": barmanPlugin, "parameters": map[string]any{
			"barmanObjectName": objectStore, "serverName": from.ServerName,
		}},
	}}
	return u
}

func toAny(m map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// imageWorkload is the Deployment, Service, volumes and files of an image app.
// bridgeImage, if set, adds the loopback bridge of local cells (see bridge.go).
func imageWorkload(app *cellv1.App, src cellv1.ImageSource, bridgeImage string) []client.Object {
	var objs []client.Object
	tag := src.Tag
	if tag == "" {
		tag = app.Spec.Version
	}
	container := corev1.Container{
		Name:    "app",
		Image:   src.Repository + ":" + tag,
		Command: src.Command,
		Args:    src.Args,
		Env: append(append([]corev1.EnvVar{}, src.Env...),
			corev1.EnvVar{Name: "SSL_CERT_FILE", Value: "/etc/ssl/certs/ca-certificates.crt"},
			corev1.EnvVar{Name: "REQUESTS_CA_BUNDLE", Value: "/etc/ssl/certs/ca-certificates.crt"},
			corev1.EnvVar{Name: "NODE_EXTRA_CA_CERTS", Value: "/etc/cell/ca.crt"},
		),
		Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: app.Spec.Access.Port}},
		VolumeMounts: []corev1.VolumeMount{
			{Name: caConfigMap, MountPath: "/etc/ssl/certs/ca-certificates.crt", SubPath: "ca-certificates.crt", ReadOnly: true},
			{Name: caConfigMap, MountPath: "/etc/cell/ca.crt", SubPath: "ca.crt", ReadOnly: true},
		},
	}
	volumes := []corev1.Volume{{Name: caConfigMap, VolumeSource: corev1.VolumeSource{
		ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: caConfigMap}},
	}}}

	for _, v := range app.Spec.Needs.Volumes {
		objs = append(objs, &corev1.PersistentVolumeClaim{
			ObjectMeta: meta(app, v.Name),
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: v.Size}},
			},
		})
		volumes = append(volumes, corev1.Volume{Name: "vol-" + v.Name, VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: v.Name},
		}})
		if v.MountPath != "" {
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "vol-" + v.Name, MountPath: v.MountPath})
		}
	}

	hash := sha256.New()
	if len(src.Files) > 0 {
		cm := &corev1.ConfigMap{ObjectMeta: meta(app, filesConfig), Data: map[string]string{}}
		var items []corev1.KeyToPath
		for i, f := range src.Files {
			key := fmt.Sprintf("f%d", i)
			cm.Data[key] = f.Content
			mode := int32(0o644)
			if f.Executable {
				mode = 0o755
			}
			items = append(items, corev1.KeyToPath{Key: key, Path: key, Mode: &mode})
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: filesConfig, MountPath: f.Path, SubPath: key, ReadOnly: true})
			fmt.Fprintf(hash, "%s\x00%s\x00%v\x00", f.Path, f.Content, f.Executable)
		}
		objs = append(objs, cm)
		volumes = append(volumes, corev1.Volume{Name: filesConfig, VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: filesConfig}, Items: items},
		}})
	}

	// No service links: Kubernetes would set <SERVICE>_PORT=tcp://... for
	// the app's own Service, which apps read as their own settings (Memos
	// takes MEMOS_PORT as its port).
	pod := corev1.PodSpec{Containers: []corev1.Container{container}, Volumes: volumes, EnableServiceLinks: ptr(false)}
	if bridgeImage != "" {
		pod.Containers = append(pod.Containers, corev1.Container{
			Name:  "localhost-bridge",
			Image: bridgeImage,
		})
		// The bridge listens on 443; allow it without root.
		pod.SecurityContext = &corev1.PodSecurityContext{Sysctls: []corev1.Sysctl{{Name: "net.ipv4.ip_unprivileged_port_start", Value: "0"}}}
	}
	selector := map[string]string{cellv1.LabelApp: app.Name}
	objs = append(objs,
		&appsv1.Deployment{
			ObjectMeta: meta(app, app.Name),
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(1)),
				Selector: &metav1.LabelSelector{MatchLabels: selector},
				// Volumes are ReadWriteOnce: never run old and new pod together.
				Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels:      labels(app),
						Annotations: map[string]string{cellv1.GroupVersion.Group + "/files-hash": hex.EncodeToString(hash.Sum(nil))[:16]},
					},
					Spec: pod,
				},
			},
		},
		&corev1.Service{
			ObjectMeta: meta(app, app.Name),
			Spec: corev1.ServiceSpec{
				Selector: selector,
				Ports:    []corev1.ServicePort{{Name: "http", Port: app.Spec.Access.Port, TargetPort: intstr.FromString("http")}},
			},
		},
	)
	return objs
}

// helmWorkload is the chart source and Flux HelmRelease of a Helm app.
// PersistentVolumeClaims of the chart are kept on uninstall (data stays).
//
// The source's name is derived from what it points to (repository, and for
// OCI chart and version). Any source change therefore renames the source,
// which changes the HelmRelease spec: readiness for the HelmRelease's
// current generation then always means "deployed from the current source".
func helmWorkload(app *cellv1.App, src cellv1.HelmSource) ([]client.Object, error) {
	ns := Namespace(app.Name)
	m := func(apiVersion, kind, name string, spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": apiVersion, "kind": kind,
			"metadata": map[string]any{"name": name, "namespace": ns, "labels": toAny(labels(app))},
			"spec":     spec,
		}}
	}
	release := map[string]any{
		"interval": "10m",
		// Flux deploys the chart as the app's own deployer, which may change
		// only the app's namespace (not the cluster, as Flux itself may).
		"serviceAccountName": helmDeployer,
		// Never left suspended by the operator, and drift (e.g. workloads a
		// restore scaled to zero) is put back to the chart's state.
		"suspend":        false,
		"driftDetection": map[string]any{"mode": "enabled"},
		"postRenderers": []any{map[string]any{"kustomize": map[string]any{"patches": []any{map[string]any{
			"target": map[string]any{"kind": "PersistentVolumeClaim"},
			"patch":  "apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: any\n  annotations:\n    helm.sh/resource-policy: keep\n",
		}}}}},
	}
	if src.Values != nil {
		var values map[string]any
		if err := json.Unmarshal(src.Values.Raw, &values); err != nil {
			return nil, fmt.Errorf("helm values: %w", err)
		}
		release["values"] = values
	}
	var source *unstructured.Unstructured
	if strings.HasPrefix(src.Repo, "oci://") {
		name := sourceName(app, src.Repo, src.Chart, src.Version)
		source = m("source.toolkit.fluxcd.io/v1", "OCIRepository", name, map[string]any{
			"interval": "1h",
			"url":      strings.TrimSuffix(src.Repo, "/") + "/" + src.Chart,
			"ref":      map[string]any{"tag": src.Version},
			"layerSelector": map[string]any{
				"mediaType": "application/vnd.cncf.helm.chart.content.v1.tar+gzip", "operation": "copy",
			},
		})
		release["chartRef"] = map[string]any{"kind": "OCIRepository", "name": name}
	} else {
		name := sourceName(app, src.Repo)
		source = m("source.toolkit.fluxcd.io/v1", "HelmRepository", name, map[string]any{"interval": "1h", "url": src.Repo})
		release["chart"] = map[string]any{"spec": map[string]any{
			"chart": src.Chart, "version": src.Version,
			"sourceRef": map[string]any{"kind": "HelmRepository", "name": name},
		}}
	}
	deployer := &corev1.ServiceAccount{ObjectMeta: meta(app, helmDeployer)}
	binding := &rbacv1.RoleBinding{
		ObjectMeta: meta(app, helmDeployer),
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "admin"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: helmDeployer, Namespace: ns}},
	}
	return []client.Object{deployer, binding, source, m("helm.toolkit.fluxcd.io/v2", "HelmRelease", app.Name, release)}, nil
}

func sourceName(app *cellv1.App, parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return app.Name + "-" + hex.EncodeToString(h[:])[:8]
}

// certificate for the app's host from the cell issuer. The operator owns it
// (instead of the ingress annotation) so it can wait for its issuance.
func certificate(app *cellv1.App, host string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "Certificate",
		"metadata": map[string]any{"name": app.Name + "-tls", "namespace": Namespace(app.Name), "labels": toAny(labels(app))},
		"spec": map[string]any{
			"secretName": app.Name + "-tls",
			"dnsNames":   []any{host},
			"issuerRef":  map[string]any{"kind": "ClusterIssuer", "name": cell.Issuer},
		},
	}}
}

// ingress publishes the app under its subdomain with its certificate. Proxy apps get the authentik forward-auth middleware and a
// route for the outpost endpoints on their host.
func ingress(app *cellv1.App, host, svc string) []client.Object {
	prefix := networkingv1.PathTypePrefix
	rule := func(path, service string, port int32) networkingv1.IngressRule {
		return networkingv1.IngressRule{Host: host, IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
			Paths: []networkingv1.HTTPIngressPath{{Path: path, PathType: &prefix, Backend: networkingv1.IngressBackend{
				Service: &networkingv1.IngressServiceBackend{Name: service, Port: networkingv1.ServiceBackendPort{Number: port}},
			}}},
		}}}
	}
	ing := &networkingv1.Ingress{
		ObjectMeta: meta(app, app.Name),
		Spec: networkingv1.IngressSpec{
			IngressClassName: ptr("traefik"),
			TLS:              []networkingv1.IngressTLS{{Hosts: []string{host}, SecretName: app.Name + "-tls"}},
			Rules:            []networkingv1.IngressRule{rule("/", svc, app.Spec.Access.Port)},
		},
	}
	ing.Annotations = map[string]string{}
	objs := []client.Object{certificate(app, host), ing}
	if app.Spec.Access.Auth.Mode != "proxy" {
		return objs
	}
	ing.Annotations["traefik.ingress.kubernetes.io/router.middlewares"] = Namespace(app.Name) + "-" + authMiddle + "@kubernetescrd"
	objs = append(objs, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "traefik.io/v1alpha1", "kind": "Middleware",
		"metadata": map[string]any{"name": authMiddle, "namespace": Namespace(app.Name), "labels": toAny(labels(app))},
		"spec": map[string]any{"forwardAuth": map[string]any{
			"address":             "http://" + authentikSvc + "." + cell.SystemNamespace + ".svc.cluster.local/outpost.goauthentik.io/auth/traefik",
			"trustForwardHeader":  true,
			"authResponseHeaders": toAnySlice(proxyHeaders),
		}},
	}})
	outpost := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: outpostIngress(app), Namespace: cell.SystemNamespace, Labels: labels(app),
			Annotations: map[string]string{"traefik.ingress.kubernetes.io/router.tls": "true"}},
		Spec: networkingv1.IngressSpec{
			IngressClassName: ptr("traefik"),
			Rules:            []networkingv1.IngressRule{rule("/outpost.goauthentik.io", authentikSvc, 80)},
		},
	}
	return append(objs, outpost)
}

func outpostIngress(app *cellv1.App) string { return "outpost-" + app.Name }

// proxyHeaders name the user the login proxy let in (auth mode proxy).
// Apps may trust them; the MCP gateway sets them as well.
var proxyHeaders = []string{"X-authentik-username", "X-authentik-groups", "X-authentik-email", "X-authentik-name", "X-authentik-uid"}

func toAnySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func ptr[T any](v T) *T { return &v }
