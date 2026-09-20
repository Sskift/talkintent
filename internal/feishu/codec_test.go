package feishu

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type GoldenVector struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	HexBytes    string `json:"hex_bytes"`
	ByteLength  int    `json:"byte_length"`
	Decoded     struct {
		SeqID           uint64   `json:"seq_id"`
		LogID           uint64   `json:"log_id"`
		Service         int32    `json:"service"`
		Method          int32    `json:"method"`
		HeaderList      []Header `json:"header_list"`
		PayloadEncoding string   `json:"payload_encoding"`
		PayloadType     string   `json:"payload_type"`
		PayloadHex      string   `json:"payload_hex"`
		LogIDNew        string   `json:"log_id_new"`
	} `json:"decoded"`
}

type GoldenFile struct {
	SDKVersion string         `json:"sdk_version"`
	Vectors    []GoldenVector `json:"vectors"`
}

func TestGoldenVectors(t *testing.T) {
	goldenPath := filepath.Join("testdata", "golden_frames.json")
	data, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("failed to read golden file: %v", err)
	}

	var gf GoldenFile
	if err := json.Unmarshal(data, &gf); err != nil {
		t.Fatalf("failed to parse golden file: %v", err)
	}

	for _, v := range gf.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			expectedBytes, err := hex.DecodeString(v.HexBytes)
			if err != nil {
				t.Fatalf("invalid hex in vector %s: %v", v.Name, err)
			}
			if len(expectedBytes) != v.ByteLength {
				t.Fatalf("vector %s byte length mismatch: got %d, want %d", v.Name, len(expectedBytes), v.ByteLength)
			}

			// 1. Unmarshal from wire bytes
			var f Frame
			if err := f.Unmarshal(expectedBytes); err != nil {
				t.Fatalf("Unmarshal failed for %s: %v", v.Name, err)
			}

			if f.SeqID != v.Decoded.SeqID {
				t.Errorf("SeqID mismatch: got %d, want %d", f.SeqID, v.Decoded.SeqID)
			}
			if f.LogID != v.Decoded.LogID {
				t.Errorf("LogID mismatch: got %d, want %d", f.LogID, v.Decoded.LogID)
			}
			if f.Service != v.Decoded.Service {
				t.Errorf("Service mismatch: got %d, want %d", f.Service, v.Decoded.Service)
			}
			if f.Method != v.Decoded.Method {
				t.Errorf("Method mismatch: got %d, want %d", f.Method, v.Decoded.Method)
			}

			if len(v.Decoded.HeaderList) > 0 {
				if !reflect.DeepEqual(f.Headers, v.Decoded.HeaderList) {
					t.Errorf("Headers mismatch: got %+v, want %+v", f.Headers, v.Decoded.HeaderList)
				}
			}

			if f.PayloadEncoding != v.Decoded.PayloadEncoding {
				t.Errorf("PayloadEncoding mismatch: got %q, want %q", f.PayloadEncoding, v.Decoded.PayloadEncoding)
			}
			if f.PayloadType != v.Decoded.PayloadType {
				t.Errorf("PayloadType mismatch: got %q, want %q", f.PayloadType, v.Decoded.PayloadType)
			}
			if f.LogIDNew != v.Decoded.LogIDNew {
				t.Errorf("LogIDNew mismatch: got %q, want %q", f.LogIDNew, v.Decoded.LogIDNew)
			}

			if v.Decoded.PayloadHex != "" {
				expPayload, _ := hex.DecodeString(v.Decoded.PayloadHex)
				if !reflect.DeepEqual(f.Payload, expPayload) {
					t.Errorf("Payload mismatch: got %x, want %x", f.Payload, expPayload)
				}
			} else if f.Payload != nil {
				t.Errorf("Expected nil payload, got %x", f.Payload)
			}

			// 2. Marshal back to wire bytes and check byte-for-byte exact match!
			wire, err := f.Marshal()
			if err != nil {
				t.Fatalf("Marshal failed for %s: %v", v.Name, err)
			}

			if !reflect.DeepEqual(wire, expectedBytes) {
				t.Errorf("Marshal byte-exact mismatch for %s:\ngot  %x\nwant %x", v.Name, wire, expectedBytes)
			}
		})
	}
}

func TestCodecEdgeCases(t *testing.T) {
	t.Run("truncated input", func(t *testing.T) {
		validPing, _ := hex.DecodeString("0800100018b96020002a0c0a0474797065120470696e6732003a004a00")
		for i := 0; i < len(validPing)-1; i++ {
			var f Frame
			// Must never panic
			_ = f.Unmarshal(validPing[:i])
		}
	})

	t.Run("skip unknown fields", func(t *testing.T) {
		// Create a frame with an unknown tag: e.g. tag 15 (15<<3 | 0 = 0x78) varint 99
		// followed by normal fields
		var f Frame
		f.SeqID = 100
		wire, err := f.Marshal()
		if err != nil {
			t.Fatal(err)
		}

		// Inject tag 15 (varint) and tag 16 (length delimited)
		injected := append([]byte{0x78, 0x05, 0x82, 0x01, 0x03, 'a', 'b', 'c'}, wire...)
		var f2 Frame
		if err := f2.Unmarshal(injected); err != nil {
			t.Fatalf("Unmarshal with unknown fields failed: %v", err)
		}
		if f2.SeqID != 100 {
			t.Errorf("SeqID mismatch after unknown fields: got %d, want 100", f2.SeqID)
		}
	})
}
