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
	// AgentGroup gives its members the agent role in the platform API.
	AgentGroup = "cell-agents"
	// AdminGroup gives its members the admin role in the platform API.
	AdminGroup = "cell-admins"
	// AgentAccount is the ops agent's account in the identity; its
	// credentials are the Secret of the same name in SystemNamespace.
	AgentAccount = "cell-agent"
	// MetricsURL is the cell's metrics store (E-87); it answers the
	// Prometheus query API.
	MetricsURL = "http://cell-metrics.cell-system.svc.cluster.local:8428"
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

// HTTPClient trusts the system roots for every host and the cell CA only for
// hosts in domain. *.localhost goes to the loopback address (RFC 6761), so it
// works against a local cell without any DNS setup.
func HTTPClient(domain string, caPEM []byte) (*http.Client, error) {
	system, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("system cert pool: %w", err)
	}
	tlsCfg := &tls.Config{RootCAs: system, MinVersion: tls.VersionTLS12}
	if len(caPEM) > 0 {
		cellRoots := x509.NewCertPool()
		if !cellRoots.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("cell CA is not valid PEM")
		}
		// Standard verification is replaced, not skipped: VerifyConnection
		// checks the chain against the roots that belong to the host.
		tlsCfg.InsecureSkipVerify = true
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("tls: no server certificate")
			}
			if cs.ServerName == "" {
				return fmt.Errorf("tls: refusing connection without server name (IP address?)")
			}
			roots := system
			if InDomain(cs.ServerName, domain) {
				roots = cellRoots
			}
			opts := x509.VerifyOptions{DNSName: cs.ServerName, Roots: roots, Intermediates: x509.NewCertPool()}
			for _, c := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err := cs.PeerCertificates[0].Verify(opts)
			return err
		}
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsCfg
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err == nil && InDomain(host, "localhost") {
			addr = net.JoinHostPort("127.0.0.1", port)
		}
		return dialer.DialContext(ctx, network, addr)
	}
	return &http.Client{Transport: tr, Timeout: 15 * time.Second}, nil
}

// InDomain reports whether host is domain or one of its subdomains.
func InDomain(host, domain string) bool {
	host, domain = strings.ToLower(strings.TrimSuffix(host, ".")), strings.ToLower(domain)
	return host == domain || strings.HasSuffix(host, "."+domain)
}
