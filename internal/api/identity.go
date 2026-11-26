package api

import (
	"errors"
	"net/http"
	"slices"
	"sort"

	"github.com/eigen-hq/cell-platform/internal/authentik"
)

// Users and groups live in the identity; the API passes them through. An
// identity change is not an app change and has no ChangeRequest; authentik
// keeps its own event log.

func identityError(err error) error {
	var ak *authentik.Error
	if errors.As(err, &ak) && ak.Status < 500 {
		status := ak.Status
		if status == http.StatusBadRequest && ak.Duplicate() {
			status = http.StatusConflict
		}
		return errorf(status, "identity: %s", ak.Body)
	}
	if errors.Is(err, authentik.ErrNotFound) {
		return errorf(http.StatusNotFound, "%v", err)
	}
	if errors.Is(err, authentik.ErrSuperuser) {
		return errorf(http.StatusForbidden, "%v", err)
	}
	return err
}

type userView struct {
	Username string   `json:"username"`
	Name     string   `json:"name"`
	Email    string   `json:"email,omitempty"`
	Groups   []string `json:"groups"`
}

func viewUser(u authentik.User) userView {
	groups := u.Groups
	if groups == nil {
		groups = []string{}
	}
	sort.Strings(groups)
	return userView{Username: u.Username, Name: u.Name, Email: u.Email, Groups: groups}
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	users, err := s.Identity.ListUsers(r.Context())
	if err != nil {
		return identityError(err)
	}
	out := []userView{}
	for _, u := range users {
		out = append(out, viewUser(u))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	writeJSON(w, http.StatusOK, out)
	return nil
}

type userRequest struct {
	Username string   `json:"username"`
	Name     string   `json:"name"`
	Email    string   `json:"email"`
	Password string   `json:"password"`
	Groups   []string `json:"groups"`
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	var req userRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if req.Username == "" || req.Name == "" {
		return errorf(http.StatusBadRequest, "username and name are required")
	}
	u, err := s.Identity.CreateUser(r.Context(), authentik.User{Username: req.Username, Name: req.Name, Email: req.Email, Groups: req.Groups}, req.Password)
	if err != nil {
		return identityError(err)
	}
	writeJSON(w, http.StatusCreated, viewUser(u))
	return nil
}

func (s *Server) createUserToken(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	token, err := s.Identity.CreateAppPassword(r.Context(), r.PathValue("name"))
	if err != nil {
		return identityError(err)
	}
	writeJSON(w, http.StatusCreated, map[string]string{"token": token})
	return nil
}

type groupView struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

func viewGroup(g authentik.Group) groupView {
	members := g.Members
	if members == nil {
		members = []string{}
	}
	sort.Strings(members)
	return groupView{Name: g.Name, Members: members}
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	groups, err := s.Identity.ListGroups(r.Context())
	if err != nil {
		return identityError(err)
	}
	out := []groupView{}
	for _, g := range groups {
		out = append(out, viewGroup(g))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if req.Name == "" {
		return errorf(http.StatusBadRequest, "name is required")
	}
	g, err := s.Identity.CreateGroup(r.Context(), req.Name)
	if err != nil {
		return identityError(err)
	}
	writeJSON(w, http.StatusCreated, viewGroup(g))
	return nil
}

// changeGroupMembers adds and removes single members. It never sends the
// whole list, so two admins editing one group cannot undo each other.
func (s *Server) changeGroupMembers(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	var req struct {
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if len(req.Add)+len(req.Remove) == 0 {
		return errorf(http.StatusBadRequest, "add or remove someone")
	}
	for _, u := range req.Add {
		if slices.Contains(req.Remove, u) {
			return errorf(http.StatusBadRequest, "%s is both added and removed", u)
		}
	}
	g, err := s.Identity.ChangeGroupMembers(r.Context(), r.PathValue("name"), req.Add, req.Remove)
	if err != nil {
		return identityError(err)
	}
	writeJSON(w, http.StatusOK, viewGroup(g))
	return nil
}
