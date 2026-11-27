//go:build e2e

package e2e

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/eigen-hq/cell-platform/internal/cell"
)

// Security: the deployed cell CA only vouches for names in the cell domain.
// A cell with public certificates (acme) has no CA of its own.
func TestCellCAConstraints(t *testing.T) {
	ctx := context.Background()
	c := kube(t)
	cfg, err := cell.LoadConfig(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLS != "selfsigned" {
		t.Skipf("the cell's certificates are %s: no cell CA", cfg.TLS)
	}
	var s corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "cert-manager", Name: "cell-ca"}, &s); err != nil {
		t.Fatal(err)
	}
	ca := parseCert(t, s.Data["tls.crt"])
	key := parseKey(t, s.Data["tls.key"])
	roots := x509.NewCertPool()
	roots.AddCert(ca)

	sign := func(dns []string, ips []net.IP) *x509.Certificate {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
			DNSNames: dns, IPAddresses: ips, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}, ca, &k.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		cert, _ := x509.ParseCertificate(der)
		return cert
	}
	verify := func(cert *x509.Certificate, name string) error {
		_, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: name})
		return err
	}

	inside := "x." + cfg.Domain
	if err := verify(sign([]string{inside}, nil), inside); err != nil {
		t.Errorf("name in cell domain rejected: %v", err)
	}
	if verify(sign([]string{"example.com"}, nil), "example.com") == nil {
		t.Error("CA vouched for example.com")
	}
	if verify(sign(nil, []net.IP{net.ParseIP("203.0.113.10")}), "203.0.113.10") == nil {
		t.Error("CA vouched for an IPv4 address")
	}
	if verify(sign(nil, []net.IP{net.ParseIP("2001:db8::1")}), "2001:db8::1") == nil {
		t.Error("CA vouched for an IPv6 address")
	}
}

func parseCert(t *testing.T, data []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(data)
	if b == nil {
		t.Fatal("no PEM certificate")
	}
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func parseKey(t *testing.T, data []byte) crypto.Signer {
	t.Helper()
	b, _ := pem.Decode(data)
	if b == nil {
		t.Fatal("no PEM key")
	}
	if k, err := x509.ParseECPrivateKey(b.Bytes); err == nil {
		return k
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return k.(crypto.Signer)
}
