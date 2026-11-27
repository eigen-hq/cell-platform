// Package api is the platform API: the only door into a cell for the UI,
// the CLI and agents. It is a thin layer over the cell's resources and its
// identity. Everything that changes an app becomes a ChangeRequest, which
// an admin approves in a call of its own. The contract is api/openapi.yaml.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/eigen-hq/cell-platform/internal/authentik"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// Roles, from identity groups. An account in none of the groups logs in
// but has no role: the identity also holds everyone who only uses apps.
const (
	RoleAdmin  = "admin"
	RoleAgent  = "agent"
	RoleViewer = "viewer"
	RoleNone   = "none"

	GroupAdmins  = cell.AdminGroup
	GroupViewers = "cell-viewers"
	GroupAgents  = cell.AgentGroup
)

var rank = map[string]int{RoleNone: 0, RoleViewer: 1, RoleAgent: 2, RoleAdmin: 3}

// Caller is the authenticated user of a request.
type Caller struct {
	Username string   `json:"username"`
	Role     string   `json:"role"`
	Groups   []string `json:"groups"`
}

// RoleOf maps identity groups to an API role: admins may do everything,
// agents read and propose changes, viewers read, everyone else nothing.
// A person among the viewers stays one also in cell-agents: agents are
// the platform's own accounts, and a viewer reads outside of tasks.
func RoleOf(groups []string) string {
	switch {
	case slices.Contains(groups, GroupAdmins):
		return RoleAdmin
	case slices.Contains(groups, GroupViewers):
		return RoleViewer
	case slices.Contains(groups, GroupAgents):
		return RoleAgent
	}
	return RoleNone
}

// Identity is what the API needs from the identity provider.
type Identity interface {
	ListUsers(ctx context.Context) ([]authentik.User, error)
	CreateUser(ctx context.Context, u authentik.User, password string) (authentik.User, error)
	RemoveUser(ctx context.Context, username string) error
	CreateAppPassword(ctx context.Context, username string) (string, error)
	ListGroups(ctx context.Context) ([]authentik.Group, error)
	CreateGroup(ctx context.Context, name string) (authentik.Group, error)
	ChangeGroupMembers(ctx context.Context, group string, add, remove []string) (authentik.Group, error)
	GetMail(ctx context.Context) (*authentik.Mail, error)
	InviteUser(ctx context.Context, username string, valid time.Duration) (authentik.Invite, error)
}

type Server struct {
	Kube client.WithWatch
	// Pods reads container logs (the controller-runtime client cannot).
	Pods     corev1client.PodsGetter
	Identity Identity
	// Authenticate turns a bearer token into a caller.
	Authenticate func(ctx context.Context, token string) (*Caller, error)
	// Catalog holds <app>/manifest.yaml files.
	Catalog fs.FS
	OpenAPI []byte
	// Login describes the identity login of first-party clients
	// (GET /v0/login); Console, if set, is served on its own host.
	Login   *Login
	Console *Console
	// Join, if set, lets more machines join the cell; a local cell has none.
	Join *Join
	// CheckMail checks a mail server before it is taken; nil means a real
	// SMTP check.
	CheckMail func(ctx context.Context, m authentik.Mail) error
	// CheckDelivery checks that the cell's own mail server can reach the
	// mail servers of the given domains; nil means a real connection.
	CheckDelivery func(ctx context.Context, domains []string) error
	// DNS looks up the own mail server's records; nil means the system's.
	DNS Resolver
	// CheckModels checks a model provider's key before it is taken; nil
	// means a real request to the provider.
	CheckModels func(ctx context.Context, p cell.ModelProvider) error
	// ModelsMetrics is the model gateway's metrics address; empty means the
	// one in the cell.
	ModelsMetrics string
	// Metrics is the metrics store's address; empty means the one in the
	// cell.
	Metrics string
	Now     func() time.Time
	Log     *slog.Logger
}

// route is one operation of the API; the tests check the table against
// the OpenAPI document.
type route struct {
	method, path string
	role         string // minimum role; "" means no authentication
	handle       func(w http.ResponseWriter, r *http.Request, c *Caller) error
}

// Viewers read the state of the cell only (E-63): apps, changes, backups,
// machines and the backup target. Tasks, pods, events and logs are for
// agents and admins, people and groups for admins; viewerRoutes in the tests
// pins that list.
func (s *Server) routes() []route {
	return []route{
		{"GET", "/healthz", "", s.healthz},
		{"GET", "/openapi.yaml", "", s.openapi},
		{"GET", "/v0/login", "", s.getLogin},
		{"GET", "/v0/whoami", RoleNone, s.whoami},
		{"GET", "/v0/catalog", RoleViewer, s.listCatalog},
		{"GET", "/v0/catalog/{name}", RoleViewer, s.getCatalogEntry},
		{"GET", "/v0/apps", RoleViewer, s.listApps},
		{"POST", "/v0/apps", RoleAgent, s.installApp},
		{"GET", "/v0/apps/{name}", RoleViewer, s.getApp},
		{"PATCH", "/v0/apps/{name}", RoleAgent, s.updateApp},
		{"DELETE", "/v0/apps/{name}", RoleAgent, s.removeApp},
		{"GET", "/v0/apps/{name}/backups", RoleViewer, s.listBackups},
		{"POST", "/v0/apps/{name}/backups", RoleAdmin, s.createBackup},
		{"POST", "/v0/apps/{name}/restore", RoleAgent, s.restoreApp},
		{"POST", "/v0/apps/{name}/restart", RoleAgent, s.restartApp},
		{"GET", "/v0/apps/{name}/access", RoleViewer, s.getAccess},
		{"PUT", "/v0/apps/{name}/access", RoleAdmin, s.setAccess},
		{"GET", "/v0/users", RoleAdmin, s.listUsers},
		{"POST", "/v0/users", RoleAdmin, s.createUser},
		{"DELETE", "/v0/users/{name}", RoleAdmin, s.removeUser},
		{"POST", "/v0/users/{name}/tokens", RoleAdmin, s.createUserToken},
		{"POST", "/v0/users/{name}/invite", RoleAdmin, s.inviteUser},
		{"GET", "/v0/mail", RoleAdmin, s.getMail},
		{"PUT", "/v0/mail", RoleAdmin, s.setMail},
		{"GET", "/v0/models", RoleAgent, s.getModels},
		{"PUT", "/v0/models", RoleAdmin, s.setModels},
		{"GET", "/v0/models/usage", RoleAdmin, s.modelsUsage},
		{"GET", "/v0/groups", RoleAdmin, s.listGroups},
		{"POST", "/v0/groups", RoleAdmin, s.createGroup},
		{"PATCH", "/v0/groups/{name}/members", RoleAdmin, s.changeGroupMembers},
		{"GET", "/v0/changes", RoleViewer, s.listChanges},
		{"GET", "/v0/changes/{name}", RoleViewer, s.getChange},
		{"POST", "/v0/changes/{name}/approve", RoleAdmin, s.approveChange},
		{"POST", "/v0/changes/{name}/reject", RoleAdmin, s.rejectChange},
		{"GET", "/v0/events", RoleViewer, s.events},
		{"GET", "/v0/apps/{name}/pods", RoleAgent, s.listPods},
		{"GET", "/v0/apps/{name}/logs", RoleAgent, s.appLogs},
		{"GET", "/v0/apps/{name}/events", RoleAgent, s.appEvents},
		{"GET", "/v0/apps/{name}/usage", RoleAgent, s.appUsage},
		{"GET", "/v0/tasks", RoleAgent, s.listTasks},
		{"POST", "/v0/tasks", RoleAdmin, s.createTask},
		{"GET", "/v0/tasks/{name}", RoleAgent, s.getTask},
		{"POST", "/v0/tasks/{name}/claim", RoleAgent, s.claimTask},
		{"POST", "/v0/tasks/{name}/finish", RoleAgent, s.finishTask},
		{"GET", "/v0/nodes", RoleViewer, s.listNodes},
		{"GET", "/v0/nodes/{name}/usage", RoleViewer, s.nodeHistory},
		{"POST", "/v0/nodes/joins", RoleAdmin, s.createJoin},
		{"GET", "/v0/nodes/join.sh", "", s.joinScript},
		{"GET", "/v0/backup-target", RoleViewer, s.getBackupTarget},
		{"PUT", "/v0/backup-target", RoleAdmin, s.setBackupTarget},
	}
}

// Handler serves the API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		mux.HandleFunc(rt.method+" "+rt.path, s.wrap(rt))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if s.onConsole(r) {
			s.serveConsole(w, r)
			return
		}
		writeError(w, errorf(http.StatusNotFound, "no such endpoint: %s %s", r.Method, r.URL.Path))
	})
	return mux
}

func (s *Server) wrap(rt route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var c *Caller
		if rt.role != "" {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || token == "" {
				writeError(w, errorf(http.StatusUnauthorized, "missing bearer token"))
				return
			}
			var err error
			if c, err = s.Authenticate(r.Context(), token); err != nil {
				writeError(w, errorf(http.StatusUnauthorized, "invalid token: %v", err))
				return
			}
			if got, ok := rank[c.Role]; !ok || got < rank[rt.role] {
				if c.Role == RoleNone {
					writeError(w, errorf(http.StatusForbidden, "%s is in none of the groups %s, %s: ask an admin for access", c.Username, GroupViewers, GroupAdmins))
					return
				}
				writeError(w, errorf(http.StatusForbidden, "needs role %s, you are %s", rt.role, c.Role))
				return
			}
			if c.Role == RoleAgent {
				ctx, err := s.withAgent(r.Context(), c)
				if err != nil {
					s.fail(w, r, err)
					return
				}
				r = r.WithContext(ctx)
			}
			if c.Role == RoleAgent && !taskFree[rt.method+" "+rt.path] {
				ctx, err := s.inTask(r, c)
				if err != nil {
					s.fail(w, r, err)
					return
				}
				r = r.WithContext(ctx)
				// Checked on every call, after it is recorded: an agent whose
				// spec an admin narrows stops at its next call. It reads and
				// changes only the app its task is about: what one app's logs
				// say must not end up in another's.
				a, task := agentOf(ctx), taskOf(ctx)
				err = mayTake(a, task.Kind, task.App)
				if err == nil {
					err = mayCall(a, rt.method, rt.path)
				}
				if err == nil && strings.HasPrefix(rt.path, "/v0/apps/{name}") && r.PathValue("name") != task.App {
					err = errorf(http.StatusForbidden, "task %s is about app %s, not %s", task.Name, task.App, r.PathValue("name"))
				}
				if err != nil {
					s.fail(w, r, err)
					return
				}
			}
		}
		if err := rt.handle(w, r, c); err != nil {
			s.fail(w, r, err)
		}
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		s.log().Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		apiErr = errorf(http.StatusInternalServerError, "%v", err)
	}
	writeError(w, apiErr)
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Error is an API error with its HTTP status.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func errorf(status int, format string, args ...any) *Error {
	return &Error{Status: status, Msg: fmt.Sprintf(format, args...)}
}

func writeError(w http.ResponseWriter, e *Error) {
	writeJSON(w, e.Status, map[string]string{"error": e.Msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// readJSON decodes a request body strictly: unknown fields are an error, so
// nobody can slip an approval into a request.
func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errorf(http.StatusBadRequest, "request body: %v", err)
	}
	return nil
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request, _ *Caller) error {
	w.Header().Set("Content-Type", "text/plain")
	_, err := io.WriteString(w, "ok\n")
	return err
}

func (s *Server) openapi(w http.ResponseWriter, _ *http.Request, _ *Caller) error {
	w.Header().Set("Content-Type", "application/yaml")
	_, err := w.Write(s.OpenAPI)
	return err
}
