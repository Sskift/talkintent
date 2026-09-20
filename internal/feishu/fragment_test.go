package feishu

import (
	"bytes"
	"testing"
	"time"
)

func TestFragmentReassembler(t *testing.T) {
	t.Run("single fragment", func(t *testing.T) {
		r := NewFragmentReassembler(5 * time.Second)
		res := r.AddFragment("msg1", 1, 0, []byte("hello"))
		if string(res) != "hello" {
			t.Fatalf("expected 'hello', got %q", string(res))
		}
	})

	t.Run("two fragments in order", func(t *testing.T) {
		r := NewFragmentReassembler(5 * time.Second)
		res1 := r.AddFragment("msg2", 2, 0, []byte("part1-"))
		if res1 != nil {
			t.Fatalf("expected nil for first fragment, got %q", string(res1))
		}
		res2 := r.AddFragment("msg2", 2, 1, []byte("part2"))
		if string(res2) != "part1-part2" {
			t.Fatalf("expected 'part1-part2', got %q", string(res2))
		}
	})

	t.Run("out of order fragments", func(t *testing.T) {
		r := NewFragmentReassembler(5 * time.Second)
		res1 := r.AddFragment("msg3", 3, 2, []byte("part3"))
		if res1 != nil {
			t.Fatal("expected nil")
		}
		res2 := r.AddFragment("msg3", 3, 0, []byte("part1-"))
		if res2 != nil {
			t.Fatal("expected nil")
		}
		res3 := r.AddFragment("msg3", 3, 1, []byte("part2-"))
		if string(res3) != "part1-part2-part3" {
			t.Fatalf("expected 'part1-part2-part3', got %q", string(res3))
		}
	})

	t.Run("ttl expiration", func(t *testing.T) {
		r := NewFragmentReassembler(20 * time.Millisecond)
		_ = r.AddFragment("msg4", 2, 0, []byte("part1"))
		time.Sleep(50 * time.Millisecond)
		// Should be expired, so seq 1 arrives for a fresh or swept entry
		res := r.AddFragment("msg4", 2, 1, []byte("part2"))
		if res != nil {
			t.Fatalf("expected nil because first part expired, got %q", string(res))
		}
	})

	t.Run("golden vectors multipart", func(t *testing.T) {
		r := NewFragmentReassembler(5 * time.Second)
		part0 := []byte(`{"schema":"2.0","header":{"event_id":"ev_multi_999","event_type":"im.message.receive_v1"},`)
		part1 := []byte(`"event":{"message":{"message_id":"om_part2","content":"{\"text\":\"split payload\"}"}}}`)

		if r.AddFragment("msg_multipart_999", 2, 0, part0) != nil {
			t.Fatal("expected nil for seq 0")
		}
		combined := r.AddFragment("msg_multipart_999", 2, 1, part1)
		expected := append(part0, part1...)
		if !bytes.Equal(combined, expected) {
			t.Fatalf("mismatch on combined golden multipart: got %s, want %s", string(combined), string(expected))
		}
	})

	t.Run("inconsistent sum or invalid bounds", func(t *testing.T) {
		r := NewFragmentReassembler(5 * time.Second)
		// packet with unbounded sum
		if r.AddFragment("unbounded", 2000, 0, []byte("large")) != nil {
			t.Fatal("expected nil for sum > 1024")
		}

		// packet 1 with sum=2, seq=0
		if r.AddFragment("inconsistent", 2, 0, []byte("part0")) != nil {
			t.Fatal("expected nil for incomplete")
		}

		// packet 2 with sum=4, seq=3 -> inconsistent sum, should return nil without panicking
		if r.AddFragment("inconsistent", 4, 3, []byte("part3")) != nil {
			t.Fatal("expected nil for inconsistent sum")
		}
	})
}
