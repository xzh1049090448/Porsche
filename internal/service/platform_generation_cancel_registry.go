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
}

func NewPlatformGenerationCancellationRegistry() *PlatformGenerationCancellationRegistry {
	return newPlatformGenerationCancellationRegistryFrom(rand.Reader)
}

func newPlatformGenerationCancellationRegistryFrom(reader io.Reader) *PlatformGenerationCancellationRegistry {
	return &PlatformGenerationCancellationRegistry{
		entries: make(map[platformGenerationCancellationKey]platformGenerationCancellationEntry),
		reader:  reader,
	}
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
	if r.entries == nil {
		r.entries = make(map[platformGenerationCancellationKey]platformGenerationCancellationEntry)
	}
	if _, exists := r.entries[key]; exists {
		return "", ErrPlatformGenerationConflict
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
	if r == nil || validatePlatformGenerationIdentity(userID, generationID) != nil || !validPlatformGenerationLeaseToken(token) {
		return false
	}

	key := platformGenerationCancellationKey{UserID: userID, GenerationID: generationID}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, exists := r.entries[key]
	if !exists || entry.token != token {
		return false
	}
	delete(r.entries, key)
	return true
}
