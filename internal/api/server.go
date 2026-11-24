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

	"github.com/marcelheidebrecht/cell-platform/internal/authentik"
	"github.com/marcelheidebrecht/cell-platform/internal/cell"
)

// Roles, from identity groups.
const (
	RoleAdmin  = "admin"
	RoleAgent  = "agent"
	RoleMember = "member"

	GroupAdmins = "cell-admins"
	GroupAgents = cell.AgentGroup
)

var rank = map[string]int{RoleMember: 1, RoleAgent: 2, RoleAdmin: 3}

// Caller is the authenticated user of a request.
type Caller struct {
	Username string   `json:"username"`
	Role     string   `json:"role"`
	Groups   []string `json:"groups"`
}

// RoleOf maps identity groups to an API role: admins may do everything,
// agents read and propose changes, everyone else reads apps and changes.
func RoleOf(groups []string) string {
	switch {
	case slices.Contains(groups, GroupAdmins):
		return RoleAdmin
	case slices.Contains(groups, GroupAgents):
		return RoleAgent
	}
	return RoleMember
}

// Identity is what the API needs from the identity provider.
type Identity interface {
	ListUsers(ctx context.Context) ([]authentik.User, error)
	CreateUser(ctx context.Context, u authentik.User, password string) (authentik.User, error)
	CreateAppPassword(ctx context.Context, username string) (string, error)
	ListGroups(ctx context.Context) ([]authentik.Group, error)
	CreateGroup(ctx context.Context, name string) (authentik.Group, error)
	SetGroupMembers(ctx context.Context, group string, usernames []string) (authentik.Group, error)
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
	Now  func() time.Time
	Log  *slog.Logger
}

// route is one operation of the API; the tests check the table against
// the OpenAPI document.
type route struct {
	method, path string
	role         string // minimum role; "" means no authentication
	handle       func(w http.ResponseWriter, r *http.Request, c *Caller) error
}

func (s *Server) routes() []route {
	return []route{
		{"GET", "/healthz", "", s.healthz},
		{"GET", "/openapi.yaml", "", s.openapi},
		{"GET", "/v0/login", "", s.getLogin},
		{"GET", "/v0/whoami", RoleMember, s.whoami},
		{"GET", "/v0/catalog", RoleMember, s.listCatalog},
		{"GET", "/v0/catalog/{name}", RoleMember, s.getCatalogEntry},
		{"GET", "/v0/apps", RoleMember, s.listApps},
		{"POST", "/v0/apps", RoleAgent, s.installApp},
		{"GET", "/v0/apps/{name}", RoleMember, s.getApp},
		{"PATCH", "/v0/apps/{name}", RoleAgent, s.updateApp},
		{"DELETE", "/v0/apps/{name}", RoleAgent, s.removeApp},
		{"GET", "/v0/apps/{name}/backups", RoleMember, s.listBackups},
		{"POST", "/v0/apps/{name}/backups", RoleAdmin, s.createBackup},
		{"POST", "/v0/apps/{name}/restore", RoleAgent, s.restoreApp},
		{"GET", "/v0/apps/{name}/access", RoleMember, s.getAccess},
		{"PUT", "/v0/apps/{name}/access", RoleAgent, s.setAccess},
		{"GET", "/v0/users", RoleAgent, s.listUsers},
		{"POST", "/v0/users", RoleAdmin, s.createUser},
		{"POST", "/v0/users/{name}/tokens", RoleAdmin, s.createUserToken},
		{"GET", "/v0/groups", RoleAgent, s.listGroups},
		{"POST", "/v0/groups", RoleAdmin, s.createGroup},
		{"PUT", "/v0/groups/{name}/members", RoleAdmin, s.setGroupMembers},
		{"GET", "/v0/changes", RoleMember, s.listChanges},
		{"GET", "/v0/changes/{name}", RoleMember, s.getChange},
		{"POST", "/v0/changes/{name}/approve", RoleAdmin, s.approveChange},
		{"POST", "/v0/changes/{name}/reject", RoleAdmin, s.rejectChange},
		{"GET", "/v0/events", RoleMember, s.events},
		{"GET", "/v0/apps/{name}/pods", RoleAgent, s.listPods},
		{"GET", "/v0/apps/{name}/logs", RoleAgent, s.appLogs},
		{"GET", "/v0/apps/{name}/events", RoleAgent, s.appEvents},
		{"GET", "/v0/tasks", RoleAgent, s.listTasks},
		{"POST", "/v0/tasks", RoleAdmin, s.createTask},
		{"GET", "/v0/tasks/{name}", RoleAgent, s.getTask},
		{"POST", "/v0/tasks/{name}/claim", RoleAgent, s.claimTask},
		{"POST", "/v0/tasks/{name}/finish", RoleAgent, s.finishTask},
		{"GET", "/v0/nodes", RoleAgent, s.listNodes},
		{"POST", "/v0/nodes/joins", RoleAdmin, s.createJoin},
		{"GET", "/v0/nodes/join.sh", "", s.joinScript},
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
			if rank[c.Role] < rank[rt.role] {
				writeError(w, errorf(http.StatusForbidden, "needs role %s, you are %s", rt.role, c.Role))
				return
			}
			if c.Role == RoleAgent && !taskFree[rt.method+" "+rt.path] {
				ctx, err := s.inTask(r, c)
				if err != nil {
					s.fail(w, r, err)
					return
				}
				r = r.WithContext(ctx)
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

func (s *Server) whoami(w http.ResponseWriter, _ *http.Request, c *Caller) error {
	writeJSON(w, http.StatusOK, c)
	return nil
}
