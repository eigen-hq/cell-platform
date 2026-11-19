// The cell operator.
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
	"github.com/marcelheidebrecht/cell-platform/internal/authentik"
	"github.com/marcelheidebrecht/cell-platform/internal/controller"
)

func main() {
	ctrl.SetLogger(zap.New())
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := cellv1.AddToScheme(scheme); err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: ":8081",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return err
	}
	systemCAs, err := os.ReadFile("/etc/ssl/certs/ca-certificates.crt")
	if err != nil {
		return fmt.Errorf("system CA bundle: %w", err)
	}
	token := os.Getenv("AUTHENTIK_TOKEN")
	if token == "" {
		return fmt.Errorf("AUTHENTIK_TOKEN is not set")
	}
	r := &controller.AppReconciler{
		Client: mgr.GetClient(),
		Identity: &authentik.Client{
			BaseURL: env("AUTHENTIK_URL", "http://authentik-server.cell-system.svc.cluster.local"),
			Token:   token,
			HTTP:    &http.Client{Timeout: 20 * time.Second},
		},
		SystemCAs:   systemCAs,
		BridgeImage: os.Getenv("BRIDGE_IMAGE"),
	}
	if err := r.SetupWithManager(mgr); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		return err
	}
	return mgr.Start(ctrl.SetupSignalHandler())
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
