package actionsecurity

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"

	"golang.org/x/crypto/hkdf"
)

func TestCryptoGoldenAndSeparation(t *testing.T) {
	root := []byte("0123456789abcdef0123456789abcdef")
	rootBefore := append([]byte(nil), root...)
	c, err := NewCrypto(root)
	if err != nil {
		t.Fatalf("NewCrypto() error = %v", err)
	}
	if !bytes.Equal(root, rootBefore) {
		t.Fatal("NewCrypto mutated caller root key")
	}

	var raw [32]byte
	copy(raw[:], []byte("fixed-payload-for-golden-tests-12"))
	intentPayload := []byte("canonical-intent")
	tests := []struct {
		name, info, purpose string
		got                 [32]byte
		payload             []byte
	}{
		{name: "ticket", info: "porsche/admin-action/ticket/v1", purpose: "ticket-value", got: c.TicketDigest(raw), payload: raw[:]},
		{name: "intent", info: "porsche/admin-action/intent/v1", purpose: "intent-v1", got: c.IntentDigest(intentPayload), payload: intentPayload},
		{name: "idempotency", info: "porsche/admin-action/idempotency/v1", purpose: "idempotency-value", got: c.IdempotencyDigest(raw), payload: raw[:]},
		{name: "lease", info: "porsche/admin-action/lease/v1", purpose: "lease-owner", got: c.LeaseOwnerDigest(raw), payload: raw[:]},
		{name: "response", info: "porsche/admin-action/response/v1", purpose: "response-v1", got: c.ResponseDigest(intentPayload), payload: intentPayload},
	}
	seen := map[string]string{}
	for _, tc := range tests {
		want := referenceDigest(t, rootBefore, tc.info, tc.purpose, tc.payload)
		if tc.got != want {
			t.Fatalf("%s digest differs from independent reference", tc.name)
		}
		hexDigest := hex.EncodeToString(tc.got[:])
		if previous, ok := seen[hexDigest]; ok {
			t.Fatalf("%s digest equals %s digest", tc.name, previous)
		}
		seen[hexDigest] = tc.name
	}

	withoutNUL := referenceDigestWithoutNUL(t, rootBefore, tests[0].info, tests[0].purpose, tests[0].payload)
	if tests[0].got == withoutNUL {
		t.Fatal("purpose separator NUL was omitted")
	}
}

func TestCryptoRateDigestAndEquality(t *testing.T) {
	root := []byte("fedcba9876543210fedcba9876543210")
	c, err := NewCrypto(root)
	if err != nil {
		t.Fatalf("NewCrypto() error = %v", err)
	}
	payload := []byte("rate-dimension")
	for _, tc := range []struct {
		purpose, info string
	}{
		{RateVerificationActor, "porsche/admin-action/ticket/v1"},
		{RateVerificationIP, "porsche/admin-action/ticket/v1"},
		{RateVerificationSession, "porsche/admin-action/ticket/v1"},
		{RateBeginSession, "porsche/admin-action/idempotency/v1"},
	} {
		got, err := c.RateDigest(tc.purpose, payload)
		if err != nil {
			t.Fatalf("RateDigest(%q) error = %v", tc.purpose, err)
		}
		want := referenceDigest(t, root, tc.info, tc.purpose, payload)
		if got != want {
			t.Fatalf("RateDigest(%q) differs from independent reference", tc.purpose)
		}
	}
	if _, err := c.RateDigest("rate-caller-selected", payload); err == nil {
		t.Fatal("RateDigest accepted caller-selected purpose")
	}
	var a, b [32]byte
	a[0], b[0] = 1, 1
	if !EqualDigest(a, b) {
		t.Fatal("EqualDigest rejected equal digests")
	}
	b[31] = 1
	if EqualDigest(a, b) {
		t.Fatal("EqualDigest accepted unequal digests")
	}
	if _, err := NewCrypto(root[:31]); err == nil {
		t.Fatal("NewCrypto accepted non-32-byte root")
	}
}

func referenceDigest(t *testing.T, root []byte, info, purpose string, payload []byte) [32]byte {
	t.Helper()
	reader := hkdf.New(sha256.New, root, nil, []byte(info))
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		t.Fatalf("reference HKDF: %v", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	var out [32]byte
	copy(out[:], mac.Sum(nil))
	return out
}

func referenceDigestWithoutNUL(t *testing.T, root []byte, info, purpose string, payload []byte) [32]byte {
	t.Helper()
	reader := hkdf.New(sha256.New, root, nil, []byte(info))
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		t.Fatalf("reference HKDF: %v", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write(payload)
	var out [32]byte
	copy(out[:], mac.Sum(nil))
	return out
}
