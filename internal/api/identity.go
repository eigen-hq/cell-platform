package api

import (
	"errors"
	"net/http"
	"sort"

	"github.com/marcelheidebrecht/cell-platform/internal/authentik"
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

func (s *Server) setGroupMembers(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	var req struct {
		Users *[]string `json:"users"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	// Only an explicit [] empties a group, never a forgotten field.
	if req.Users == nil {
		return errorf(http.StatusBadRequest, "users is required ([] removes every member)")
	}
	g, err := s.Identity.SetGroupMembers(r.Context(), r.PathValue("name"), *req.Users)
	if err != nil {
		return identityError(err)
	}
	writeJSON(w, http.StatusOK, viewGroup(g))
	return nil
}
