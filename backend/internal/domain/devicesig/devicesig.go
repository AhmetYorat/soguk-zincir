// Package devicesig authenticates sensor devices.
//
// Every device has its own 32-byte key. A request carries
//
//	X-Device-Id:  sensor code
//	X-Seq:        strictly increasing message counter
//	X-Timestamp:  unix milliseconds when the device signed it
//	X-Signature:  hex HMAC-SHA256(key, "v1\n" + id + "\n" + seq + "\n" + ts + "\n" + hex(sha256(body)))
//
// The server rejects bad signatures, timestamps outside the allowed clock
// skew, and any seq that is not greater than the last accepted one (replay).
// The seq check must be done atomically in storage; Accept is the rule.
package devicesig

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Header names.
const (
	HeaderDevice    = "X-Device-Id"
	HeaderSeq       = "X-Seq"
	HeaderTimestamp = "X-Timestamp"
	HeaderSignature = "X-Signature"
)

// Errors returned by Verify.
var (
	ErrMissing   = errors.New("missing authentication headers")
	ErrMalformed = errors.New("malformed authentication headers")
	ErrSignature = errors.New("invalid signature")
	ErrSkew      = errors.New("timestamp outside allowed clock skew")
	ErrReplay    = errors.New("sequence number already used (replay)")
)

// StringToSign builds the canonical message.
func StringToSign(deviceID string, seq uint64, tsMillis int64, body []byte) string {
	sum := sha256.Sum256(body)
	return "v1\n" + deviceID + "\n" + strconv.FormatUint(seq, 10) + "\n" +
		strconv.FormatInt(tsMillis, 10) + "\n" + hex.EncodeToString(sum[:])
}

// Sign returns the hex signature.
func Sign(key []byte, deviceID string, seq uint64, tsMillis int64, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(StringToSign(deviceID, seq, tsMillis, body)))
	return hex.EncodeToString(mac.Sum(nil))
}

// Headers are the parsed request headers.
type Headers struct {
	DeviceID  string
	Seq       uint64
	Timestamp time.Time
	TSMillis  int64
	Signature []byte
}

// Parse validates the presence and shape of the headers.
func Parse(get func(string) string) (Headers, error) {
	id, seqS, tsS, sigS := get(HeaderDevice), get(HeaderSeq), get(HeaderTimestamp), get(HeaderSignature)
	if id == "" || seqS == "" || tsS == "" || sigS == "" {
		return Headers{}, ErrMissing
	}
	seq, err := strconv.ParseUint(seqS, 10, 64)
	if err != nil || seq == 0 {
		return Headers{}, ErrMalformed
	}
	ts, err := strconv.ParseInt(tsS, 10, 64)
	if err != nil {
		return Headers{}, ErrMalformed
	}
	sig, err := hex.DecodeString(sigS)
	if err != nil || len(sig) != sha256.Size {
		return Headers{}, ErrMalformed
	}
	return Headers{DeviceID: id, Seq: seq, TSMillis: ts, Timestamp: time.UnixMilli(ts), Signature: sig}, nil
}

// Verify checks signature and clock skew. It does not check the sequence.
func Verify(key []byte, h Headers, body []byte, now time.Time, skew time.Duration) error {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(StringToSign(h.DeviceID, h.Seq, h.TSMillis, body)))
	if !hmac.Equal(mac.Sum(nil), h.Signature) {
		return ErrSignature
	}
	if d := now.Sub(h.Timestamp); d > skew || d < -skew {
		return ErrSkew
	}
	return nil
}

// Accept is the replay rule: a sequence number is accepted only if it is
// greater than the last accepted one.
func Accept(lastSeq, seq uint64) bool { return seq > lastSeq }

// DeriveKey derives a device key from the provisioning secret, so that the
// seed job and the simulator can agree on keys without exchanging them.
// A real deployment would provision random keys at manufacturing time.
func DeriveKey(provisioningSecret []byte, deviceID string) []byte {
	mac := hmac.New(sha256.New, provisioningSecret)
	mac.Write([]byte("device-key/v1/" + deviceID))
	return mac.Sum(nil)
}

// Keybox encrypts device keys at rest with AES-256-GCM.
type Keybox struct{ aead cipher.AEAD }

// NewKeybox takes a 32-byte master key.
func NewKeybox(master []byte) (*Keybox, error) {
	if len(master) != 32 {
		return nil, fmt.Errorf("devicesig: master key must be 32 bytes, got %d", len(master))
	}
	block, err := aes.NewCipher(master)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Keybox{aead: aead}, nil
}

// Seal encrypts a device key; the device id is bound as additional data so a
// ciphertext cannot be moved to another device row.
func (k *Keybox) Seal(deviceID string, key []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return k.aead.Seal(nil, nonce, key, []byte(deviceID)), nonce, nil
}

// Open decrypts a device key.
func (k *Keybox) Open(deviceID string, ciphertext, nonce []byte) ([]byte, error) {
	return k.aead.Open(nil, nonce, ciphertext, []byte(deviceID))
}
