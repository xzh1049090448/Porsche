package persistence

import "time"

// Clock supplies UTC Unix milliseconds for persistence identifiers.
type Clock interface {
	NowMillis() int64
}

type utcClock struct{}

// NowMillis returns the current UTC Unix time in milliseconds.
func (utcClock) NowMillis() int64 { return time.Now().UTC().UnixMilli() }

// SystemClock returns the production UTC clock.
func SystemClock() Clock { return utcClock{} }
