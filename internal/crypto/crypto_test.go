package crypto

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

const master = "0123456789abcdef0123456789abcdef"

func TestSealRoundTrip(t *testing.T) {
	s, err := NewSealer(master, LabelRCON)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := s.Seal("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	pt, err := s.Open(ct)
	if err != nil || pt != "hunter2" {
		t.Fatalf("got %q, %v", pt, err)
	}
	ct2, _ := s.Seal("hunter2")
	if ct == ct2 {
		t.Error("nonce reuse: identical ciphertexts")
	}
}

func TestLabelsIsolated(t *testing.T) {
	a, _ := NewSealer(master, LabelRCON)
	b, _ := NewSealer(master, LabelWebhook)
	ct, _ := a.Seal("x")
	if _, err := b.Open(ct); err == nil {
		t.Error("ciphertext opened under a different label")
	}
}

func TestSHA256Hex(t *testing.T) {
	sum := sha256.Sum256([]byte("abc"))
	if SHA256Hex("abc") != hex.EncodeToString(sum[:]) {
		t.Error("mismatch")
	}
}
