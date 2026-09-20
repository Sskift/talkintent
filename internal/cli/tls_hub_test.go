package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/daemon"
	"github.com/Sskift/talkintent/internal/probe"
	"github.com/Sskift/talkintent/internal/protocol"
)

type dummyProbeAgent struct{}

func (d *dummyProbeAgent) Run(ctx context.Context, req *probe.RunRequest) (*protocol.QueryResponsePayload, error) {
	return &protocol.QueryResponsePayload{
		QueryID: req.QueryID,
		Status:  protocol.QueryStatusCompleted,
		Answer:  "dummy answer",
	}, nil
}

// generateTLSHubCert creates a self-signed server certificate bound to 127.0.0.1
// and writes the PEM certificate to caPath.
func generateTLSHubCert(t *testing.T, caPath string) tls.Certificate {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate private key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName:   "127.0.0.1",
			Organization: []string{"TalkIntent Test Hub"},
		},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certBytes})
	if err := os.WriteFile(caPath, certPEM, 0600); err != nil {
		t.Fatalf("failed to write CA PEM file: %v", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{certBytes},
		PrivateKey:  priv,
	}
}

// TestTLSHub_PrivateCA verifies that:
// (a) Connecting without hub_ca_file fails with x509 unknown authority.
// (b) Connecting with hub_ca_file succeeds across pair, REST commands (status, members), and daemon WS dial.
func TestTLSHub_PrivateCA(t *testing.T) {
	tempDir := t.TempDir()
	caFile := filepath.Join(tempDir, "hub-ca.pem")
	serverCert := generateTLSHubCert(t, caFile)

	wsConnected := make(chan struct{}, 1)

	// Build TLS Hub handler with pair, members, and WS daemon endpoints
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/pair", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.PairRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.InviteCode != "INV-TLS-TEST" {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{
				Error: protocol.ErrorDetail{Code: "INVALID_CODE", Message: "invalid invite code"},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(protocol.PairResponse{
			MemberID:   "mem_tls_user",
			MemberName: "Alice TLS",
			Token:      "ti_mem_tls_token_secret",
		})
	})

	mux.HandleFunc("/api/v1/members", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer ti_mem_tls_token_secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(protocol.MemberListResponse{
			Members: []protocol.MemberInfo{
				{ID: "mem_tls_user", Name: "Alice TLS", Online: true},
			},
		})
	})

	mux.HandleFunc("/api/v1/members/me", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer ti_mem_tls_token_secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "mem_tls_user", "name": "Alice TLS"})
	})

	mux.HandleFunc("/ws/daemon", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer ti_mem_tls_token_secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()

		select {
		case wsConnected <- struct{}{}:
		default:
		}

		// Read daemon_hello
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _, _ = conn.Read(ctx)
	})

	// Start TLS server bound explicitly to 127.0.0.1:0
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on 127.0.0.1: %v", err)
	}
	ts := httptest.NewUnstartedServer(mux)
	ts.Listener = l
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
	}
	ts.StartTLS()
	defer ts.Close()

	hubURL := ts.URL
	t.Logf("TLS Hub running at %s", hubURL)

	// -------------------------------------------------------------
	// Part (a): Without hub_ca_file -> Must fail with x509 error
	// -------------------------------------------------------------
	t.Run("Untrusted_FailsWithX509", func(t *testing.T) {
		// 1. Untrusted pair attempt
		var stdout, stderr bytes.Buffer
		untrustedCfgPath := filepath.Join(tempDir, "untrusted-config.json")
		exitCode := ExecutePair(context.Background(), PairOptions{
			HubURL:      hubURL,
			InviteCode:  "INV-TLS-TEST",
			MachineName: "test-node",
			ConfigPath:  untrustedCfgPath,
			HubCAFile:   "", // No CA configured
		}, &stdout, &stderr)

		if exitCode == 0 {
			t.Fatalf("expected pair to fail without hub_ca_file, got exit 0")
		}
		errStr := stderr.String()
		if !strings.Contains(errStr, "x509") && !strings.Contains(errStr, "certificate") && !strings.Contains(errStr, "unknown authority") {
			t.Errorf("expected x509 certificate error in stderr, got: %s", errStr)
		}

		// 2. Untrusted REST (members)
		_, err := FetchMembers(context.Background(), hubURL, "ti_mem_tls_token_secret", "")
		if err == nil {
			t.Fatal("expected FetchMembers to fail without hub_ca_file, got nil error")
		}
		if !strings.Contains(err.Error(), "x509") && !strings.Contains(err.Error(), "certificate") {
			t.Errorf("expected x509 error in FetchMembers, got: %v", err)
		}

		// 3. Untrusted Daemon WS Dial
		wsURL := "wss://" + strings.TrimPrefix(hubURL, "https://") + "/ws/daemon"
		untrustedClient, err := config.NewHubHTTPClient("", 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
			HTTPClient: untrustedClient,
			HTTPHeader: http.Header{"Authorization": []string{"Bearer ti_mem_tls_token_secret"}},
		})
		if err == nil {
			t.Fatal("expected websocket.Dial to fail without hub_ca_file, got nil error")
		}
		if !strings.Contains(err.Error(), "x509") && !strings.Contains(err.Error(), "certificate") {
			t.Errorf("expected x509 error in websocket.Dial, got: %v", err)
		}
	})

	// -------------------------------------------------------------
	// Part (b): With hub_ca_file -> Must succeed across all paths
	// -------------------------------------------------------------
	t.Run("Trusted_SucceedsAcrossAllPaths", func(t *testing.T) {
		trustedCfgPath := filepath.Join(tempDir, "trusted-config.json")
		var stdout, stderr bytes.Buffer

		// 1. Pair with -hub-ca-file
		exitCode := ExecutePair(context.Background(), PairOptions{
			HubURL:      hubURL,
			InviteCode:  "INV-TLS-TEST",
			MachineName: "test-node",
			ConfigPath:  trustedCfgPath,
			HubCAFile:   caFile,
		}, &stdout, &stderr)

		if exitCode != 0 {
			t.Fatalf("pair failed with hub_ca_file: exit %d, stderr: %s", exitCode, stderr.String())
		}

		// Verify that hub_ca_file is persisted in config.json as absolute path
		savedCfg, err := config.LoadClientConfig(trustedCfgPath)
		if err != nil {
			t.Fatalf("failed to load saved client config: %v", err)
		}
		absCAFile, _ := filepath.Abs(caFile)
		if savedCfg.HubCAFile != absCAFile {
			t.Errorf("expected saved HubCAFile %q, got %q", absCAFile, savedCfg.HubCAFile)
		}
		if savedCfg.MemberID != "mem_tls_user" {
			t.Errorf("expected MemberID 'mem_tls_user', got %q", savedCfg.MemberID)
		}

		// 2. REST members subcommand with saved configuration
		stdout.Reset()
		stderr.Reset()
		exitCode = ExecuteMembers(context.Background(), MembersOptions{
			ConfigPath: trustedCfgPath,
			JSONOutput: true,
		}, &stdout, &stderr)
		if exitCode != 0 {
			t.Fatalf("ExecuteMembers failed: exit %d, stderr: %s", exitCode, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Alice TLS") {
			t.Errorf("expected Alice TLS in members output, got: %s", stdout.String())
		}

		// 3. REST status subcommand with saved configuration
		stdout.Reset()
		stderr.Reset()
		exitCode = ExecuteStatus(context.Background(), StatusOptions{
			ConfigPath: trustedCfgPath,
			JSONOutput: true,
		}, &stdout, &stderr)
		if exitCode != 0 {
			t.Fatalf("ExecuteStatus failed: exit %d, stderr: %s", exitCode, stderr.String())
		}
		var statusRep StatusReport
		if err := json.Unmarshal(stdout.Bytes(), &statusRep); err != nil {
			t.Fatalf("failed to decode status report JSON: %v", err)
		}
		if !statusRep.Hub.Reachable {
			t.Errorf("expected Hub.Reachable true with hub_ca_file, got false (error: %s)", statusRep.Hub.Error)
		}

		// 4. Daemon WebSocket connection via StartDaemon / connectAndServe
		d, err := daemon.NewClientDaemon(savedCfg, &dummyProbeAgent{}, nil)
		if err != nil {
			t.Fatalf("failed to construct ClientDaemon: %v", err)
		}
		daemonCtx, cancelDaemon := context.WithCancel(context.Background())
		defer cancelDaemon()

		daemonErrCh := make(chan error, 1)
		go func() {
			daemonErrCh <- d.Start(daemonCtx)
		}()

		// Wait for WS connection to establish and be accepted by the TLS hub
		select {
		case <-wsConnected:
			t.Log("Daemon successfully connected to Hub over WSS with private CA!")
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for daemon to connect over WSS")
		}

		_ = d.Stop()
		cancelDaemon()
	})

	// -------------------------------------------------------------
	// Part (c): Environment variable override TALKINTENT_HUB_CA_FILE
	// -------------------------------------------------------------
	t.Run("EnvOverride_TALKINTENT_HUB_CA_FILE", func(t *testing.T) {
		envCfgPath := filepath.Join(tempDir, "env-config.json")
		// Config WITHOUT hub_ca_file
		rawCfg := &config.ClientConfig{
			HubURL:     hubURL,
			MemberID:   "mem_tls_user",
			MemberName: "Alice TLS",
			Token:      "ti_mem_tls_token_secret",
		}
		if err := config.SaveClientConfig(envCfgPath, rawCfg); err != nil {
			t.Fatal(err)
		}

		t.Setenv(config.EnvTalkIntentHubCAFile, caFile)

		var stdout, stderr bytes.Buffer
		exitCode := ExecuteMembers(context.Background(), MembersOptions{
			ConfigPath: envCfgPath,
			JSONOutput: true,
		}, &stdout, &stderr)

		if exitCode != 0 {
			t.Fatalf("ExecuteMembers failed with TALKINTENT_HUB_CA_FILE: exit %d, stderr: %s", exitCode, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Alice TLS") {
			t.Errorf("expected Alice TLS in members output via TALKINTENT_HUB_CA_FILE, got: %s", stdout.String())
		}
	})
}
