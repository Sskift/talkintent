package protocol

import (
	"encoding/json"
	"testing"
)

func TestEnvelopeSerialization(t *testing.T) {
	hello := DaemonHelloPayload{
		MemberID:       "mem_123",
		ClientVersion:  "1.0.0",
		MachineName:    "laptop",
		MaxConcurrency: 2,
		Workspaces: []WorkspaceInfo{
			{ID: "ws1", Name: "backend", RootPath: "/src/backend", HasPrivacyPrompt: true},
		},
	}
	raw, err := json.Marshal(hello)
	if err != nil {
		t.Fatalf("failed to marshal hello: %v", err)
	}

	env := Envelope{
		Version:   Version1,
		Type:      TypeDaemonHello,
		ID:        "msg_1",
		Timestamp: 1726828800000,
		Payload:   raw,
	}

	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("failed to marshal envelope: %v", err)
	}

	var parsed Envelope
	if err := json.Unmarshal(envBytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal envelope: %v", err)
	}

	if parsed.Version != Version1 {
		t.Errorf("expected version %s, got %s", Version1, parsed.Version)
	}
	if parsed.Type != TypeDaemonHello {
		t.Errorf("expected type %s, got %s", TypeDaemonHello, parsed.Type)
	}

	var parsedHello DaemonHelloPayload
	if err := json.Unmarshal(parsed.Payload, &parsedHello); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if parsedHello.MemberID != "mem_123" {
		t.Errorf("expected member_id mem_123, got %s", parsedHello.MemberID)
	}
	if len(parsedHello.Workspaces) != 1 || parsedHello.Workspaces[0].Name != "backend" {
		t.Errorf("workspaces payload mismatch")
	}
}
