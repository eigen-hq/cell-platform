package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// The rights of the operator and the API (E-70), pinned: what they may do
// across the cluster, where they hold Roles, and that what they hold in an
// app's namespace is bound only there.

const crud = "get,list,watch,create,update,patch,delete"

var clusterWide = map[string][]string{
	"cell-operator": {
		"cell.internal/apps:" + crud, "cell.internal/apps/status:" + crud, "cell.internal/apps/finalizers:" + crud,
		"cell.internal/backups:" + crud, "cell.internal/backups/status:" + crud,
		"cell.internal/restores:" + crud, "cell.internal/restores/status:" + crud,
		"cell.internal/changerequests:" + crud, "cell.internal/changerequests/status:" + crud,
		"cell.internal/tasks:get,list,watch,create,patch",
		"apps/deployments:get,list,watch", "batch/jobs:get,list,watch",
		"postgresql.cnpg.io/clusters:get,list,watch", "helm.toolkit.fluxcd.io/helmreleases:get,list,watch",
		"networking.k8s.io/ingresses:list",
		"/namespaces:get,create,patch,delete", "/nodes:get,list,watch",
		"rbac.authorization.k8s.io/rolebindings:create,patch",
		"rbac.authorization.k8s.io/clusterroles#admin:bind",
		"rbac.authorization.k8s.io/clusterroles#cell-operator-app:bind",
		"rbac.authorization.k8s.io/clusterroles#cell-api-app:bind",
	},
	"cell-api": {
		"cell.internal/apps:get,list,watch,create", "cell.internal/backups:get,list,watch,create",
		"cell.internal/restores:get,list,watch",
		"cell.internal/changerequests:get,list,watch,create,update",
		"cell.internal/tasks:get,list,watch,create", "cell.internal/tasks/status:update",
		"/namespaces:get", "/nodes:get,list",
	},
	// The kubelet's /metrics/* on every machine (E-87).
	"cell-metrics-collector": {"/nodes/metrics:get"},
}

// The namespaces an account holds a Role in.
var roleNamespaces = map[string][]string{
	"cell-operator": {"cell-system", "cert-manager", "kube-system"},
	"cell-api":      {"cell-system", "kube-system", "longhorn-system"},
}

// The ClusterRoles the operator binds in app namespaces only.
var appRoles = []string{"cell-operator-app", "cell-api-app"}

var kindLine = regexp.MustCompile(`(?m)^kind: (\S+)$`)

type manifests struct {
	clusterRoles map[string]rbacv1.ClusterRole
	roles        map[string]rbacv1.Role // namespace/name
	crbs         []rbacv1.ClusterRoleBinding
	rbs          []rbacv1.RoleBinding
}

func load(files ...string) (manifests, error) {
	m := manifests{clusterRoles: map[string]rbacv1.ClusterRole{}, roles: map[string]rbacv1.Role{}}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return m, err
		}
		for _, d := range strings.Split(string(raw), "\n---\n") {
			k := kindLine.FindStringSubmatch(d)
			if k == nil {
				continue
			}
			var into any
			switch k[1] {
			case "ClusterRole":
				into = &rbacv1.ClusterRole{}
			case "Role":
				into = &rbacv1.Role{}
			case "ClusterRoleBinding":
				into = &rbacv1.ClusterRoleBinding{}
			case "RoleBinding":
				into = &rbacv1.RoleBinding{}
			default:
				continue
			}
			if err := yaml.UnmarshalStrict([]byte(d), into); err != nil {
				return m, fmt.Errorf("%s: %w", f, err)
			}
			switch o := into.(type) {
			case *rbacv1.ClusterRole:
				m.clusterRoles[o.Name] = *o
			case *rbacv1.Role:
				m.roles[o.Namespace+"/"+o.Name] = *o
			case *rbacv1.ClusterRoleBinding:
				m.crbs = append(m.crbs, *o)
			case *rbacv1.RoleBinding:
				m.rbs = append(m.rbs, *o)
			}
		}
	}
	return m, nil
}

// account is the cell-system ServiceAccount a binding names, if it names one
// of the two.
func account(subjects []rbacv1.Subject) []string {
	var out []string
	for _, s := range subjects {
		if _, ok := clusterWide[s.Name]; ok && s.Kind == rbacv1.ServiceAccountKind && s.Namespace == "cell-system" {
			out = append(out, s.Name)
		}
	}
	return out
}

// grantsOf is one entry per verb: "group/resource[#name]:verb".
func grantsOf(rules []rbacv1.PolicyRule) []string {
	var out []string
	for _, r := range rules {
		for _, v := range r.Verbs {
			for _, g := range r.APIGroups {
				for _, res := range r.Resources {
					if len(r.ResourceNames) == 0 {
						out = append(out, g+"/"+res+":"+v)
					}
					for _, n := range r.ResourceNames {
						out = append(out, g+"/"+res+"#"+n+":"+v)
					}
				}
			}
			if len(r.NonResourceURLs) > 0 {
				out = append(out, "nonResourceURLs:"+v)
			}
		}
	}
	return out
}

// perVerb splits allowlist entries "what:verb,verb" into one per verb.
func perVerb(entries []string) []string {
	var out []string
	for _, e := range entries {
		what, verbs, _ := strings.Cut(e, ":")
		for _, v := range strings.Split(verbs, ",") {
			out = append(out, what+":"+v)
		}
	}
	return out
}

func wildcard(rules []rbacv1.PolicyRule) bool {
	for _, r := range rules {
		for _, l := range [][]string{r.APIGroups, r.Resources, r.Verbs, r.ResourceNames} {
			if slices.Contains(l, "*") {
				return true
			}
		}
	}
	return false
}

// findings lists every right of the two accounts the allowlist does not hold.
func findings(m manifests) []string {
	var out []string
	held := map[string][]string{}
	for _, b := range m.crbs {
		for _, a := range account(b.Subjects) {
			if slices.Contains(appRoles, b.RoleRef.Name) {
				out = append(out, fmt.Sprintf("%s holds %s across the cluster", a, b.RoleRef.Name))
			}
			r, ok := m.clusterRoles[b.RoleRef.Name]
			if !ok || b.RoleRef.Kind != "ClusterRole" {
				out = append(out, fmt.Sprintf("%s: ClusterRoleBinding %s names no ClusterRole of the manifests", a, b.Name))
				continue
			}
			held[a] = append(held[a], grantsOf(r.Rules)...)
		}
	}
	for a, entries := range clusterWide {
		allow := perVerb(entries)
		for _, g := range held[a] {
			if !slices.Contains(allow, g) {
				out = append(out, fmt.Sprintf("%s holds %s across the cluster", a, g))
			}
		}
	}
	for _, b := range m.rbs {
		for _, a := range account(b.Subjects) {
			if !slices.Contains(roleNamespaces[a], b.Namespace) {
				out = append(out, fmt.Sprintf("%s holds RoleBinding %s in %s", a, b.Name, b.Namespace))
			}
			if b.RoleRef.Kind != "Role" {
				out = append(out, fmt.Sprintf("%s: RoleBinding %s/%s binds %s %s", a, b.Namespace, b.Name, b.RoleRef.Kind, b.RoleRef.Name))
			} else if r, ok := m.roles[b.Namespace+"/"+b.RoleRef.Name]; !ok || wildcard(r.Rules) {
				out = append(out, fmt.Sprintf("%s: Role %s/%s is missing or grants *", a, b.Namespace, b.RoleRef.Name))
			}
		}
	}
	for _, name := range appRoles {
		r, ok := m.clusterRoles[name]
		if !ok {
			out = append(out, "ClusterRole "+name+" is missing")
			continue
		}
		for _, g := range grantsOf(r.Rules) {
			if wildcard(r.Rules) || strings.HasPrefix(g, "rbac.authorization.k8s.io/") || strings.HasPrefix(g, "nonResourceURLs") {
				out = append(out, fmt.Sprintf("%s grants %s", name, g))
			}
		}
	}
	return out
}

func TestRightsAllowlist(t *testing.T) {
	files, err := filepath.Glob("base/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := load(files...)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings(m) {
		t.Error(f)
	}
}

// The cluster-wide rights of v0.2.4 and v0.2.5 fail the allowlist.
func TestRightsAllowlistRejectsOldRights(t *testing.T) {
	m, err := load("../internal/controller/testdata/rbac-v0.2.5.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got := findings(m)
	for _, want := range []string{
		"cell-operator holds /secrets:get across the cluster",
		"cell-operator holds rbac.authorization.k8s.io/rolebindings:delete across the cluster",
		"cell-api holds /pods:list across the cluster",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("want the finding %q, got %q", want, got)
		}
	}
}
