package api

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func node(name string, server, ready bool, ip string) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}, Annotations: map[string]string{"k3s.io/external-ip": ip}},
		Status: corev1.NodeStatus{
			Capacity:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")},
			NodeInfo:   corev1.NodeSystemInfo{OSImage: "Ubuntu 24.04.5 LTS", KubeletVersion: "v1.37.2+k3s1"},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}},
		},
	}
	if server {
		n.Labels["node-role.kubernetes.io/control-plane"] = "true"
	}
	if ready {
		n.Status.Conditions[0].Status = corev1.ConditionTrue
	}
	return n
}

func TestNodes(t *testing.T) {
	pod := func(ns, name, node string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: corev1.PodSpec{NodeName: node},
			Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	}
	// The API reads pods only in the apps' namespaces, once the operator has
	// bound its role there, and in cell-system (E-70).
	readable := map[string]bool{"cell-system": true, "app-notes": true, "app-files": true}
	funcs := interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		o := (&client.ListOptions{}).ApplyOptions(opts)
		if _, ok := list.(*corev1.PodList); ok && !readable[o.Namespace] {
			return apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", fmt.Errorf("namespace %q", o.Namespace))
		}
		return c.List(ctx, list, opts...)
	}}
	h := newHarnessWith(t, funcs, node("b-worker", false, false, "203.0.113.2"), node("z-server", true, true, "203.0.113.1"),
		installed("notes", "oidc"), installed("files", "oidc"), installed("fresh", "oidc"),
		pod("app-notes", "notes-0", "b-worker"), pod("app-notes", "notes-1", "b-worker"), pod("app-files", "files-0", "z-server"),
		pod("cell-system", "cell-api-0", "z-server"), pod("kube-system", "coredns-0", "b-worker"))
	code, body := h.do("GET", "/v0/nodes", "agent", nil)
	list, _ := body.([]any)
	if code != 200 || len(list) != 2 {
		t.Fatalf("nodes: %d %v", code, body)
	}
	first, second := list[0].(map[string]any), list[1].(map[string]any)
	if first["name"] != "z-server" || first["role"] != "server" || first["ready"] != true || first["address"] != "203.0.113.1" ||
		first["cpu"] != 4.0 || first["memory"] != float64(8<<30) || second["role"] != "agent" || second["ready"] != false ||
		fmt.Sprint(first["apps"]) != "[files]" || first["system"] != true || fmt.Sprint(second["apps"]) != "[notes]" || second["system"] != false {
		t.Errorf("nodes: %v", list)
	}
	if code, _ := h.do("GET", "/v0/nodes", "none", nil); code != 403 {
		t.Errorf("no role lists nodes: %d", code)
	}
}

// A ready machine without usage is flagged only when others have theirs
// and it had time for a measurement.
func TestMarkUnmeasured(t *testing.T) {
	now := time.Now()
	used := 1.0
	old, fresh := now.Add(-time.Hour), now.Add(-time.Minute)
	nodes := []nodeView{
		{Name: "measured", Ready: true, Since: old, CPUUsed: &used},
		{Name: "blocked", Ready: true, Since: old},
		{Name: "new", Ready: true, Since: fresh},
		{Name: "down", Ready: false, Since: old},
	}
	markUnmeasured(nodes, now)
	for _, v := range nodes {
		if v.Unmeasured != (v.Name == "blocked") {
			t.Errorf("%s: unmeasured %v", v.Name, v.Unmeasured)
		}
	}
	none := []nodeView{{Name: "a", Ready: true, Since: old}, {Name: "b", Ready: true, Since: old}}
	markUnmeasured(none, now)
	if none[0].Unmeasured || none[1].Unmeasured {
		t.Errorf("no metrics at all: %v", none)
	}
}

// An admin gets a join command; its key is a k3s bootstrap token that
// expires in an hour, and the script it fetches joins with that key.
func TestJoin(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.do("POST", "/v0/nodes/joins", "admin", nil); code != 409 {
		t.Errorf("join on a one-machine cell: %d", code)
	}
	if code, _ := h.do("GET", "/v0/nodes/join.sh", "", nil); code != 409 {
		t.Errorf("script on a one-machine cell: %d", code)
	}
	h.server.Join = &Join{Server: "203.0.113.1", APIHost: "api.x", CAHash: "ab12", Script: []byte("K3S_VERSION=v1\nsay hi\n")}
	if code, _ := h.do("POST", "/v0/nodes/joins", "agent", nil); code != 403 {
		t.Errorf("agent creates a join key: %d", code)
	}

	code, body := h.do("POST", "/v0/nodes/joins", "admin", nil)
	m := regexp.MustCompile(`^([a-z0-9]{6})\.([a-z0-9]{16})$`).FindStringSubmatch(field(body, "key").(string))
	if code != 201 || m == nil || field(body, "command") != `curl -fsSL https://api.x/v0/nodes/join.sh | bash` ||
		field(body, "expiresAt") != "2026-11-19T13:00:00Z" {
		t.Fatalf("join: %d %v", code, body)
	}
	var s corev1.Secret
	if err := h.kube.Get(context.Background(), client.ObjectKey{Namespace: "kube-system", Name: "bootstrap-token-" + m[1]}, &s); err != nil {
		t.Fatal(err)
	}
	d := s.StringData
	if s.Type != corev1.SecretTypeBootstrapToken || d["token-id"] != m[1] || d["token-secret"] != m[2] ||
		d["auth-extra-groups"] != "system:bootstrappers:k3s:default-node-token" || d["usage-bootstrap-authentication"] != "true" {
		t.Errorf("join key: %v %v", s.Type, d)
	}
	if exp, err := time.Parse(time.RFC3339, d["expiration"]); err != nil || !exp.Equal(time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("expiration: %q", d["expiration"])
	}

	code, body = h.do("GET", "/v0/nodes/join.sh", "", nil)
	script, _ := body.(string)
	if code != 200 || !strings.HasPrefix(script, "#!/usr/bin/env bash\n") || !strings.Contains(script, "\nCELL_JOIN_SERVER=203.0.113.1\n") ||
		!strings.Contains(script, "\nCELL_JOIN_TOKEN=K10ab12::$key\n") || !strings.Contains(script, "K3S_VERSION=v1\nsay hi\nCELL_JOIN_SCRIPT\n") ||
		strings.Contains(script, m[2]) {
		t.Errorf("script: %d %q", code, script)
	}
}

// The join script takes the key from the terminal, or from CELL_JOIN_KEY
// for a run without one, and fails loud with neither (E-64).
func TestJoinScriptKey(t *testing.T) {
	h := newHarness(t)
	h.server.Join = &Join{Server: "203.0.113.1", APIHost: "api.x", CAHash: "ab12",
		Script: []byte(`printf '%s|%s|%s' "$CELL_JOIN_SERVER" "$CELL_JOIN_TOKEN" "${CELL_JOIN_KEY-unset}"` + "\n")}
	_, body := h.do("GET", "/v0/nodes/join.sh", "", nil)
	script, _ := body.(string)
	// Not root, the script runs the join through sudo: a stand-in runs it
	// as this user.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte("#!/bin/sh\ncase $1 in --preserve-env=*) shift ;; esac\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(env ...string) (string, error) {
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(env, "PATH="+bin+":/usr/bin:/bin")
		// No terminal: as with curl piped into sudo-rs, which runs it in the
		// background.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("CELL_JOIN_KEY= abc123.xxxxxxxxxxxxxxxx\r\n")
	if err != nil || out != "203.0.113.1|K10ab12::abc123.xxxxxxxxxxxxxxxx|unset" {
		t.Errorf("key from the environment: %v %q", err, out)
	}
	if out, err := run(); err == nil || !strings.Contains(out, "no terminal to ask for the join key") ||
		!strings.Contains(out, `  curl -fsSL https://api.x/v0/nodes/join.sh | bash`) {
		t.Errorf("no terminal and no key: %v %q", err, out)
	}
	if out, err := run("CELL_JOIN_KEY=abc123.xxxxxxxxxxxxxxxX"); err == nil || !strings.Contains(out, "not a join key") {
		t.Errorf("malformed key: %v %q", err, out)
	}
}
