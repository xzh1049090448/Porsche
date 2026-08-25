package persistence

import "testing"

func TestSnowflakeProducesUniqueSignedInt64(t *testing.T) {
	generator := NewSnowflake(0, fixedClock{})
	first := generator.Next()
	second := generator.Next()
	if first <= 0 || second <= first {
		t.Fatalf("invalid snowflake IDs: %d, %d", first, second)
	}
}

type fixedClock struct{}

func (fixedClock) NowMillis() int64 { return 1_725_000_000_000 }
