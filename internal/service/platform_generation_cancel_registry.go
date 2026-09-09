package service

import (
	"context"
	"crypto/rand"
	"io"
	"sync"
)

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
	mu      sync.Mutex
	entries map[platformGenerationCancellationKey]platformGenerationCancellationEntry
	reader  io.Reader
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

func (r *PlatformGenerationCancellationRegistry) Register(userID int64, generationID string, cancel context.CancelFunc) (string, error) {
	if validatePlatformGenerationIdentity(userID, generationID) != nil || cancel == nil {
		return "", ErrPlatformGenerationInvalid
	}
	if r == nil {
		return "", ErrPlatformGenerationUnavailable
	}

	key := platformGenerationCancellationKey{UserID: userID, GenerationID: generationID}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[key]; exists {
		return "", ErrPlatformGenerationConflict
	}
	token, _, err := newPlatformGenerationLeaseFrom(r.reader)
	if err != nil {
		return "", err
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
