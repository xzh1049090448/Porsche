package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"sync"
)

const platformGenerationCancellationRegistrationTokenBytes = 32

type platformGenerationCancellationKey struct {
	UserID       int64
	GenerationID string
}

type platformGenerationCancellationEntry struct {
	token   string
	cancel  context.CancelFunc
	invoked bool
}

// PlatformGenerationCancellationRegistry keeps cancellation callbacks local to
// this process. Its registrations are deliberately independent from the
// durable generation lifecycle record.
type PlatformGenerationCancellationRegistry struct {
	mu        sync.Mutex
	entropyMu sync.Mutex
	entries   map[platformGenerationCancellationKey]platformGenerationCancellationEntry
	reader    io.Reader
	closed    bool
	drained   chan struct{}
}

func NewPlatformGenerationCancellationRegistry() *PlatformGenerationCancellationRegistry {
	return newPlatformGenerationCancellationRegistryFrom(rand.Reader)
}

func newPlatformGenerationCancellationRegistryFrom(reader io.Reader) *PlatformGenerationCancellationRegistry {
	return &PlatformGenerationCancellationRegistry{
		entries: make(map[platformGenerationCancellationKey]platformGenerationCancellationEntry),
		reader:  reader,
		drained: closedPlatformGenerationDrain(),
	}
}

func closedPlatformGenerationDrain() chan struct{} {
	drained := make(chan struct{})
	close(drained)
	return drained
}

func (r *PlatformGenerationCancellationRegistry) drainLocked() chan struct{} {
	if r.drained == nil {
		r.drained = closedPlatformGenerationDrain()
	}
	return r.drained
}

func newPlatformGenerationCancellationRegistrationToken() (string, error) {
	return newPlatformGenerationCancellationRegistrationTokenFrom(rand.Reader)
}

func newPlatformGenerationCancellationRegistrationTokenFrom(reader io.Reader) (string, error) {
	if reader == nil {
		return "", ErrPlatformGenerationUnavailable
	}
	raw := make([]byte, platformGenerationCancellationRegistrationTokenBytes)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return "", ErrPlatformGenerationUnavailable
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func validPlatformGenerationCancellationRegistrationToken(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(raw) == platformGenerationCancellationRegistrationTokenBytes && base64.RawURLEncoding.EncodeToString(raw) == token
}

func (r *PlatformGenerationCancellationRegistry) newRegistrationToken() (string, error) {
	r.entropyMu.Lock()
	defer r.entropyMu.Unlock()
	if r.reader == nil {
		return newPlatformGenerationCancellationRegistrationToken()
	}
	return newPlatformGenerationCancellationRegistrationTokenFrom(r.reader)
}

func (r *PlatformGenerationCancellationRegistry) Register(userID int64, generationID string, cancel context.CancelFunc) (string, error) {
	if validatePlatformGenerationIdentity(userID, generationID) != nil || cancel == nil {
		return "", ErrPlatformGenerationInvalid
	}
	if r == nil {
		return "", ErrPlatformGenerationUnavailable
	}

	key := platformGenerationCancellationKey{UserID: userID, GenerationID: generationID}
	r.mu.Lock()
	r.drainLocked()
	if r.closed {
		r.mu.Unlock()
		return "", ErrPlatformGenerationUnavailable
	}
	_, exists := r.entries[key]
	r.mu.Unlock()
	if exists {
		return "", ErrPlatformGenerationConflict
	}

	token, err := r.newRegistrationToken()
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drainLocked()
	if r.closed {
		return "", ErrPlatformGenerationUnavailable
	}
	if r.entries == nil {
		r.entries = make(map[platformGenerationCancellationKey]platformGenerationCancellationEntry)
	}
	if _, exists := r.entries[key]; exists {
		return "", ErrPlatformGenerationConflict
	}
	if len(r.entries) == 0 {
		r.drained = make(chan struct{})
	}
	r.entries[key] = platformGenerationCancellationEntry{token: token, cancel: cancel}
	return token, nil
}

func (r *PlatformGenerationCancellationRegistry) Cancel(userID int64, generationID string) bool {
	if r == nil || validatePlatformGenerationIdentity(userID, generationID) != nil {
		return false
	}

	key := platformGenerationCancellationKey{UserID: userID, GenerationID: generationID}
	r.mu.Lock()
	r.drainLocked()
	entry, exists := r.entries[key]
	if !exists || entry.invoked {
		r.mu.Unlock()
		return false
	}
	entry.invoked = true
	r.entries[key] = entry
	cancel := entry.cancel
	r.mu.Unlock()

	cancel()
	return true
}

func (r *PlatformGenerationCancellationRegistry) Unregister(userID int64, generationID, token string) bool {
	if r == nil || validatePlatformGenerationIdentity(userID, generationID) != nil || !validPlatformGenerationCancellationRegistrationToken(token) {
		return false
	}

	key := platformGenerationCancellationKey{UserID: userID, GenerationID: generationID}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drainLocked()
	entry, exists := r.entries[key]
	if !exists || entry.token != token {
		return false
	}
	delete(r.entries, key)
	if len(r.entries) == 0 {
		close(r.drained)
	}
	return true
}

func (r *PlatformGenerationCancellationRegistry) CloseAndWait(ctx context.Context) error {
	if r == nil || ctx == nil {
		return ErrPlatformGenerationUnavailable
	}

	r.mu.Lock()
	r.closed = true
	cancels := make([]context.CancelFunc, 0, len(r.entries))
	for key, entry := range r.entries {
		if !entry.invoked {
			entry.invoked = true
			r.entries[key] = entry
			cancels = append(cancels, entry.cancel)
		}
	}
	drained := r.drainLocked()
	r.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
