package chain

import (
	"bytes"
	"testing"
	"time"
)

func build(t *testing.T, payloads ...string) []Entry {
	t.Helper()
	prev := Genesis
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var out []Entry
	for i, p := range payloads {
		canon, err := Canonical([]byte(p))
		if err != nil {
			t.Fatal(err)
		}
		at := base.Add(time.Duration(i) * time.Minute)
		h := Link(prev, int64(i+1), at, canon)
		out = append(out, Entry{Seq: int64(i + 1), CreatedAt: at, Payload: []byte(p), PrevHash: prev, Hash: h})
		prev = h
	}
	return out
}

func TestIntactChainVerifies(t *testing.T) {
	es := build(t, `{"lot":"L1","minutes":12}`, `{"lot":"L2","minutes":3}`, `{"lot":"L3"}`)
	if p := Verify(es); p != nil {
		t.Fatalf("unexpected problem %+v", p)
	}
}

func TestCanonicalIgnoresKeyOrderAndWhitespace(t *testing.T) {
	a, _ := Canonical([]byte(`{"b":1, "a":{"y":2,"x":1.50}}`))
	b, _ := Canonical([]byte(`{"a":{"x":1.50,"y":2},"b":1}`))
	if !bytes.Equal(a, b) {
		t.Fatalf("%s != %s", a, b)
	}
	// numbers are kept as written, not re-rounded through float64
	if !bytes.Contains(a, []byte("1.50")) {
		t.Fatalf("number changed: %s", a)
	}
}

func TestPayloadReorderedByJSONBStillVerifies(t *testing.T) {
	es := build(t, `{"z":1,"a":2}`)
	es[0].Payload = []byte(`{"a": 2, "z": 1}`) // what jsonb gives back
	if p := Verify(es); p != nil {
		t.Fatalf("%+v", p)
	}
}

func TestTamperedPayloadDetected(t *testing.T) {
	es := build(t, `{"minutes":12}`, `{"minutes":3}`, `{"minutes":7}`)
	es[1].Payload = []byte(`{"minutes":0}`)
	p := Verify(es)
	if p == nil || p.Seq != 2 {
		t.Fatalf("expected problem at seq 2, got %+v", p)
	}
}

func TestTamperedTimestampDetected(t *testing.T) {
	es := build(t, `{"a":1}`, `{"a":2}`)
	es[0].CreatedAt = es[0].CreatedAt.Add(time.Second)
	if p := Verify(es); p == nil || p.Seq != 1 {
		t.Fatalf("got %+v", p)
	}
}

func TestDeletedEntryDetected(t *testing.T) {
	es := build(t, `{"a":1}`, `{"a":2}`, `{"a":3}`)
	es = append(es[:1], es[2:]...)
	if p := Verify(es); p == nil {
		t.Fatal("deletion not detected")
	}
}

func TestRecomputedHashStillBreaksSuccessor(t *testing.T) {
	// An attacker who edits entry 1 and recomputes its hash still breaks
	// entry 2's prev_hash link.
	es := build(t, `{"a":1}`, `{"a":2}`)
	canon, _ := Canonical([]byte(`{"a":100}`))
	es[0].Payload = []byte(`{"a":100}`)
	es[0].Hash = Link(Genesis, 1, es[0].CreatedAt, canon)
	if p := Verify(es); p == nil || p.Seq != 2 {
		t.Fatalf("got %+v", p)
	}
}

func TestInvalidJSONRejected(t *testing.T) {
	if _, err := Canonical([]byte(`{nope`)); err == nil {
		t.Fatal("expected error")
	}
}
