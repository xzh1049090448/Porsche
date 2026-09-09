package service

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestUpstreamPriceMonitorLeaseOneOwnerAndExpiry(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	if !f.db.Migrator().HasTable("upstream_monitor_leases") {
		t.Fatal("BLOCKED_FIXTURE: migration missing upstream_monitor_leases")
	}
	now := int64(1_900_000_000_000)
	if err := f.db.Model(&models.UpstreamMonitorLease{}).Where("lease_key=?", "catalog").Updates(map[string]any{"owner_token": nil, "lease_expires_at": 0, "updated_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.db.Model(&models.UpstreamMonitorLease{}).Where("lease_key=?", "catalog").Updates(map[string]any{"owner_token": nil, "lease_expires_at": 0}).Error
	})
	monitors := []*UpstreamPriceMonitor{{db: f.db, now: func() int64 { return now }, random: bytes.NewReader(bytes.Repeat([]byte{1}, 32))}, {db: f.db, now: func() int64 { return now }, random: bytes.NewReader(bytes.Repeat([]byte{2}, 32))}}
	start := make(chan struct{})
	owners := make(chan string, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, m := range monitors {
		wg.Add(1)
		go func(m *UpstreamPriceMonitor) {
			defer wg.Done()
			<-start
			o, e := m.acquireLease(context.Background())
			owners <- o
			errs <- e
		}(m)
	}
	close(start)
	wg.Wait()
	close(owners)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	won := 0
	var owner string
	for o := range owners {
		if o != "" {
			won++
			owner = o
		}
	}
	if won != 1 {
		t.Fatalf("owners won=%d", won)
	}
	now += upstreamMonitorLeaseDuration.Milliseconds() + 1
	replacement, err := monitors[1].acquireLease(context.Background())
	if err != nil || replacement == "" || replacement == owner {
		t.Fatalf("replacement=%q err=%v", replacement, err)
	}
	monitors[0].releaseLease(owner)
	var row models.UpstreamMonitorLease
	if err = f.db.Where("lease_key=?", "catalog").First(&row).Error; err != nil || row.OwnerToken == nil || *row.OwnerToken != replacement {
		t.Fatalf("row=%#v err=%v", row, err)
	}
}
