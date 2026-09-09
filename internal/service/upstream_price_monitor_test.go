package service

import (
	"context"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

func TestUpstreamPriceMonitorComparesComponentsIndependently(t *testing.T) {
	in, out := "2.00000000", "4.00000000"
	upIn, upOut := "3.00000000", "4.00000000"
	got := planPriceComparisons("alpha", &in, &out, &whitelabel.CatalogObservedModel{NormalizedID: "up-alpha", InputPriceUSDPerMillionTokens: &upIn, OutputPriceUSDPerMillionTokens: &upOut}, 100)
	if len(got) != 1 || got[0].Component != "input" || got[0].Kind != monitorComparisonBelow {
		t.Fatalf("comparisons=%#v", got)
	}
}

func TestUpstreamPriceMonitorReportsEachNonComparableComponent(t *testing.T) {
	invalid, valid := "invalid", "1.00000000"
	got := planPriceComparisons("alpha", &invalid, nil, &whitelabel.CatalogObservedModel{NormalizedID: "up-alpha", InputPriceUSDPerMillionTokens: &valid}, 100)
	if len(got) != 2 || got[0].Reason != "invalid_current_price" || got[1].Reason != "missing_current_price" {
		t.Fatalf("comparisons=%#v", got)
	}
}

func TestUpstreamPriceMonitorSchedulerTicksEveryFiveMinutesAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan struct{}, 2)
	m := &UpstreamPriceMonitor{interval: 5 * time.Minute, ticker: func(time.Duration) (<-chan time.Time, func()) {
		ch := make(chan time.Time, 2)
		ch <- time.Unix(1, 0)
		ch <- time.Unix(2, 0)
		return ch, func() { close(ticks) }
	}, tick: func(context.Context) error {
		ticks <- struct{}{}
		if len(ticks) == 2 {
			cancel()
		}
		return nil
	}}
	m.Run(ctx)
	if len(ticks) != 2 {
		t.Fatalf("ticks=%d", len(ticks))
	}
}
