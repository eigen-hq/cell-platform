package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	yamlv3 "go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
)

// oneYAMLDocument returns the single document of a YAML file as JSON. A
// repeated key or a second document is an error: either would drop part of
// what the person wrote without a word. The yaml.v3 decoder splits the
// stream, whatever its line endings; the document is then converted as
// Kubernetes converts YAML, so a date stays the text it was.
func oneYAMLDocument(file string) ([]byte, error) {
	dec := yamlv3.NewDecoder(strings.NewReader(file))
	var doc yamlv3.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return []byte("null"), nil
		}
		return nil, err
	}
	var more yamlv3.Node
	if err := dec.Decode(&more); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("one YAML document only")
	}
	one, err := yamlv3.Marshal(&doc)
	if err != nil {
		return nil, err
	}
	return yaml.YAMLToJSONStrict(one)
}

// --- catalog ---------------------------------------------------------------

func (s *Server) catalog() ([]cellv1.App, error) {
	files, err := fs.Glob(s.Catalog, "*/manifest.yaml")
	if err != nil {
		return nil, err
	}
	var apps []cellv1.App
	for _, f := range files {
		data, err := fs.ReadFile(s.Catalog, f)
		if err != nil {
			return nil, err
		}
		var app cellv1.App
		if err := yaml.UnmarshalStrict(data, &app); err != nil {
			return nil, err
		}
		app.Name = path.Dir(f)
		apps = append(apps, app)
	}
	return apps, nil
}

func (s *Server) catalogEntry(name string) (*cellv1.App, error) {
	apps, err := s.catalog()
	if err != nil {
		return nil, err
	}
	for i := range apps {
		if apps[i].Name == name {
			return &apps[i], nil
		}
	}
	return nil, errorf(http.StatusNotFound, "the catalog has no app %q", name)
}

type catalogEntry struct {
	Name      string          `json:"name"`
	Title     string          `json:"title"`
	Version   string          `json:"version"`
	Source    string          `json:"source"`
	Auth      string          `json:"auth"`
	Subdomain string          `json:"subdomain"`
	Manifest  json.RawMessage `json:"manifest,omitempty"`
}

func viewEntry(a *cellv1.App, full bool) (catalogEntry, error) {
	e := catalogEntry{Name: a.Name, Title: a.Spec.Title, Version: a.Spec.Version, Source: "image",
		Auth: a.Spec.Access.Auth.Mode, Subdomain: a.Spec.Access.Subdomain}
	if a.Spec.Source.Helm != nil {
		e.Source = "helm"
	}
	if full {
		raw, err := json.Marshal(a.Spec)
		if err != nil {
			return e, err
		}
		e.Manifest = raw
	}
	return e, nil
}

func (s *Server) listCatalog(w http.ResponseWriter, _ *http.Request, _ *Caller) error {
	apps, err := s.catalog()
	if err != nil {
		return err
	}
	out := []catalogEntry{}
	for i := range apps {
		e, err := viewEntry(&apps[i], false)
		if err != nil {
			return err
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) getCatalogEntry(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	app, err := s.catalogEntry(r.PathValue("name"))
	if err != nil {
		return err
	}
	e, err := viewEntry(app, true)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, e)
	return nil
}

// --- apps ------------------------------------------------------------------

type condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type appView struct {
	Name           string          `json:"name"`
	Title          string          `json:"title,omitempty"`
	Version        string          `json:"version"`
	RunningVersion string          `json:"runningVersion,omitempty"`
	Phase          string          `json:"phase"`
	URL            string          `json:"url,omitempty"`
	Groups         []string        `json:"groups"`
	LastBackup     *lastBackup     `json:"lastBackup"`
	Conditions     []condition     `json:"conditions"`
	Spec           json.RawMessage `json:"spec"`
}

type lastBackup struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

func viewApp(a *cellv1.App) (appView, error) {
	v := appView{Name: a.Name, Title: a.Spec.Title, Version: a.Spec.Version, RunningVersion: a.Status.Version,
		Phase: string(a.Status.Phase), URL: a.Status.URL, Groups: a.Spec.Access.Groups, Conditions: []condition{}}
	if v.Groups == nil {
		v.Groups = []string{}
	}
	for _, c := range a.Status.Conditions {
		v.Conditions = append(v.Conditions, condition{Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message})
	}
	if b := a.Status.LastBackup; b != nil {
		v.LastBackup = &lastBackup{Name: b.Name, At: b.At.Time}
	}
	raw, err := json.Marshal(a.Spec)
	v.Spec = raw
	return v, err
}

func (s *Server) app(ctx context.Context, name string) (*cellv1.App, error) {
	var a cellv1.App
	if err := s.Kube.Get(ctx, client.ObjectKey{Name: name}, &a); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, errorf(http.StatusNotFound, "no app %q", name)
		}
		return nil, err
	}
	return &a, nil
}

// Viewers see the state of the cell, not what it holds (E-63): a spec and a
// patch can carry configuration values, an agent's reason quotes logs as its
// evidence, and status messages can quote either or a container's output.
// Of the spec they keep how the app is reached; phases and reasons stay.
func forViewer(c *Caller) bool { return c.Role == RoleViewer }

func viewAppFor(c *Caller, a *cellv1.App) (appView, error) {
	v, err := viewApp(a)
	if err != nil || !forViewer(c) {
		return v, err
	}
	for i := range v.Conditions {
		v.Conditions[i].Message = ""
	}
	v.Spec, err = json.Marshal(map[string]any{"access": a.Spec.Access})
	return v, err
}

func viewChangeFor(c *Caller, cr *cellv1.ChangeRequest) (changeView, error) {
	v, err := viewChange(cr)
	if err != nil || !forViewer(c) {
		return v, err
	}
	v.Patch, v.Message = json.RawMessage("null"), ""
	if v.Requester.Kind == "Agent" {
		v.Reason = ""
	}
	return v, nil
}

func (s *Server) listApps(w http.ResponseWriter, r *http.Request, c *Caller) error {
	var list cellv1.AppList
	if err := s.Kube.List(r.Context(), &list); err != nil {
		return err
	}
	out := []appView{}
	for i := range list.Items {
		v, err := viewAppFor(c, &list.Items[i])
		if err != nil {
			return err
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) getApp(w http.ResponseWriter, r *http.Request, c *Caller) error {
	a, err := s.app(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	v, err := viewAppFor(c, a)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

// --- changes to apps -------------------------------------------------------

// record writes a ChangeRequest for the caller. It never carries a
// decision: an admin approves or rejects it in a call of its own (A-10).
func (s *Server) record(ctx context.Context, c *Caller, w http.ResponseWriter, spec cellv1.ChangeRequestSpec) error {
	spec.Requester = cellv1.Requester{Kind: "Human", Name: c.Username}
	if c.Role == RoleAgent {
		// An agent proposes changes only for the app its task is about.
		task := taskOf(ctx)
		if spec.App != task.App {
			return errorf(http.StatusForbidden, "task %s is about app %s, not %s", task.Name, task.App, spec.App)
		}
		spec.Requester.Kind = "Agent"
		spec.Task = task.Name
	}
	spec.Approval = cellv1.Approval{Required: true}
	if spec.Risk == "" {
		spec.Risk = "medium"
	}
	cr := &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{GenerateName: spec.App + "-" + spec.Action + "-"}, Spec: spec}
	if err := s.Kube.Create(ctx, cr); err != nil {
		if apierrors.IsInvalid(err) {
			return errorf(http.StatusBadRequest, "%v", err)
		}
		return err
	}
	v, err := viewChange(cr)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, v)
	return nil
}

// appName is what the App resource accepts as name.
var appName = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

type installRequest struct {
	Catalog string `json:"catalog"`
	// Manifest is an app spec from elsewhere (an agent packaged it).
	Manifest json.RawMessage `json:"manifest"`
	// ManifestYAML is a manifest file as people write it and as the
	// catalog keeps it: an App with apiVersion, kind, metadata and spec.
	ManifestYAML string   `json:"manifestYaml"`
	Name         string   `json:"name"`
	Subdomain    string   `json:"subdomain"`
	Groups       []string `json:"groups"`
	Reason       string   `json:"reason"`
}

func (s *Server) installApp(w http.ResponseWriter, r *http.Request, c *Caller) error {
	var req installRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if req.ManifestYAML != "" {
		if len(req.Manifest) > 0 {
			return errorf(http.StatusBadRequest, "give manifest or manifestYaml, not both")
		}
		// Every key of the file is known: a typo or a wrong indent beside
		// spec would otherwise be dropped. The status of an exported App
		// is allowed and ignored.
		var doc struct {
			APIVersion string            `json:"apiVersion"`
			Kind       string            `json:"kind"`
			Metadata   metav1.ObjectMeta `json:"metadata"`
			Spec       json.RawMessage   `json:"spec"`
			Status     json.RawMessage   `json:"status"`
		}
		js, err := oneYAMLDocument(req.ManifestYAML)
		if err != nil {
			return errorf(http.StatusBadRequest, "manifest: %v", err)
		}
		dec := json.NewDecoder(bytes.NewReader(js))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&doc); err != nil {
			return errorf(http.StatusBadRequest, "manifest: %v", err)
		}
		if doc.APIVersion != cellv1.GroupVersion.String() || doc.Kind != "App" || len(doc.Spec) == 0 || string(doc.Spec) == "null" {
			return errorf(http.StatusBadRequest, "manifest: an App with apiVersion %s, kind App and a spec, as in the catalog", cellv1.GroupVersion)
		}
		if req.Name == "" {
			req.Name = doc.Metadata.Name
		}
		req.Manifest = doc.Spec
	}
	var spec cellv1.AppSpec
	name, risk := req.Name, "low"
	switch {
	case req.Catalog != "" && len(req.Manifest) > 0:
		return errorf(http.StatusBadRequest, "give catalog or manifest, not both")
	case req.Catalog != "":
		entry, err := s.catalogEntry(req.Catalog)
		if err != nil {
			return err
		}
		spec = entry.Spec
		if name == "" {
			name = entry.Name
		}
		if req.Reason == "" {
			req.Reason = "install " + req.Catalog + " from the catalog"
		}
	case len(req.Manifest) > 0:
		dec := json.NewDecoder(bytes.NewReader(req.Manifest))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&spec); err != nil {
			return errorf(http.StatusBadRequest, "manifest: %v", err)
		}
		if name == "" {
			return errorf(http.StatusBadRequest, "name is required with a manifest")
		}
		// Nobody maintains this manifest: approvers must read it.
		risk = "high"
		if req.Reason == "" {
			req.Reason = "install " + name + " from a manifest that is not in the catalog"
		}
	default:
		return errorf(http.StatusBadRequest, "give catalog (an entry of GET /v0/catalog) or manifest")
	}
	if !appName.MatchString(name) || strings.HasPrefix(name, "cell-") {
		return errorf(http.StatusBadRequest, "app name %q: lowercase letters, digits and dashes, at most 40, starting with a letter, not with cell- (the platform's)", name)
	}
	if _, err := s.app(r.Context(), name); err == nil {
		return errorf(http.StatusConflict, "app %q is already installed", name)
	}
	if req.Subdomain != "" {
		spec.Access.Subdomain = req.Subdomain
	}
	if req.Groups != nil {
		spec.Access.Groups = req.Groups
	}
	if err := s.subdomainFree(r.Context(), spec.Access.Subdomain, name); err != nil {
		return err
	}
	// The cluster checks the app against its schema and rules without
	// creating it (the API's account may only do dry runs on apps), so a
	// wrong manifest is refused now, not after its approval.
	if err := s.Kube.Create(r.Context(), &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}, client.DryRunAll); err != nil {
		switch {
		case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
			return errorf(http.StatusBadRequest, "manifest: %v", err)
		case apierrors.IsAlreadyExists(err):
			return errorf(http.StatusConflict, "app %q is already installed", name)
		}
		return err
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	// Data an earlier installation of the name left behind goes to the new
	// app, whatever it is: approvers must see that.
	var ns corev1.Namespace
	switch err := s.Kube.Get(r.Context(), client.ObjectKey{Name: "app-" + name}, &ns); {
	case err == nil:
		risk = "high"
		req.Reason += "; reuses the data (volumes, database, secrets) an earlier installation left in namespace app-" + name
	case !apierrors.IsNotFound(err):
		return err
	}
	return s.record(r.Context(), c, w, cellv1.ChangeRequestSpec{App: name, Action: "install", Reason: req.Reason, Risk: risk,
		Patch: &apiextensionsv1.JSON{Raw: raw}})
}

// subdomainFree refuses a subdomain another app serves. The operator
// enforces the same for changes approved side by side.
func (s *Server) subdomainFree(ctx context.Context, subdomain, app string) error {
	var apps cellv1.AppList
	if err := s.Kube.List(ctx, &apps); err != nil {
		return err
	}
	for _, a := range apps.Items {
		if a.Name != app && a.Spec.Access.Subdomain == subdomain {
			return errorf(http.StatusConflict, "subdomain %s is taken by app %s", subdomain, a.Name)
		}
	}
	return nil
}

type updateRequest struct {
	Patch  json.RawMessage `json:"patch"`
	Reason string          `json:"reason"`
}

func (s *Server) updateApp(w http.ResponseWriter, r *http.Request, c *Caller) error {
	app, err := s.app(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	var req updateRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	spec, err := checkPatch(app, req.Patch)
	if err != nil {
		return err
	}
	if spec.Access.Subdomain != app.Spec.Access.Subdomain {
		if err := s.subdomainFree(r.Context(), spec.Access.Subdomain, app.Name); err != nil {
			return err
		}
	}
	return s.record(r.Context(), c, w, cellv1.ChangeRequestSpec{App: app.Name, Action: "update", Reason: req.Reason, Risk: patchRisk(req.Patch),
		Patch: &apiextensionsv1.JSON{Raw: req.Patch}})
}

// patchRisk rates an update by what it touches, so the requester cannot
// play it down: who may use the app is low, a new version medium, anything
// else (source, values, needs, subdomain, ...) high.
func patchRisk(patch json.RawMessage) string {
	var obj map[string]json.RawMessage
	if json.Unmarshal(patch, &obj) != nil || len(obj) != 1 {
		return "high"
	}
	if _, ok := obj["version"]; ok {
		return "medium"
	}
	var access map[string]json.RawMessage
	if json.Unmarshal(obj["access"], &access) == nil && len(access) == 1 && access["groups"] != nil {
		return "low"
	}
	return "high"
}

// checkPatch catches what is wrong with a patch before anyone approves it:
// not an object, or a field the manifest does not have. The API server
// validates the rest when the change runs.
func checkPatch(app *cellv1.App, patch json.RawMessage) (*cellv1.AppSpec, error) {
	var obj map[string]any
	if err := json.Unmarshal(patch, &obj); err != nil || obj == nil {
		return nil, errorf(http.StatusBadRequest, "patch must be a JSON object")
	}
	cur, err := json.Marshal(app.Spec)
	if err != nil {
		return nil, err
	}
	next, err := jsonpatch.MergePatch(cur, patch)
	if err != nil {
		return nil, errorf(http.StatusBadRequest, "patch: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(next))
	dec.DisallowUnknownFields()
	var spec cellv1.AppSpec
	if err := dec.Decode(&spec); err != nil {
		return nil, errorf(http.StatusBadRequest, "patched manifest: %v", err)
	}
	if len(spec.Access.Groups) > 0 && spec.Access.Auth.Mode == "none" {
		return nil, errorf(http.StatusBadRequest, "app %s has no login (auth mode none): access groups need one", app.Name)
	}
	return &spec, nil
}

func (s *Server) removeApp(w http.ResponseWriter, r *http.Request, c *Caller) error {
	app, err := s.app(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	q := r.URL.Query()
	deleteData := false
	if v := q.Get("deleteData"); v != "" {
		if deleteData, err = strconv.ParseBool(v); err != nil {
			return errorf(http.StatusBadRequest, "deleteData must be true or false")
		}
	}
	reason := q.Get("reason")
	if reason == "" {
		reason = "remove " + app.Name
	}
	return s.record(r.Context(), c, w, cellv1.ChangeRequestSpec{App: app.Name, Action: "remove", Reason: reason, Risk: "high", DeleteData: deleteData})
}

type restoreRequest struct {
	Backup string `json:"backup"`
	Reason string `json:"reason"`
}

func (s *Server) restoreApp(w http.ResponseWriter, r *http.Request, c *Caller) error {
	app, err := s.app(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	var req restoreRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	// The change names the backup, so approvers see what comes back.
	if req.Backup == "" || req.Backup == "latest" {
		backups, err := s.backupsOf(r.Context(), app.Name)
		if err != nil {
			return err
		}
		for _, b := range backups {
			if b.Phase == string(cellv1.RunCompleted) {
				req.Backup = b.Name
				break
			}
		}
		if req.Backup == "" || req.Backup == "latest" {
			return errorf(http.StatusBadRequest, "app %s has no completed backup in this cell; name one from the bucket", app.Name)
		}
	}
	if req.Reason == "" {
		req.Reason = "restore backup " + req.Backup
	}
	return s.record(r.Context(), c, w, cellv1.ChangeRequestSpec{App: app.Name, Action: "restore", Reason: req.Reason, Risk: "high", Backup: req.Backup})
}

// restartApp rolls out new pods of the app as a change: spec and data stay,
// so the change takes no backup and has nothing to roll back.
func (s *Server) restartApp(w http.ResponseWriter, r *http.Request, c *Caller) error {
	app, err := s.app(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if req.Reason == "" {
		req.Reason = "restart " + app.Name
	}
	return s.record(r.Context(), c, w, cellv1.ChangeRequestSpec{App: app.Name, Action: "restart", Reason: req.Reason, Risk: "low"})
}

type accessView struct {
	Groups []string `json:"groups"`
	Reason string   `json:"reason,omitempty"`
}

func (s *Server) getAccess(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	a, err := s.app(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	groups := a.Spec.Access.Groups
	if groups == nil {
		groups = []string{}
	}
	writeJSON(w, http.StatusOK, accessView{Groups: groups})
	return nil
}

func (s *Server) setAccess(w http.ResponseWriter, r *http.Request, c *Caller) error {
	app, err := s.app(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	var req struct {
		Groups *[]string `json:"groups"`
		Reason string    `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	// Only an explicit [] lifts the restriction, never a forgotten field.
	if req.Groups == nil {
		return errorf(http.StatusBadRequest, "groups is required ([] lets everyone who can log in use the app)")
	}
	patch, err := json.Marshal(map[string]any{"access": map[string]any{"groups": *req.Groups}})
	if err != nil {
		return err
	}
	if _, err := checkPatch(app, patch); err != nil {
		return err
	}
	if req.Reason == "" {
		req.Reason = "set who may use the app"
	}
	return s.record(r.Context(), c, w, cellv1.ChangeRequestSpec{App: app.Name, Action: "update", Reason: req.Reason, Risk: "low",
		Patch: &apiextensionsv1.JSON{Raw: patch}})
}

// --- backups ---------------------------------------------------------------

type backupView struct {
	Name        string     `json:"name"`
	App         string     `json:"app"`
	Reason      string     `json:"reason,omitempty"`
	Phase       string     `json:"phase"`
	Message     string     `json:"message,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt"`
}

func viewBackup(b *cellv1.Backup) backupView {
	return backupView{Name: b.Name, App: b.Spec.App, Reason: b.Spec.Reason, Phase: string(b.Status.Phase),
		Message: b.Status.Message, CreatedAt: b.CreationTimestamp.Time, CompletedAt: timePtr(b.Status.CompletedAt)}
}

// backupsOf returns the app's backups, newest first.
func (s *Server) backupsOf(ctx context.Context, app string) ([]backupView, error) {
	var list cellv1.BackupList
	if err := s.Kube.List(ctx, &list); err != nil {
		return nil, err
	}
	out := []backupView{}
	for i := range list.Items {
		if list.Items[i].Spec.App == app {
			out = append(out, viewBackup(&list.Items[i]))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request, c *Caller) error {
	app, err := s.app(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	out, err := s.backupsOf(r.Context(), app.Name)
	if err != nil {
		return err
	}
	if forViewer(c) {
		for i := range out {
			out[i].Message = ""
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// createBackup starts a backup at once: it changes nothing in the app, so
// it needs no approval, only an admin.
func (s *Server) createBackup(w http.ResponseWriter, r *http.Request, c *Caller) error {
	app, err := s.app(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	b := &cellv1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:   app.Name + "-" + s.now().UTC().Format("20060102-150405"),
			Labels: map[string]string{cellv1.LabelApp: app.Name},
		},
		Spec: cellv1.BackupSpec{App: app.Name, Reason: "manual, by " + c.Username},
	}
	if err := s.Kube.Create(r.Context(), b); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return errorf(http.StatusConflict, "a backup of %s started this second already", app.Name)
		}
		return err
	}
	writeJSON(w, http.StatusCreated, viewBackup(b))
	return nil
}

// --- change log ------------------------------------------------------------

type changeView struct {
	Name       string          `json:"name"`
	App        string          `json:"app"`
	Action     string          `json:"action"`
	Requester  requesterView   `json:"requester"`
	Reason     string          `json:"reason"`
	Risk       string          `json:"risk"`
	Patch      json.RawMessage `json:"patch"`
	Restores   string          `json:"restores,omitempty"`
	DeleteData bool            `json:"deleteData,omitempty"`
	Approval   approvalView    `json:"approval"`
	Phase      string          `json:"phase"`
	Message    string          `json:"message,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
	StartedAt  *time.Time      `json:"startedAt"`
	FinishedAt *time.Time      `json:"finishedAt"`
	Backup     string          `json:"backup,omitempty"`
	Restore    string          `json:"restore,omitempty"`
	Task       string          `json:"task,omitempty"`
}

type requesterView struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type approvalView struct {
	Required bool       `json:"required"`
	Decision string     `json:"decision,omitempty"`
	By       string     `json:"by,omitempty"`
	At       *time.Time `json:"at"`
}

func timePtr(t *metav1.Time) *time.Time {
	if t == nil {
		return nil
	}
	return &t.Time
}

func viewChange(cr *cellv1.ChangeRequest) (changeView, error) {
	v := changeView{
		Name: cr.Name, App: cr.Spec.App, Action: cr.Spec.Action, Reason: cr.Spec.Reason, Risk: cr.Spec.Risk,
		Requester: requesterView{Kind: cr.Spec.Requester.Kind, Name: cr.Spec.Requester.Name},
		Restores:  cr.Spec.Backup, DeleteData: cr.Spec.DeleteData, Patch: json.RawMessage("null"),
		Approval: approvalView{Required: cr.Spec.Approval.Required, Decision: cr.Spec.Approval.Decision,
			By: cr.Spec.Approval.By, At: timePtr(cr.Spec.Approval.At)},
		Phase: string(cr.Status.Phase), Message: cr.Status.Message, CreatedAt: cr.CreationTimestamp.Time,
		StartedAt: timePtr(cr.Status.StartedAt), FinishedAt: timePtr(cr.Status.FinishedAt),
		Backup: cr.Status.Backup, Restore: cr.Status.Restore, Task: cr.Spec.Task,
	}
	if v.Action == "" {
		v.Action = "update"
	}
	if cr.Spec.Patch != nil {
		v.Patch = cr.Spec.Patch.Raw
	}
	return v, nil
}

func (s *Server) listChanges(w http.ResponseWriter, r *http.Request, c *Caller) error {
	var list cellv1.ChangeRequestList
	if err := s.Kube.List(r.Context(), &list); err != nil {
		return err
	}
	app := r.URL.Query().Get("app")
	out := []changeView{}
	for i := range list.Items {
		if app != "" && list.Items[i].Spec.App != app {
			continue
		}
		v, err := viewChangeFor(c, &list.Items[i])
		if err != nil {
			return err
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) change(ctx context.Context, name string) (*cellv1.ChangeRequest, error) {
	var cr cellv1.ChangeRequest
	if err := s.Kube.Get(ctx, client.ObjectKey{Name: name}, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, errorf(http.StatusNotFound, "no change %q", name)
		}
		return nil, err
	}
	return &cr, nil
}

func (s *Server) getChange(w http.ResponseWriter, r *http.Request, c *Caller) error {
	cr, err := s.change(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	v, err := viewChangeFor(c, cr)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

// decide records an admin's decision on a change, once. The operator writes
// the change too (finalizer), so a conflict is retried on a fresh copy.
func (s *Server) decide(w http.ResponseWriter, r *http.Request, c *Caller, decision string) error {
	var cr *cellv1.ChangeRequest
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var err error
		if cr, err = s.change(r.Context(), r.PathValue("name")); err != nil {
			return err
		}
		switch {
		case cr.Spec.Approval.Decision != "":
			return errorf(http.StatusConflict, "change %s was already %s by %s", cr.Name, cr.Spec.Approval.Decision, cr.Spec.Approval.By)
		case cr.Status.Phase == cellv1.RunFailed || cr.Status.Phase == cellv1.RunSucceeded:
			return errorf(http.StatusConflict, "change %s already ended: %s", cr.Name, cr.Status.Message)
		}
		cr.Spec.Approval.Decision, cr.Spec.Approval.By, cr.Spec.Approval.At = decision, c.Username, &metav1.Time{Time: s.now()}
		return s.Kube.Update(r.Context(), cr)
	})
	if err != nil {
		if apierrors.IsConflict(err) {
			return errorf(http.StatusConflict, "change %s keeps changing, try again", r.PathValue("name"))
		}
		return err
	}
	v, err := viewChange(cr)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

func (s *Server) approveChange(w http.ResponseWriter, r *http.Request, c *Caller) error {
	return s.decide(w, r, c, "Approved")
}

func (s *Server) rejectChange(w http.ResponseWriter, r *http.Request, c *Caller) error {
	return s.decide(w, r, c, "Rejected")
}
