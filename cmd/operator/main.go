// The cell operator.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/authentik"
	"github.com/eigen-hq/cell-platform/internal/cell"
	"github.com/eigen-hq/cell-platform/internal/controller"
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
	opts := controller.ManagerOptions(scheme)
	opts.HealthProbeBindAddress = ":8081"
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), opts)
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
	identity := &authentik.Client{
		BaseURL: env("AUTHENTIK_URL", "http://authentik-server.cell-system.svc.cluster.local"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
	}
	r := &controller.AppReconciler{
		Client:      mgr.GetClient(),
		Identity:    identity,
		SystemCAs:   systemCAs,
		BridgeImage: os.Getenv("BRIDGE_IMAGE"),
	}
	if err := r.SetupWithManager(mgr); err != nil {
		return err
	}
	// The ops agent's account and the MCP gateway's login client; the
	// identity may still be starting.
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		for {
			err := controller.EnsureAgentAccount(ctx, mgr.GetClient(), identity)
			if err == nil {
				err = identity.EnsureAPIClient(ctx, controller.GatewayClient, "MCP gateway", controller.GatewayTokenValidity)
			}
			if err == nil {
				return nil
			}
			ctrl.Log.Info("waiting to set up the agent's account and the gateway's client", "error", err.Error())
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(10 * time.Second):
			}
		}
	})); err != nil {
		return err
	}
	if controller.PostgresImage = os.Getenv("POSTGRES_IMAGE"); controller.PostgresImage == "" {
		return fmt.Errorf("POSTGRES_IMAGE is not set")
	}
	restic := os.Getenv("RESTIC_IMAGE")
	if restic == "" {
		return fmt.Errorf("RESTIC_IMAGE is not set")
	}
	if err := (&controller.BackupReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), ResticImage: restic}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.RestoreReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), ResticImage: restic, Restored: r.ForgetLogin}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.ChangeReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader()}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.GatewayReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return err
	}
	admins := func(ctx context.Context) ([]string, error) {
		users, err := identity.ListUsers(ctx)
		var to []string
		for _, u := range users {
			if u.Email != "" && slices.Contains(u.Groups, cell.AdminGroup) {
				to = append(to, u.Email)
			}
		}
		return to, err
	}
	if err := (&controller.MailReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Identity: identity}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.WarningReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Recipients: admins}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.ModelsReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.JoinKeyReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader()}).SetupWithManager(mgr); err != nil {
		return err
	}
	rclone := os.Getenv("RCLONE_IMAGE")
	if rclone == "" {
		return fmt.Errorf("RCLONE_IMAGE is not set")
	}
	if err := (&controller.BackupTargetReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), RcloneImage: rclone}).SetupWithManager(mgr); err != nil {
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
