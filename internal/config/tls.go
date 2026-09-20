package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// NewHubTLSConfig constructs a *tls.Config configured to trust the certificate(s) in caFile.
// If caFile is empty, it returns nil, indicating that standard default TLS verification is used.
// It supports private CA certificates and bare leaf certificates (Go natively validates both
// when present in RootCAs).
func NewHubTLSConfig(caFile string) (*tls.Config, error) {
	caFile = strings.TrimSpace(caFile)
	if caFile == "" {
		return nil, nil
	}

	pemData, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read hub_ca_file %q: %w", caFile, err)
	}

	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemData) {
		// Bare leaf certificate in pool or system pool was incompatible: try a clean empty pool
		pool = x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemData) {
			return nil, fmt.Errorf("failed to parse valid PEM certificate from hub_ca_file %q", caFile)
		}
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
	}, nil
}

// NewHubHTTPClient constructs an *http.Client configured for communicating with the Hub,
// honoring custom CA certificates if configured via caFile.
// If caFile is empty, standard system certificate trust is used.
// Plaintext http/ws connections bypass TLS automatically without behavioral differences.
func NewHubHTTPClient(caFile string, timeout time.Duration) (*http.Client, error) {
	tlsConfig, err := NewHubTLSConfig(caFile)
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: tlsConfig,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}, nil
}
