package config

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
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewHubTLSConfig_Empty(t *testing.T) {
	cfg, err := NewHubTLSConfig("")
	if err != nil {
		t.Fatalf("unexpected error for empty caFile: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil tls.Config for empty caFile, got %+v", cfg)
	}

	client, err := NewHubHTTPClient("", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error for empty caFile: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil http.Client")
	}
	if client.Timeout != 5*time.Second {
		t.Errorf("expected timeout 5s, got %v", client.Timeout)
	}
}

func TestNewHubTLSConfig_NonExistentFile(t *testing.T) {
	_, err := NewHubTLSConfig(filepath.Join(t.TempDir(), "nonexistent.pem"))
	if err == nil {
		t.Fatal("expected error for non-existent caFile, got nil")
	}
}

func TestNewHubTLSConfig_InvalidPEM(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(tmp, []byte("NOT A PEM CERTIFICATE"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := NewHubTLSConfig(tmp)
	if err == nil {
		t.Fatal("expected error for invalid PEM, got nil")
	}
}

func TestNewHubTLSConfig_BareLeafCertificate(t *testing.T) {
	// Generate an intermediate/issuer CA
	caPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "Custom Test CA",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(1 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caBytes, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caPriv.PublicKey, caPriv)
	if err != nil {
		t.Fatal(err)
	}

	// Generate a leaf certificate issued by the CA (isCA: false)
	leafPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			CommonName: "127.0.0.1",
		},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:   time.Now().Add(-1 * time.Hour),
		NotAfter:    time.Now().Add(1 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafBytes, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafPriv.PublicKey, caPriv)
	if err != nil {
		t.Fatal(err)
	}

	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafBytes})
	caFile := filepath.Join(t.TempDir(), "leaf.pem")
	if err := os.WriteFile(caFile, leafPEM, 0600); err != nil {
		t.Fatal(err)
	}

	// Start TLS server with this leaf cert
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok from tls server"))
	}))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = l
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{
			{
				Certificate: [][]byte{leafBytes},
				PrivateKey:  leafPriv,
			},
		},
	}
	server.StartTLS()
	defer server.Close()

	// 1. Without hub_ca_file -> should fail verification
	untrustedClient, err := NewHubHTTPClient("", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = untrustedClient.Get(server.URL)
	if err == nil {
		t.Fatal("expected TLS verification failure without CA, got success")
	}

	// 2. With bare leaf hub_ca_file -> should succeed
	trustedClient, err := NewHubHTTPClient(caFile, 5*time.Second)
	if err != nil {
		t.Fatalf("failed to build trusted client: %v", err)
	}
	resp, err := trustedClient.Get(server.URL)
	if err != nil {
		t.Fatalf("expected success with bare leaf certificate in hub_ca_file, got: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected HTTP 200, got %d", resp.StatusCode)
	}

	_ = caBytes
}
