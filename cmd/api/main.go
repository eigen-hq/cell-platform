// The platform API server.
package main

import (
	"context"
	"errors"
	"fmt"
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
	cellapi "github.com/marcelheidebrecht/cell-platform/internal/api"
	"github.com/marcelheidebrecht/cell-platform/internal/authentik"
)

// apiClient is the identity's OAuth2 client for API logins (and its slug).
const apiClient = "cell-api"

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
	authURL := env("AUTHENTIK_URL", "http://authentik-server.cell-system.svc.cluster.local")
	hc := &http.Client{Timeout: 20 * time.Second}
	identity := &authentik.Client{BaseURL: authURL, Token: token, HTTP: hc}

	// The identity side of the API: its login client and the role groups.
	if err := retry(ctx, log, "identity setup", func() error {
		if err := identity.EnsureAPIClient(ctx, apiClient, "Platform API"); err != nil {
			return err
		}
		if err := identity.EnsureGroup(ctx, cellapi.GroupAdmins, "akadmin"); err != nil {
			return err
		}
		return identity.EnsureGroup(ctx, cellapi.GroupAgents)
	}); err != nil {
		return err
	}
	tokens := &cellapi.Tokens{UserinfoURL: authURL + "/application/o/userinfo/", ClientID: apiClient, HTTP: hc, TTL: 30 * time.Second}

	srv := &http.Server{
		Addr: ":8080",
		Handler: (&cellapi.Server{
			Kube: kube, Pods: clientset.CoreV1(), Identity: identity, Authenticate: tokens.Check,
			Catalog: catalog.FS, OpenAPI: api.OpenAPI, Log: log,
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
