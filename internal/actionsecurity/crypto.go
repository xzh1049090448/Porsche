package actionsecurity

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"

	"golang.org/x/crypto/hkdf"
)

const (
	RateVerificationActor   = "rate-verification-actor"
	RateVerificationIP      = "rate-verification-ip"
	RateVerificationSession = "rate-verification-session"
	RateBeginSession        = "rate-begin-session"
)

const (
	ticketInfo      = "porsche/admin-action/ticket/v1"
	intentInfo      = "porsche/admin-action/intent/v1"
	idempotencyInfo = "porsche/admin-action/idempotency/v1"
	leaseInfo       = "porsche/admin-action/lease/v1"
)

type Crypto struct {
	ticket      [32]byte
	intent      [32]byte
	idempotency [32]byte
	lease       [32]byte
}

func NewCrypto(root []byte) (*Crypto, error) {
	if len(root) != 32 {
		return nil, errors.New("invalid action-security root key")
	}
	rootCopy := append([]byte(nil), root...)
	defer clear(rootCopy)

	c := &Crypto{}
	for _, target := range []struct {
		info string
		key  *[32]byte
	}{
		{info: ticketInfo, key: &c.ticket},
		{info: intentInfo, key: &c.intent},
		{info: idempotencyInfo, key: &c.idempotency},
		{info: leaseInfo, key: &c.lease},
	} {
		derived, err := deriveKey(rootCopy, target.info)
		if err != nil {
			return nil, err
		}
		*target.key = derived
		clear(derived[:])
	}
	return c, nil
}

func (c *Crypto) TicketDigest(raw [32]byte) [32]byte {
	return digest(c.ticket, "ticket-value", raw[:])
}

func (c *Crypto) IntentDigest(encoded []byte) [32]byte {
	return digest(c.intent, "intent-v1", encoded)
}

func (c *Crypto) IdempotencyDigest(raw [32]byte) [32]byte {
	return digest(c.idempotency, "idempotency-value", raw[:])
}

func (c *Crypto) LeaseOwnerDigest(raw [32]byte) [32]byte {
	return digest(c.lease, "lease-owner", raw[:])
}

func (c *Crypto) RateDigest(purpose string, payload []byte) ([32]byte, error) {
	switch purpose {
	case RateVerificationActor, RateVerificationIP, RateVerificationSession:
		return digest(c.ticket, purpose, payload), nil
	case RateBeginSession:
		return digest(c.idempotency, purpose, payload), nil
	default:
		return [32]byte{}, errors.New("invalid rate digest purpose")
	}
}

func EqualDigest(a, b [32]byte) bool {
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func deriveKey(root []byte, info string) ([32]byte, error) {
	reader := hkdf.New(sha256.New, root, nil, []byte(info))
	var key [32]byte
	if _, err := io.ReadFull(reader, key[:]); err != nil {
		return [32]byte{}, err
	}
	return key, nil
}

func digest(key [32]byte, purpose string, payload []byte) [32]byte {
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	sum := mac.Sum(nil)
	var out [32]byte
	copy(out[:], sum)
	clear(sum)
	return out
}
