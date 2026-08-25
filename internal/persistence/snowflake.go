package persistence

import (
	"sync"
	"time"
)

const (
	snowflakeEpochMillis int64 = 1_704_067_200_000 // 2024-01-01T00:00:00Z
	nodeBits                   = 10
	sequenceBits               = 12
	maxSequence          int64 = (1 << sequenceBits) - 1
)

// Snowflake creates positive, signed 64-bit business identifiers. Node IDs
// use ten bits, so deployments may configure values from 0 through 1023.
type Snowflake struct {
	mu       sync.Mutex
	nodeID   int64
	clock    Clock
	lastMS   int64
	sequence int64
}

var defaultGenerator = NewSnowflake(0, SystemClock())

// ConfigureSnowflake replaces the process-wide generator during application
// startup. Services use this shared implementation instead of inventing IDs.
func ConfigureSnowflake(nodeID int) { defaultGenerator = NewSnowflake(nodeID, SystemClock()) }

// NextGUID returns the next business identifier from the shared generator.
func NextGUID() int64 { return defaultGenerator.Next() }

// NowMillis returns a UTC Unix millisecond value for persisted audit fields.
func NowMillis() int64 { return time.Now().UTC().UnixMilli() }

// NewSnowflake constructs a concurrency-safe generator for a validated node.
func NewSnowflake(nodeID int, clock Clock) *Snowflake {
	if nodeID < 0 || nodeID > (1<<nodeBits)-1 {
		panic("snowflake node ID must be between 0 and 1023")
	}
	if clock == nil {
		clock = SystemClock()
	}
	return &Snowflake{nodeID: int64(nodeID), clock: clock}
}

// Next returns a strictly increasing positive signed int64 identifier.
func (g *Snowflake) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.clock.NowMillis()
	if now < g.lastMS {
		now = g.lastMS
	}
	if now == g.lastMS {
		g.sequence++
		if g.sequence > maxSequence {
			now = g.lastMS + 1
			g.sequence = 0
		}
	} else {
		g.sequence = 0
	}
	g.lastMS = now
	return ((now - snowflakeEpochMillis) << (nodeBits + sequenceBits)) | (g.nodeID << sequenceBits) | g.sequence
}
