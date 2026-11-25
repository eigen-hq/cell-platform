package controller

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/marcelheidebrecht/cell-platform/internal/cell"
)

func TestACMEAccounts(t *testing.T) {
	ctx := context.Background()
	cfg := cell.Config{Domain: "acme-x.cells.test"}
	c := fake.NewClientBuilder().Build()
	// A cell without an address from the naming service: nothing to do.
	if err := ensureACMEAccount(ctx, c, cfg, "notes.acme-x.cells.test"); err != nil {
		t.Fatal(err)
	}
	secret := func(name string, data map[string]string) *corev1.Secret {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namesNamespace, Name: name}, Data: map[string][]byte{}}
		for k, v := range data {
			s.Data[k] = []byte(v)
		}
		return s
	}
	c = fake.NewClientBuilder().WithObjects(
		secret(namesSecret, map[string]string{"label": "acme-x", "token": "tok"}),
		secret(acmeAccounts, map[string]string{accountsKey: `{"auth.acme-x.cells.test":{"username":"acme-x","password":"tok","fulldomain":"auth.acme-x.cells.test","subdomain":"auth","allowfrom":[]}}`}),
	).Build()
	for range 2 {
		if err := ensureACMEAccount(ctx, c, cfg, "notes.acme-x.cells.test"); err != nil {
			t.Fatal(err)
		}
	}
	var s corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: namesNamespace, Name: acmeAccounts}, &s); err != nil {
		t.Fatal(err)
	}
	accounts := map[string]acmeAccount{}
	if err := json.Unmarshal(s.Data[accountsKey], &accounts); err != nil {
		t.Fatal(err)
	}
	notes := accounts["notes.acme-x.cells.test"]
	if len(accounts) != 2 || notes.SubDomain != "notes" || notes.Username != "acme-x" || notes.Password != "tok" ||
		notes.FullDomain != "notes.acme-x.cells.test" || accounts["auth.acme-x.cells.test"].SubDomain != "auth" {
		t.Errorf("accounts %+v", accounts)
	}
}
