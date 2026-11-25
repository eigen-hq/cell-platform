// The platform API server.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/marcelheidebrecht/cell-platform/api"
	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
	"github.com/marcelheidebrecht/cell-platform/catalog"
	"github.com/marcelheidebrecht/cell-platform/deploy"
	cellapi "github.com/marcelheidebrecht/cell-platform/internal/api"
	"github.com/marcelheidebrecht/cell-platform/internal/authentik"
	"github.com/marcelheidebrecht/cell-platform/internal/cell"
)

// The identity's OAuth2 clients (and their slugs): cell-api for logins with
// app passwords (CLI, agents), cell-console for the web console.
const (
	apiClient     = "cell-api"
	consoleClient = "cell-console"
	meshClient    = "cell-mesh"
	meshGroup     = "cell-mesh"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("api stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := cellv1.AddToScheme(scheme); err != nil {
		return err
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	kube, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	token := os.Getenv("AUTHENTIK_TOKEN")
	if token == "" {
		return fmt.Errorf("AUTHENTIK_TOKEN is not set")
	}
	domain := os.Getenv("CELL_DOMAIN")
	if domain == "" {
		return fmt.Errorf("CELL_DOMAIN is not set")
	}
	cellCfg := cell.Config{Domain: domain}
	authOrigin := "https://" + cellCfg.Host("auth")
	consoleURL := "https://" + cellCfg.Host("console") + "/"
	authURL := env("AUTHENTIK_URL", "http://authentik-server.cell-system.svc.cluster.local")
	hc := &http.Client{Timeout: 20 * time.Second}
	identity := &authentik.Client{BaseURL: authURL, Token: token, HTTP: hc}

	// The identity side of the API: its login client and the role groups.
	if err := retry(ctx, log, "identity setup", func() error {
		if err := identity.EnsureAPIClient(ctx, apiClient, "Platform API"); err != nil {
			return err
		}
		if err := identity.EnsureOIDC(ctx, authentik.OIDC{Slug: consoleClient, Title: "Console", LaunchURL: consoleURL,
			ClientID: consoleClient, RedirectURIs: []string{consoleURL}, Public: true}); err != nil {
			return err
		}
		if err := identity.EnsureGroup(ctx, cellapi.GroupAdmins, "akadmin"); err != nil {
			return err
		}
		if err := identity.EnsureGroup(ctx, cellapi.GroupAgents); err != nil {
			return err
		}
		if err := identity.EnsureGroup(ctx, cellapi.GroupViewers); err != nil {
			return err
		}
		// The console is for the people who run the cell: admins change it,
		// viewers read it.
		if err := identity.EnsureAccess(ctx, consoleClient, []string{cellapi.GroupAdmins, cellapi.GroupViewers}); err != nil {
			return err
		}
		// The mesh (E-56): members of cell-mesh and the admins join it with
		// their cell login.
		if secret := os.Getenv("MESH_CLIENT_SECRET"); secret != "" {
			meshURL := "https://" + cellCfg.Host("mesh")
			if err := identity.EnsureOIDC(ctx, authentik.OIDC{Slug: meshClient, Title: "Mesh", LaunchURL: meshURL,
				ClientID: meshClient, ClientSecret: secret, RedirectURIs: []string{meshURL + "/oidc/callback"}}); err != nil {
				return err
			}
			if err := identity.EnsureGroup(ctx, meshGroup); err != nil {
				return err
			}
			return identity.EnsureAccess(ctx, meshClient, []string{cellapi.GroupAdmins, meshGroup})
		}
		return nil
	}); err != nil {
		return err
	}
	tokens := &cellapi.Tokens{UserinfoURL: authURL + "/application/o/userinfo/", ClientIDs: []string{apiClient, consoleClient}, HTTP: hc, TTL: 30 * time.Second}
	login := &cellapi.Login{
		Issuer:                authOrigin + "/application/o/" + consoleClient + "/",
		AuthorizationEndpoint: authOrigin + "/application/o/authorize/",
		TokenEndpoint:         authOrigin + "/application/o/token/",
		EndSessionEndpoint:    authOrigin + "/application/o/" + consoleClient + "/end-session/",
		ClientID:              consoleClient,
		RedirectURI:           consoleURL,
		Scope:                 "openid profile email",
	}
	var console *cellapi.Console
	if dir := os.Getenv("CONSOLE_DIR"); dir != "" {
		files := os.DirFS(dir)
		if _, err := fs.Stat(files, "index.html"); err != nil {
			return fmt.Errorf("console: %w", err)
		}
		console = &cellapi.Console{Host: cellCfg.Host("console"), Files: files, AuthOrigin: authOrigin}
	}

	// More machines join on the server's address (E-44); a local cell has none.
	var join *cellapi.Join
	if server := os.Getenv("CELL_JOIN_SERVER"); server != "" {
		ca := cfg.CAData
		if len(ca) == 0 {
			if ca, err = os.ReadFile(cfg.CAFile); err != nil {
				return fmt.Errorf("cluster CA: %w", err)
			}
		}
		sum := sha256.Sum256(ca)
		join = &cellapi.Join{Server: server, APIHost: cellCfg.Host("api"), CAHash: hex.EncodeToString(sum[:]), Script: deploy.NodeScript}
	}

	srv := &http.Server{
		Addr: ":8080",
		Handler: (&cellapi.Server{
			Kube: kube, Pods: clientset.CoreV1(), Identity: identity, Authenticate: tokens.Check,
			Catalog: catalog.FS, OpenAPI: api.OpenAPI, Login: login, Console: console, Join: join, Log: log,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("api listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// retry runs f until it succeeds or the context ends (the identity may
// still be starting).
func retry(ctx context.Context, log *slog.Logger, what string, f func() error) error {
	for {
		err := f()
		if err == nil {
			return nil
		}
		log.Info("waiting", "for", what, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
