// The naming service (E-55): addresses and certificate challenges for cells
// under a zone in the Hetzner Cloud DNS. Configuration from the environment:
// NAMES_ZONE, NAMES_DNS_TOKEN, NAMES_KEYS (registration keys, comma
// separated), NAMES_SECRET (signs the cells' tokens).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/marcelheidebrecht/cell-platform/internal/names"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	need := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			log.Error("missing setting", "name", k)
			os.Exit(1)
		}
		return v
	}
	zone := need("NAMES_ZONE")
	s := &names.Server{
		Zone:   zone,
		Keys:   strings.Split(need("NAMES_KEYS"), ","),
		Secret: []byte(need("NAMES_SECRET")),
		DNS:    &names.Hetzner{Token: need("NAMES_DNS_TOKEN"), Zone: zone, HTTP: &http.Client{Timeout: 20 * time.Second}},
		Log:    log,
	}
	srv := &http.Server{Addr: ":8080", Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	log.Info("naming service", "zone", zone)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}
