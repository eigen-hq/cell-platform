package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Every third-party image the platform deploys is pinned by digest (E-52,
// E-76). Our own image (OPERATOR_IMAGE, BRIDGE_IMAGE) is built on the server
// and has no digest.

var (
	pinnedRe = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	sumRe    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// An image field, or the value that follows an env var named *_IMAGE.
	imageRe = regexp.MustCompile(`(?m)^\s*(?:-\s+)?image(?:Name)?:[ \t]+"?([^"\s#]+)|^\s*-?\s*name:\s+[A-Z_]*_IMAGE[ \t]*\n\s*value:[ \t]+"?([^"\s#]+)`)
	varRe   = regexp.MustCompile(`\$\{([A-Z_]+)\}`)
	fromRe  = regexp.MustCompile(`(?m)^FROM\s+(\S+)`)
	ours    = "registry.cell.internal/cell-operator:test"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func versionEnv(t *testing.T) map[string]string {
	t.Helper()
	env := map[string]string{"OPERATOR_IMAGE": ours, "BRIDGE_IMAGE": ours}
	for _, l := range strings.Split(read(t, "versions.env"), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && !strings.HasPrefix(l, "#") {
			env[k] = v
		}
	}
	return env
}

// lock maps repository:tag to its digest.
func lock(t *testing.T) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, l := range strings.Split(read(t, "images.lock"), "\n") {
		if l = strings.TrimSpace(l); l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if !pinnedRe.MatchString(l) {
			t.Errorf("images.lock: %q has no sha256 digest", l)
			continue
		}
		ref, sum, _ := strings.Cut(l, "@")
		m[ref] = sum
	}
	return m
}

func TestBaseImagesAreLocked(t *testing.T) {
	env, locked := versionEnv(t), lock(t)
	if !sumRe.MatchString(env["AUTHENTIK_DIGEST"]) {
		t.Errorf("versions.env: AUTHENTIK_DIGEST is not a sha256 digest")
	}
	files, err := filepath.Glob("base/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no manifests in base/: %v", err)
	}
	n := 0
	for _, f := range files {
		text := varRe.ReplaceAllStringFunc(read(t, f), func(v string) string { return env[v[2:len(v)-1]] })
		for _, m := range imageRe.FindAllStringSubmatch(text, -1) {
			ref := m[1] + m[2]
			n++
			if ref == ours {
				continue
			}
			if _, ok := locked[ref]; !ok {
				t.Errorf("%s: image %s is not in images.lock (add it with its digest)", f, ref)
			}
		}
	}
	if n < 8 {
		t.Errorf("found only %d images in base/: the scan no longer matches the manifests", n)
	}
}

func TestDockerfileBasesArePinned(t *testing.T) {
	froms := fromRe.FindAllStringSubmatch(read(t, "../Dockerfile"), -1)
	if len(froms) == 0 {
		t.Fatal("no FROM lines in Dockerfile")
	}
	for _, m := range froms {
		if !pinnedRe.MatchString(m[1]) {
			t.Errorf("Dockerfile: base image %s has no digest", m[1])
		}
	}
}

func TestCatalogImagesArePinned(t *testing.T) {
	files, _ := filepath.Glob("../catalog/*/manifest.yaml")
	if len(files) == 0 {
		t.Fatal("no catalog manifests found")
	}
	for _, f := range files {
		var m struct {
			Spec struct {
				Source struct {
					Image *struct{ Repository, Tag string }
					Helm  *struct{ Values map[string]any }
				}
			}
		}
		if err := yaml.Unmarshal([]byte(read(t, f)), &m); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		src := m.Spec.Source
		switch {
		case src.Image != nil:
			if !pinnedRe.MatchString(src.Image.Tag) {
				t.Errorf("%s: image %s:%s has no digest in its tag", f, src.Image.Repository, src.Image.Tag)
			}
		case src.Helm != nil:
			// Charts take the digest as image.digest; a chart without that
			// value needs its own check here.
			img, _ := src.Helm.Values["image"].(map[string]any)
			if d, _ := img["digest"].(string); !sumRe.MatchString(d) {
				t.Errorf("%s: chart values set no image.digest", f)
			}
		default:
			t.Errorf("%s: neither an image nor a chart source", f)
		}
	}
}
