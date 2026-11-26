package api

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// Join is what a new machine needs to join the cell (E-44).
type Join struct {
	// Server is the address machines reach the cell's server on.
	Server string
	// APIHost serves the join script (api.<domain>).
	APIHost string
	// CAHash is the SHA-256 of the cluster CA, in hex; the joining machine
	// checks the server against it.
	CAHash string
	// Script is versions.env and node.sh, run on the new machine.
	Script []byte
}

const (
	joinTTL = time.Hour
	// joinGroup is the group k3s gives machines that join with a token.
	joinGroup = "system:bootstrappers:k3s:default-node-token"
)

type nodeView struct {
	Name    string    `json:"name"`
	Role    string    `json:"role"` // server | agent
	Ready   bool      `json:"ready"`
	Since   time.Time `json:"since"` // ready (or not) since
	Address string    `json:"address"`
	CPU     int64     `json:"cpu"`    // cores
	Memory  int64     `json:"memory"` // bytes
	Disk    int64     `json:"disk"`   // bytes
	// In use; absent while no measurement is there.
	CPUUsed    *float64 `json:"cpuUsed,omitempty"`    // cores
	MemoryUsed *int64   `json:"memoryUsed,omitempty"` // bytes
	DiskUsed   *int64   `json:"diskUsed,omitempty"`   // bytes, of Disk
	// Apps with a pod on the machine; System: the cell's own services run there.
	Apps    []string  `json:"apps"`
	System  bool      `json:"system"`
	OS      string    `json:"os"`
	Version string    `json:"version"`
	Joined  time.Time `json:"joined"`
}

func viewNode(n *corev1.Node) nodeView {
	v := nodeView{Name: n.Name, Role: "agent", CPU: n.Status.Capacity.Cpu().Value(), Memory: n.Status.Capacity.Memory().Value(),
		Disk: n.Status.Capacity.StorageEphemeral().Value(), Apps: []string{},
		OS: n.Status.NodeInfo.OSImage, Version: n.Status.NodeInfo.KubeletVersion, Joined: n.CreationTimestamp.UTC()}
	if n.Labels["node-role.kubernetes.io/control-plane"] == "true" {
		v.Role = "server"
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			v.Ready = c.Status == corev1.ConditionTrue
			v.Since = c.LastTransitionTime.UTC()
		}
	}
	v.Address = n.Annotations["k3s.io/external-ip"]
	for _, kind := range []corev1.NodeAddressType{corev1.NodeExternalIP, corev1.NodeInternalIP} {
		for _, a := range n.Status.Addresses {
			if v.Address == "" && a.Type == kind {
				v.Address = a.Address
			}
		}
	}
	return v
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	ctx := r.Context()
	var nodes corev1.NodeList
	if err := s.Kube.List(ctx, &nodes); err != nil {
		return err
	}
	// Pods of the apps and of the cell's own services; the API reads no
	// other namespace (E-70).
	var apps cellv1.AppList
	if err := s.Kube.List(ctx, &apps); err != nil {
		return err
	}
	var pods corev1.PodList
	if err := s.Kube.List(ctx, &pods, client.InNamespace(cell.SystemNamespace)); err != nil {
		return err
	}
	for _, a := range apps.Items {
		var list corev1.PodList
		if err := s.appList(ctx, &list, "app-"+a.Name); err != nil {
			return err
		}
		pods.Items = append(pods.Items, list.Items...)
	}
	out := []nodeView{}
	byName := map[string]*nodeView{}
	for i := range nodes.Items {
		out = append(out, viewNode(&nodes.Items[i]))
	}
	for i := range out {
		byName[out[i].Name] = &out[i]
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		v := byName[p.Spec.NodeName]
		if v == nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		if app, ok := strings.CutPrefix(p.Namespace, "app-"); ok {
			if !slices.Contains(v.Apps, app) {
				v.Apps = append(v.Apps, app)
			}
		} else if p.Namespace == cell.SystemNamespace {
			v.System = true
		}
	}
	s.nodeUsage(r, byName)
	for i := range out {
		slices.Sort(out[i].Apps)
	}
	// The server first, then by name.
	slices.SortFunc(out, func(a, b nodeView) int {
		if a.Role != b.Role {
			return strings.Compare(b.Role, a.Role)
		}
		return strings.Compare(a.Name, b.Name)
	})
	writeJSON(w, http.StatusOK, out)
	return nil
}

// nodeUsage adds what the machines use: processor and memory from the
// metrics API, disk from Longhorn (cells on one's own machines). Either may
// be missing, a local cell has no Longhorn; then the view leaves it out.
func (s *Server) nodeUsage(r *http.Request, nodes map[string]*nodeView) {
	ctx := r.Context()
	metrics := &unstructured.UnstructuredList{}
	metrics.SetGroupVersionKind(schema.GroupVersionKind{Group: "metrics.k8s.io", Version: "v1beta1", Kind: "NodeMetricsList"})
	if err := s.Kube.List(ctx, metrics); err != nil {
		s.log().Debug("no node metrics", "error", err)
	}
	for _, m := range metrics.Items {
		v := nodes[m.GetName()]
		cpu, _, _ := unstructured.NestedString(m.Object, "usage", "cpu")
		mem, _, _ := unstructured.NestedString(m.Object, "usage", "memory")
		if v == nil || cpu == "" || mem == "" {
			continue
		}
		c, errC := resource.ParseQuantity(cpu)
		b, errM := resource.ParseQuantity(mem)
		if errC != nil || errM != nil {
			continue
		}
		cores, bytes := float64(c.MilliValue())/1000, b.Value()
		v.CPUUsed, v.MemoryUsed = &cores, &bytes
	}
	disks := &unstructured.UnstructuredList{}
	disks.SetGroupVersionKind(schema.GroupVersionKind{Group: "longhorn.io", Version: "v1beta2", Kind: "NodeList"})
	if err := s.Kube.List(ctx, disks, client.InNamespace("longhorn-system")); err != nil {
		return // no Longhorn: the disk stays the machine's capacity, without use
	}
	for _, d := range disks.Items {
		v := nodes[d.GetName()]
		status, _, _ := unstructured.NestedMap(d.Object, "status", "diskStatus")
		if v == nil || len(status) == 0 {
			continue
		}
		var size, free int64
		for _, disk := range status {
			m, _ := disk.(map[string]any)
			size += number(m["storageMaximum"])
			free += number(m["storageAvailable"])
		}
		if size > 0 {
			used := size - free
			v.Disk, v.DiskUsed = size, &used
		}
	}
}

func number(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

// createJoin makes a join key, valid for an hour (policy cell-api-join-keys
// allows no longer), and the command that joins a machine with it.
func (s *Server) createJoin(w http.ResponseWriter, r *http.Request, c *Caller) error {
	if s.Join == nil {
		return errorf(http.StatusConflict, "this cell takes no more machines (it runs on one machine only)")
	}
	id, secret := randomToken(6), randomToken(16)
	expires := s.now().Add(joinTTL).UTC().Truncate(time.Second)
	err := s.Kube.Create(r.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-token-" + id, Namespace: metav1.NamespaceSystem},
		Type:       corev1.SecretTypeBootstrapToken,
		StringData: map[string]string{
			"token-id": id, "token-secret": secret, "expiration": expires.Format(time.RFC3339),
			"usage-bootstrap-authentication": "true", "usage-bootstrap-signing": "true",
			"auth-extra-groups": joinGroup, "description": "machine join, created by " + c.Username,
		},
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		// The key is pasted when the script asks for it: it stays out of
		// URLs (logs) and off the command line (shell history, ps), E-64.
		"command":   joinCommand(s.Join.APIHost),
		"key":       id + "." + secret,
		"expiresAt": expires,
	})
	return nil
}

// joinScript is the script the join command runs on the new machine. It
// needs no login and holds nothing secret (the server's address and the CA's
// hash). It asks for the join key on the terminal (or takes CELL_JOIN_KEY,
// for a run without one), and k3s on the server checks the key when the
// machine joins. Once the machine is in, the operator deletes the key
// (E-64).
//
// Piped into bash as the user, it runs the join through sudo itself, with
// the terminal as its input: so sudo keeps it in the foreground (sudo-rs
// runs a piped "| sudo bash" in the background, where reading the terminal
// stops it for good) and it can ask for the password and the key.
func (s *Server) joinScript(w http.ResponseWriter, _ *http.Request, _ *Caller) error {
	if s.Join == nil {
		return errorf(http.StatusConflict, "this cell takes no more machines (it runs on one machine only)")
	}
	cmd := joinCommand(s.Join.APIHost)
	w.Header().Set("Content-Type", "text/x-shellscript")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `#!/usr/bin/env bash
# Joins this machine to the cell (E-44):
#
#   %s
#
# It asks for the join key from the console on the terminal: the key stays
# off the command line (E-64).
set -euo pipefail
# Read as a whole before running: a download cut short runs nothing.
{
f=$(mktemp)
trap 'rm -f "$f"' EXIT
cat >"$f" <<'CELL_JOIN_SCRIPT'
key=${CELL_JOIN_KEY:-}
if [ -z "$key" ]; then
  if ! [ -t 0 ]; then
    echo "error: no terminal to ask for the join key on: run this in a terminal (or set CELL_JOIN_KEY):" >&2
    echo '  %s' >&2
    exit 1
  fi
  printf 'Join key (from the console): ' >&2
  IFS= read -rs key || { echo "error: no join key" >&2; exit 1; }
  printf '\n' >&2
fi
key=${key//[[:space:]]/}
[[ $key =~ ^[a-z0-9]{6}\.[a-z0-9]{16}$ ]] || { echo "error: that is not a join key from the console" >&2; exit 1; }
CELL_JOIN_SERVER=%s
CELL_JOIN_TOKEN=K10%s::$key
unset CELL_JOIN_KEY key
`, cmd, cmd, s.Join.Server, s.Join.CAHash)
	if _, err := w.Write(s.Join.Script); err != nil {
		return err
	}
	_, err := fmt.Fprint(w, `CELL_JOIN_SCRIPT
# The join gets the terminal as its input only as the terminal's foreground
# job: in the background, reading it would stop the join for good.
fg=$(ps -o pgid=,tpgid= -p $$ 2>/dev/null | awk '{print ($1 == $2)}') || fg=0
run=(bash "$f")
[ "$(id -u)" -eq 0 ] || run=(sudo ${CELL_JOIN_KEY:+--preserve-env=CELL_JOIN_KEY} bash "$f")
if [ "$fg" = 1 ] && { true </dev/tty; } 2>/dev/null; then
  "${run[@]}" </dev/tty
else
  "${run[@]}" </dev/null
fi
}
`)
	return err
}

// joinCommand is the command the console shows.
func joinCommand(apiHost string) string {
	return fmt.Sprintf(`curl -fsSL https://%s/v0/nodes/join.sh | bash`, apiHost)
}

// randomToken returns n characters from [a-z0-9].
func randomToken(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		c, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars)))) // crypto/rand never fails
		b[i] = chars[c.Int64()]
	}
	return string(b)
}
