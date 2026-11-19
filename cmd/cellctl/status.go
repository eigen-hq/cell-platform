package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/marcelheidebrecht/cell-platform/internal/cell"
)

type state string

const (
	ok   state = "ok"
	warn state = "warn"
	fail state = "FAIL"
)

type result struct {
	check  string
	state  state
	detail string
}

// status checks the base services of the cell and returns the exit code.
func status(ctx context.Context, c client.Client, w io.Writer) int {
	cfg, err := cell.LoadConfig(ctx, c)
	if err != nil {
		fmt.Fprintln(w, "cell not bootstrapped:", err)
		return 1
	}
	results := []result{checkNodes(ctx, c)}
	namespaces := []string{"kube-system", "cert-manager", "cnpg-system", cell.SystemNamespace}
	if cfg.Storage == "longhorn" {
		namespaces = append(namespaces, "longhorn-system")
	}
	for _, ns := range namespaces {
		results = append(results, checkWorkloads(ctx, c, ns))
	}
	results = append(results,
		checkReady(ctx, c, "tls issuer", schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "ClusterIssuer"}, "", cell.Issuer),
		checkReady(ctx, c, "identity db", schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}, cell.SystemNamespace, "identity-db"),
		checkReady(ctx, c, "login cert", schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}, cell.SystemNamespace, "auth-tls"),
		checkLogin(ctx, c, cfg),
		checkStorage(ctx, c, cfg),
		checkBackupTarget(ctx, c),
	)

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "CELL\t%s\t(tls %s, storage %s)\n", cfg.Domain, cfg.TLS, cfg.Storage)
	code := 0
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.check, r.state, r.detail)
		if r.state == fail {
			code = 1
		}
	}
	tw.Flush()
	return code
}

func checkNodes(ctx context.Context, c client.Client) result {
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes); err != nil {
		return result{"node", fail, err.Error()}
	}
	var notReady, names []string
	for _, n := range nodes.Items {
		names = append(names, n.Name+" "+n.Status.NodeInfo.KubeletVersion)
		if !nodeReady(n) {
			notReady = append(notReady, n.Name)
		}
	}
	if len(nodes.Items) == 0 || len(notReady) > 0 {
		return result{"node", fail, "not ready: " + strings.Join(notReady, ", ")}
	}
	return result{"node", ok, strings.Join(names, ", ")}
}

func nodeReady(n corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// checkWorkloads requires every Deployment and DaemonSet in ns to run its
// desired number of up-to-date pods.
func checkWorkloads(ctx context.Context, c client.Client, ns string) result {
	name := "workloads " + ns
	var deploys appsv1.DeploymentList
	if err := c.List(ctx, &deploys, client.InNamespace(ns)); err != nil {
		return result{name, fail, err.Error()}
	}
	var daemons appsv1.DaemonSetList
	if err := c.List(ctx, &daemons, client.InNamespace(ns)); err != nil {
		return result{name, fail, err.Error()}
	}
	if len(deploys.Items)+len(daemons.Items) == 0 {
		return result{name, fail, "none found"}
	}
	var bad []string
	for _, d := range deploys.Items {
		if p := deploymentProblem(d); p != "" {
			bad = append(bad, d.Name+" "+p)
		}
	}
	for _, d := range daemons.Items {
		if p := daemonSetProblem(d); p != "" {
			bad = append(bad, d.Name+" "+p)
		}
	}
	if len(bad) > 0 {
		return result{name, fail, strings.Join(bad, ", ")}
	}
	return result{name, ok, fmt.Sprintf("%d deployments, %d daemonsets ready", len(deploys.Items), len(daemons.Items))}
}

func deploymentProblem(d appsv1.Deployment) string {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	s := d.Status
	switch {
	case s.ObservedGeneration < d.Generation:
		return "rollout not observed yet"
	case s.UpdatedReplicas < want || s.AvailableReplicas < want:
		return fmt.Sprintf("%d/%d available, %d updated", s.AvailableReplicas, want, s.UpdatedReplicas)
	}
	return ""
}

func daemonSetProblem(d appsv1.DaemonSet) string {
	s := d.Status
	switch {
	case s.ObservedGeneration < d.Generation:
		return "rollout not observed yet"
	case s.NumberAvailable < s.DesiredNumberScheduled || s.UpdatedNumberScheduled < s.DesiredNumberScheduled:
		return fmt.Sprintf("%d/%d available, %d updated", s.NumberAvailable, s.DesiredNumberScheduled, s.UpdatedNumberScheduled)
	}
	return ""
}

// checkReady reports the Ready condition of any resource that has one.
func checkReady(ctx context.Context, c client.Client, check string, gvk schema.GroupVersionKind, ns, name string) result {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, u); err != nil {
		return result{check, fail, err.Error()}
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, raw := range conds {
		cond, _ := raw.(map[string]any)
		if cond["type"] == "Ready" {
			if cond["status"] == "True" {
				return result{check, ok, gvk.Kind + " " + name}
			}
			return result{check, fail, fmt.Sprintf("%s %s: %v", gvk.Kind, name, cond["message"])}
		}
	}
	return result{check, fail, gvk.Kind + " " + name + ": no Ready condition yet"}
}

func checkLogin(ctx context.Context, c client.Client, cfg cell.Config) result {
	ca, err := cell.LoadCA(ctx, c, cfg)
	if err != nil {
		return result{"login", fail, err.Error()}
	}
	hc, err := cell.HTTPClient(ca)
	if err != nil {
		return result{"login", fail, err.Error()}
	}
	url := "https://" + cfg.Host("auth") + "/api/v3/flows/executor/default-authentication-flow/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return result{"login", fail, err.Error()}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return result{"login", fail, err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result{"login", fail, fmt.Sprintf("%s: HTTP %d", url, resp.StatusCode)}
	}
	return result{"login", ok, "https://" + cfg.Host("auth")}
}

func checkStorage(ctx context.Context, c client.Client, cfg cell.Config) result {
	var classes storagev1.StorageClassList
	if err := c.List(ctx, &classes); err != nil {
		return result{"storage", fail, err.Error()}
	}
	def := ""
	for _, sc := range classes.Items {
		if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			def = sc.Name
		}
	}
	if def == "" {
		return result{"storage", fail, "no default StorageClass"}
	}
	snap := &unstructured.UnstructuredList{}
	snap.SetGroupVersionKind(schema.GroupVersionKind{Group: "snapshot.storage.k8s.io", Version: "v1", Kind: "VolumeSnapshotClassList"})
	err := c.List(ctx, snap)
	if err != nil && !meta.IsNoMatchError(err) {
		return result{"storage", fail, "list VolumeSnapshotClasses: " + err.Error()}
	}
	if len(snap.Items) == 0 {
		if cfg.Storage == "local-path" {
			return result{"storage", warn, def + ", no snapshots (local cell)"}
		}
		return result{"storage", fail, def + ", no VolumeSnapshotClass"}
	}
	return result{"storage", ok, def + ", snapshots via " + snap.Items[0].GetName()}
}

func checkBackupTarget(ctx context.Context, c client.Client) result {
	var s corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: cell.SystemNamespace, Name: "backup-target"}, &s); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return result{"backup target", warn, "not configured (CELL_BACKUP_S3_*)"}
		}
		return result{"backup target", fail, err.Error()}
	}
	return result{"backup target", ok, fmt.Sprintf("%s/%s", s.Data["endpoint"], s.Data["bucket"])}
}
