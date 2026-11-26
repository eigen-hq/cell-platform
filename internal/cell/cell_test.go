package cell

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// The cell CA is trusted for hosts in the cell domain only.
func TestHTTPClientScopesCellCA(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test cell ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames:    []string{"auth.cell.localhost", "evil.localhost"},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}}
	srv.StartTLS()
	defer srv.Close()
	port := mustURL(t, srv.URL).Port()

	hc, err := HTTPClient("cell.localhost", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Get("https://auth.cell.localhost:" + port + "/")
	if err != nil {
		t.Fatalf("host in cell domain: %v", err)
	}
	_ = resp.Body.Close()
	if _, err := hc.Get("https://evil.localhost:" + port + "/"); err == nil {
		t.Fatal("cell CA was trusted for a host outside the cell domain")
	}
	if _, err := hc.Get("https://127.0.0.1:" + port + "/"); err == nil {
		t.Fatal("connection without server name was accepted")
	}
}

func TestInDomain(t *testing.T) {
	for host, want := range map[string]bool{
		"cell.localhost": true, "auth.cell.localhost": true, "AUTH.Cell.Localhost.": true,
		"xcell.localhost": false, "cell.localhost.evil": false, "localhost": false,
	} {
		if got := InDomain(host, "cell.localhost"); got != want {
			t.Errorf("InDomain(%q) = %v", host, got)
		}
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
