// The ops agent: works on the cell's tasks through the platform API.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/marcelheidebrecht/cell-platform/internal/agent"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	username, password := os.Getenv("CELL_AGENT_USERNAME"), os.Getenv("CELL_AGENT_PASSWORD")
	if username == "" || password == "" {
		return fmt.Errorf("CELL_AGENT_USERNAME and CELL_AGENT_PASSWORD are required (Secret cell-agent)")
	}
	a := &agent.Agent{
		API: &agent.API{
			URL:      env("CELL_API_URL", "http://cell-api.cell-system.svc.cluster.local"),
			TokenURL: env("CELL_TOKEN_URL", "http://authentik-server.cell-system.svc.cluster.local/application/o/token/"),
			ClientID: "cell-api", Username: username, Password: password,
			HTTP: &http.Client{Timeout: 60 * time.Second},
		},
		// E-33: the customer's model key; the model can be exchanged.
		ModelID: env("CELL_AGENT_MODEL", "claude-opus-5-5"),
		Log:     log,
		Poll:    10 * time.Second,
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		opts := []option.RequestOption{option.WithAPIKey(key)}
		if u := os.Getenv("ANTHROPIC_BASE_URL"); u != "" {
			opts = append(opts, option.WithBaseURL(u))
		}
		m := anthropic.NewClient(opts...)
		a.Model = &m
	}
	log.Info("agent started", "account", username, "model", a.ModelID, "modelKey", a.Model != nil)
	return a.Run(ctx)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
