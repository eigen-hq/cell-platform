package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
)

func TestRender(t *testing.T) {
	data := map[string]any{
		"database": map[string]any{"host": "db-rw.app-x.svc", "secretName": "db-credentials"},
		"access":   map[string]any{"host": "x.cell.localhost"},
	}
	in := cellv1.ImageSource{
		Repository: "nextcloud",
		Env: []corev1.EnvVar{
			{Name: "HOST", Value: "{{ .database.host }}"},
			{Name: "URL", Value: `https://{{ .access.host }}/"quoted"`},
			{Name: "PW", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "{{ .database.secretName }}"}, Key: "password"}}},
		},
		Files: []cellv1.File{{Path: "/hook.sh", Content: "#!/bin/sh\necho ${HOME} {{ .access.host }}\n"}},
	}
	out, err := render(in, data)
	if err != nil {
		t.Fatal(err)
	}
	if out.Env[0].Value != "db-rw.app-x.svc" || out.Env[1].Value != `https://x.cell.localhost/"quoted"` ||
		out.Env[2].ValueFrom.SecretKeyRef.Name != "db-credentials" {
		t.Errorf("env not rendered: %+v", out.Env)
	}
	if out.Files[0].Content != "#!/bin/sh\necho ${HOME} x.cell.localhost\n" {
		t.Errorf("file not rendered: %q", out.Files[0].Content)
	}
	if in.Env[0].Value != "{{ .database.host }}" {
		t.Error("input was modified")
	}

	_, err = render(cellv1.ImageSource{Tag: "{{ .auth.clientId }}"}, data)
	if err == nil || !strings.Contains(err.Error(), "auth") {
		t.Errorf("missing placeholder not reported: %v", err)
	}
}

func jsonValue(t *testing.T, s string) *apiextensionsv1.JSON {
	t.Helper()
	return &apiextensionsv1.JSON{Raw: []byte(s)}
}

func TestHelmReady(t *testing.T) {
	obj := func(kind string, gen, observed int64, status map[string]any) *unstructured.Unstructured {
		status["observedGeneration"] = observed
		status["conditions"] = []any{map[string]any{"type": "Ready", "status": "True"}}
		u := &unstructured.Unstructured{Object: map[string]any{"kind": kind, "status": status}}
		u.SetGeneration(gen)
		return u
	}
	oci := func(gen, observed int64, revision string) *unstructured.Unstructured {
		return obj("OCIRepository", gen, observed, map[string]any{"artifact": map[string]any{"revision": revision}})
	}
	hr := func(gen, observed int64, chart, status string) *unstructured.Unstructured {
		return obj("HelmRelease", gen, observed, map[string]any{"history": []any{map[string]any{"chartVersion": chart, "status": status}}})
	}
	current := oci(1, 1, "1.1@sha256:73accaef0604ffff")
	repo := obj("HelmRepository", 1, 1, map[string]any{})
	for name, c := range map[string]struct {
		source, hr *unstructured.Unstructured
		want       bool
	}{
		"deployed current OCI artifact": {current, hr(2, 2, "1.1+73accaef0604", "deployed"), true},
		"deployed from HTTP repository": {repo, hr(2, 2, "1.1", "deployed"), true},
		"old chart still deployed":      {current, hr(2, 2, "1.0+73accaef0604", "deployed"), false},
		"new chart failed":              {current, hr(2, 2, "1.1+73accaef0604", "failed"), false},
		"release spec not observed":     {current, hr(3, 2, "1.1+73accaef0604", "deployed"), false},
		"source spec not observed":      {oci(2, 1, "1.1@sha256:73accaef0604ffff"), hr(2, 2, "1.1+73accaef0604", "deployed"), false},
		"same version, other artifact":  {oci(2, 2, "1.1@sha256:0000aaaa1111ffff"), hr(2, 2, "1.1+73accaef0604", "deployed"), false},
	} {
		if got, _, _ := helmReady(c.source, c.hr, "1.1"); got != c.want {
			t.Errorf("%s: ready=%v", name, got)
		}
	}
}

func TestReadyConditionPerConditionGeneration(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"kind": "Certificate", "status": map[string]any{
		"conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(1)}},
	}}}
	u.SetGeneration(2)
	if ok, _, _ := readyCondition(u); ok {
		t.Error("stale per-condition generation counted as ready")
	}
	u.SetGeneration(1)
	if ok, _, _ := readyCondition(u); !ok {
		t.Error("current condition not ready")
	}
}
