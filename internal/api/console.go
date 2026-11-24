package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Login tells a client where and how its users log in: an OpenID Connect
// authorization code login with PKCE at the cell's identity. Every
// first-party client (the console today, native apps later) logs in the
// same way with its own client ID.
type Login struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorizationEndpoint"`
	TokenEndpoint         string `json:"tokenEndpoint"`
	EndSessionEndpoint    string `json:"endSessionEndpoint"`
	ClientID              string `json:"clientId"`
	RedirectURI           string `json:"redirectUri"`
	Scope                 string `json:"scope"`
}

// Console is the web console. The API serves it on its own host, so the
// console calls the API on the same origin.
type Console struct {
	Host  string
	Files fs.FS // the built console: index.html and assets/
	// AuthOrigin is the identity's origin; the console exchanges its login
	// code there.
	AuthOrigin string
}

func (s *Server) getLogin(w http.ResponseWriter, _ *http.Request, _ *Caller) error {
	if s.Login == nil {
		return errorf(http.StatusNotFound, "no login configured")
	}
	writeJSON(w, http.StatusOK, s.Login)
	return nil
}

// onConsole reports whether a request is for the console's files.
func (s *Server) onConsole(r *http.Request) bool {
	host, _, _ := strings.Cut(r.Host, ":")
	return s.Console != nil && host == s.Console.Host && !strings.HasPrefix(r.URL.Path, "/v0/")
}

// serveConsole serves a file of the console, or index.html for any other
// path (the console routes in the browser).
func (s *Server) serveConsole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, errorf(http.StatusMethodNotAllowed, "the console only serves files"))
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; "+
		"connect-src 'self' "+s.Console.AuthOrigin+"; manifest-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if st, err := fs.Stat(s.Console.Files, name); name == "" || err != nil || st.IsDir() {
		if strings.HasPrefix(name, "assets/") {
			// A tab from before a deploy asks for an old file: no page instead.
			http.NotFound(w, r)
			return
		}
		name = "index.html"
	}
	if strings.HasPrefix(name, "assets/") {
		h.Set("Cache-Control", "public, max-age=31536000, immutable") // file names carry a content hash
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, s.Console.Files, name)
}
