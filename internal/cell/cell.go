// Package cell reads the cell configuration written by deploy/bootstrap.sh and
// provides clients that reach the cell the way a user does.
package cell

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	SystemNamespace = "cell-system"
	// Issuer is the ClusterIssuer every cell certificate comes from.
	Issuer = "cell"
)

// Config is the cell-system/cell-config ConfigMap.
type Config struct {
	Domain  string
	TLS     string // selfsigned | acme
	Storage string // local-path | longhorn
}

func LoadConfig(ctx context.Context, c client.Reader) (Config, error) {
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: SystemNamespace, Name: "cell-config"}, &cm); err != nil {
		return Config{}, fmt.Errorf("read cell config: %w", err)
	}
	cfg := Config{Domain: cm.Data["domain"], TLS: cm.Data["tls"], Storage: cm.Data["storage"]}
	if cfg.Domain == "" {
		return Config{}, fmt.Errorf("cell config has no domain")
	}
	return cfg, nil
}

// Host returns the full hostname of a subdomain in this cell.
func (c Config) Host(subdomain string) string { return subdomain + "." + c.Domain }

// LoadCA returns the cell root CA (PEM) for self-signed cells, nil otherwise.
func LoadCA(ctx context.Context, c client.Reader, cfg Config) ([]byte, error) {
	if cfg.TLS != "selfsigned" {
		return nil, nil
	}
	var s corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "cert-manager", Name: "cell-ca"}, &s); err != nil {
		return nil, fmt.Errorf("read cell CA: %w", err)
	}
	return s.Data["ca.crt"], nil
}

// HTTPClient trusts the system roots plus the cell CA and sends *.localhost
// to the loopback address (RFC 6761), so it works against a local cell
// without any DNS setup.
func HTTPClient(caPEM []byte) (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("system cert pool: %w", err)
	}
	if len(caPEM) > 0 && !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("cell CA is not valid PEM")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err == nil && (host == "localhost" || strings.HasSuffix(host, ".localhost")) {
			addr = net.JoinHostPort("127.0.0.1", port)
		}
		return dialer.DialContext(ctx, network, addr)
	}
	return &http.Client{Transport: tr, Timeout: 15 * time.Second}, nil
}
