package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/marcelheidebrecht/cell-platform/internal/cell"
)

// A cell with an address from the naming service (E-55) proves its
// certificates through it. cert-manager's acmeDNS solver looks its account
// up by host, so every host needs an entry before its certificate is asked
// for; bootstrap writes those of the platform hosts.
const (
	namesNamespace = "cert-manager"
	namesSecret    = "cell-names"
	acmeAccounts   = "cell-acme-dns"
	accountsKey    = "accounts.json"
)

type acmeAccount struct {
	Username   string   `json:"username"`
	Password   string   `json:"password"`
	FullDomain string   `json:"fulldomain"`
	SubDomain  string   `json:"subdomain"`
	AllowFrom  []string `json:"allowfrom"`
}

// ensureACMEAccount adds the host's account; a cell without an address from
// the naming service has nothing to do.
func ensureACMEAccount(ctx context.Context, c client.Client, cfg cell.Config, host string) error {
	var names corev1.Secret
	err := c.Get(ctx, client.ObjectKey{Namespace: namesNamespace, Name: namesSecret}, &names)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var s corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: namesNamespace, Name: acmeAccounts}, &s); err != nil {
		return fmt.Errorf("acme-dns accounts: %w", err)
	}
	accounts := map[string]acmeAccount{}
	if raw := s.Data[accountsKey]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &accounts); err != nil {
			return fmt.Errorf("acme-dns accounts: %w", err)
		}
	}
	if _, ok := accounts[host]; ok {
		return nil
	}
	accounts[host] = acmeAccount{
		Username: string(names.Data["label"]), Password: string(names.Data["token"]),
		FullDomain: host, SubDomain: strings.TrimSuffix(strings.TrimSuffix(host, cfg.Domain), "."), AllowFrom: []string{},
	}
	raw, err := json.Marshal(accounts)
	if err != nil {
		return err
	}
	if s.Data == nil {
		s.Data = map[string][]byte{}
	}
	s.Data[accountsKey] = raw
	// Conflicts with another writer retry with the reconcile.
	return c.Update(ctx, &s)
}
