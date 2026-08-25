package handler

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestParseAnalyticsFiltersRejectsInvalidContractValues(t *testing.T) {
	for _, target := range []string{
		"/charts/call_trend?range=30d", "/charts/call_trend?top_n=4", "/charts/call_trend?granularity=3h",
		"/charts/call_trend?start_at=invalid&end_at=2026-08-19T12:00:00Z", "/charts/call_trend?user_id=1",
		"/charts/user_consumption_trend", "/charts/unknown?range=24h",
	} {
		req := httptest.NewRequest("GET", target, nil)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		view := "call_trend"
		if target == "/charts/user_consumption_trend" {
			view = "user_consumption_trend"
		}
		if target == "/charts/unknown?range=24h" {
			view = "unknown"
		}
		if _, err := parseAnalyticsFilters(c, view); err == nil {
			t.Fatalf("%s: expected validation error", target)
		}
	}
}

func TestParseAnalyticsFiltersUsesMillisecondStorageRangeAndGUID(t *testing.T) {
	req := httptest.NewRequest("GET", "/?start_at=2026-08-18T00:00:00Z&end_at=2026-08-18T02:00:00Z&user_guid=9001", nil)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	f, err := parseAnalyticsFilters(c, "user_consumption_trend")
	if err != nil {
		t.Fatal(err)
	}
	if f.StartAtMillis != 1787011200000 || f.EndAtMillis != 1787018400000 || f.UserGUID != 9001 {
		t.Fatalf("analytics filters must use milliseconds and a user GUID: %#v", f)
	}
}

func TestParseAnalyticsFiltersUsesCustomUTCIntervalAndDeduplicatedModels(t *testing.T) {
	req := httptest.NewRequest("GET", "/?range=1h&start_at=2026-08-18T00:00:00%2B08:00&end_at=2026-08-18T02:00:00%2B08:00&models=a,%20b,a%20,,b&top_n=5&granularity=1h", nil)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	f, err := parseAnalyticsFilters(c, "call_trend")
	if err != nil {
		t.Fatal(err)
	}
	if f.StartAtMillis != time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC).UnixMilli() || len(f.Models) != 2 || f.Models[0] != "a" || f.TopN != 5 {
		t.Fatalf("unexpected filters: %#v", f)
	}
}

func TestParseAnalyticsFiltersRejectsExcessiveModelFilters(t *testing.T) {
	tooMany := make([]string, 51)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("model-%d", i)
	}
	for _, raw := range []string{strings.Repeat("x", 129), strings.Join(tooMany, ",")} {
		req := httptest.NewRequest("GET", "/?models="+raw, nil)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		if _, err := parseAnalyticsFilters(c, "call_trend"); err == nil {
			t.Fatalf("models=%q: expected validation error", raw)
		}
	}
}
