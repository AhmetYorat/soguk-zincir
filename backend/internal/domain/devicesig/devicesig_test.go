package devicesig

import (
	"bytes"
	"net/http"
	"strconv"
	"testing"
	"time"
)

var (
	now  = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	key  = DeriveKey([]byte("provisioning-secret"), "S-0001")
	body = []byte(`{"readings":[{"ts":"2026-10-01T12:00:00Z","temp_cc":412,"humidity_pm":650}]}`)
)

func signed(seq uint64, ts time.Time, b []byte, k []byte) http.Header {
	h := http.Header{}
	h.Set(HeaderDevice, "S-0001")
	h.Set(HeaderSeq, strconv.FormatUint(seq, 10))
	h.Set(HeaderTimestamp, strconv.FormatInt(ts.UnixMilli(), 10))
	h.Set(HeaderSignature, Sign(k, "S-0001", seq, ts.UnixMilli(), b))
	return h
}

func TestValidSignatureAccepted(t *testing.T) {
	h, err := Parse(signed(7, now, body, key).Get)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(key, h, body, now.Add(3*time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestTamperedBodyRejected(t *testing.T) {
	h, _ := Parse(signed(7, now, body, key).Get)
	evil := bytes.Replace(body, []byte("412"), []byte("402"), 1)
	if err := Verify(key, h, evil, now, time.Minute); err != ErrSignature {
		t.Fatalf("err = %v", err)
	}
}

func TestWrongKeyRejected(t *testing.T) {
	other := DeriveKey([]byte("provisioning-secret"), "S-0002")
	h, _ := Parse(signed(7, now, body, other).Get)
	if err := Verify(key, h, body, now, time.Minute); err != ErrSignature {
		t.Fatalf("err = %v", err)
	}
}

func TestChangedSeqInvalidatesSignature(t *testing.T) {
	hdr := signed(7, now, body, key)
	hdr.Set(HeaderSeq, "8") // attacker bumps the counter to dodge replay check
	h, _ := Parse(hdr.Get)
	if err := Verify(key, h, body, now, time.Minute); err != ErrSignature {
		t.Fatalf("err = %v", err)
	}
}

func TestClockSkewRejected(t *testing.T) {
	h, _ := Parse(signed(7, now.Add(-5*time.Minute), body, key).Get)
	if err := Verify(key, h, body, now, 2*time.Minute); err != ErrSkew {
		t.Fatalf("err = %v", err)
	}
	h, _ = Parse(signed(8, now.Add(5*time.Minute), body, key).Get)
	if err := Verify(key, h, body, now, 2*time.Minute); err != ErrSkew {
		t.Fatalf("future: err = %v", err)
	}
}

func TestReplayRule(t *testing.T) {
	if Accept(10, 10) || Accept(10, 9) || !Accept(10, 11) {
		t.Fatal("replay rule wrong")
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse(http.Header{}.Get); err != ErrMissing {
		t.Fatalf("err = %v", err)
	}
	hdr := signed(7, now, body, key)
	hdr.Set(HeaderSignature, "zz")
	if _, err := Parse(hdr.Get); err != ErrMalformed {
		t.Fatalf("err = %v", err)
	}
	hdr = signed(7, now, body, key)
	hdr.Set(HeaderSeq, "0")
	if _, err := Parse(hdr.Get); err != ErrMalformed {
		t.Fatalf("seq 0: err = %v", err)
	}
}

func TestDeriveKeyIsDeterministicAndDistinct(t *testing.T) {
	a := DeriveKey([]byte("s"), "S-1")
	if !bytes.Equal(a, DeriveKey([]byte("s"), "S-1")) || bytes.Equal(a, DeriveKey([]byte("s"), "S-2")) {
		t.Fatal("derivation wrong")
	}
}

func TestKeyboxRoundTripAndBinding(t *testing.T) {
	kb, err := NewKeybox(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ct, nonce, err := kb.Seal("S-0001", key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := kb.Open("S-0001", ct, nonce)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("round trip failed: %v", err)
	}
	if _, err := kb.Open("S-0002", ct, nonce); err == nil {
		t.Fatal("ciphertext must be bound to its device id")
	}
	if _, err := NewKeybox([]byte("short")); err == nil {
		t.Fatal("short master key accepted")
	}
}
