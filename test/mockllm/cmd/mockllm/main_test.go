package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func getFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestRunCommandHelp(t *testing.T) {
	err := run([]string{"-invalid-flag-12345"})
	if err == nil {
		t.Errorf("expected error with invalid flag, got nil")
	}
}

func TestRunCommandLifecycle(t *testing.T) {
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		err := runContext(ctx, []string{
			"-addr", addr,
			"-mode", "auto",
			"-canned", "CLI test answer",
			"-refuse", "forbidden",
			"-thinking=true",
			"-tools", "git_status,git_diff",
		})
		errCh <- err
	}()

	// Wait for server to become healthy
	baseURL := fmt.Sprintf("http://%s", addr)
	healthy := false
	for i := 0; i < 30; i++ {
		resp, err := http.Get(baseURL + "/healthz")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			healthy = true
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !healthy {
		t.Fatalf("server on %s failed to become healthy within timeout", addr)
	}

	// Verify /healthz and /v1/models
	resp, err := http.Get(baseURL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK from /v1/models, got %d", resp.StatusCode)
	}

	// Cancel context to trigger graceful shutdown
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("expected clean server shutdown, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("server shutdown timed out")
	}
}

func TestEnvHelpers(t *testing.T) {
	t.Setenv("TEST_STRING_VAR", "hello")
	if val := getEnv("TEST_STRING_VAR", "default"); val != "hello" {
		t.Errorf("expected hello, got %s", val)
	}
	if val := getEnv("UNSET_STRING_VAR", "default"); val != "default" {
		t.Errorf("expected default, got %s", val)
	}

	t.Setenv("TEST_BOOL_VAR", "true")
	if !getEnvBool("TEST_BOOL_VAR", false) {
		t.Errorf("expected true")
	}
	t.Setenv("TEST_BOOL_VAR_NUM", "1")
	if !getEnvBool("TEST_BOOL_VAR_NUM", false) {
		t.Errorf("expected true")
	}
	if getEnvBool("UNSET_BOOL_VAR", false) {
		t.Errorf("expected false for unset")
	}

	t.Setenv("TEST_DUR_VAR", "250ms")
	if d := getEnvDuration("TEST_DUR_VAR", 0); d != 250*time.Millisecond {
		t.Errorf("expected 250ms, got %v", d)
	}
	if d := getEnvDuration("UNSET_DUR_VAR", 5*time.Second); d != 5*time.Second {
		t.Errorf("expected 5s, got %v", d)
	}
}
