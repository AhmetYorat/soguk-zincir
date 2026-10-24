// Package chain implements the tamper-evident hash chain used for excursion
// reports.
//
//	hash_n = SHA-256( hash_{n-1} || seq_n (8 bytes, big endian) ||
//	                  created_at_n (unix µs, 8 bytes) || canonical(payload_n) )
//
// The genesis prev hash is 32 zero bytes. Changing, removing or reordering
// any entry breaks every hash after it.
package chain

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"
)

// Genesis is the prev hash of the first entry.
var Genesis = make([]byte, sha256.Size)

// Canonical returns a deterministic JSON encoding: object keys sorted, no
// insignificant whitespace, numbers kept as written. It accepts raw JSON so
// that a payload read back from a jsonb column (which reorders keys) hashes
// to the same value as the one originally written.
func Canonical(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("chain: payload is not valid JSON: %w", err)
	}
	// encoding/json sorts map keys, which is all canonicalisation needs here.
	return json.Marshal(v)
}

// Link computes the hash of an entry.
func Link(prev []byte, seq int64, createdAt time.Time, canonical []byte) []byte {
	h := sha256.New()
	h.Write(prev)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(seq))
	h.Write(buf[:])
	binary.BigEndian.PutUint64(buf[:], uint64(createdAt.UnixMicro()))
	h.Write(buf[:])
	h.Write(canonical)
	return h.Sum(nil)
}

// Entry is a stored chain element.
type Entry struct {
	Seq       int64
	CreatedAt time.Time
	Payload   []byte // raw JSON as stored
	PrevHash  []byte
	Hash      []byte
}

// Problem describes the first inconsistency found.
type Problem struct {
	Seq    int64  `json:"seq"`
	Reason string `json:"reason"`
}

// Verify checks a full chain ordered by seq. It returns nil when intact.
func Verify(entries []Entry) *Problem {
	prev := Genesis
	for i, e := range entries {
		if e.Seq != int64(i+1) {
			return &Problem{Seq: e.Seq, Reason: fmt.Sprintf("sequence gap: expected %d", i+1)}
		}
		if !bytes.Equal(e.PrevHash, prev) {
			return &Problem{Seq: e.Seq, Reason: "prev_hash does not match previous entry"}
		}
		canon, err := Canonical(e.Payload)
		if err != nil {
			return &Problem{Seq: e.Seq, Reason: err.Error()}
		}
		if want := Link(prev, e.Seq, e.CreatedAt, canon); !bytes.Equal(want, e.Hash) {
			return &Problem{Seq: e.Seq, Reason: "hash mismatch: payload or timestamp was altered"}
		}
		prev = e.Hash
	}
	return nil
}
