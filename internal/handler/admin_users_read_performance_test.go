package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

// This is opt-in because it creates 100k owned synthetic rows. The coordinator
// must authorize this workload in a disposable, already migrated fixture.
func TestAdminUsersReadPerformance(t *testing.T) {
	if os.Getenv("B1D_RUN_PERFORMANCE") != "1" {
		t.Skip("NOT_RUN: opt-in 100k synthetic-user performance fixture requires this batch authorization")
	}
	state := adminAuthzHTTPState(t)
	root := adminAuthzHTTPUser(t, state, models.UserRoleRoot)
	access := platformJWT(t, state, root)
	now := time.Now().UTC().UnixMilli()
	groupID := platformTestDefaultBusinessGroupID(t, state)
	for batch := 0; batch < 200; batch++ {
		users := make([]models.User, 500)
		for i := range users {
			guid := platformTestSnowflake.Next()
			// Base36 preserves task-row uniqueness while fitting the production
			// users.username VARCHAR(20) constraint in the real MySQL fixture.
			username := "p" + strconv.FormatInt(guid, 36)
			users[i] = models.User{AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, UpdatedAt: now}, GroupID: groupID, Username: &username, PlanType: models.PlanFree, Status: models.UserStatusActive, Role: models.UserRoleUser, AuthVersion: 1, AllowedModels: models.JSONSlice{}}
		}
		if err := state.DB.CreateInBatches(users, 500).Error; err != nil {
			t.Fatal("seed task-owned synthetic users failed")
		}
	}
	analyzeStarted := time.Now()
	if err := state.DB.Exec("ANALYZE TABLE users").Error; err != nil {
		t.Fatal("analyze seeded users failed")
	}
	analyzeElapsed := time.Since(analyzeStarted)
	r := gin.New()
	RegisterAdminUsersRead(r, state)
	measure := func() (time.Duration, int, bool) {
		req := httptest.NewRequest(http.MethodGet, "/admin/v2/users?page_size=20", nil)
		req.Header.Set("Authorization", "Bearer "+access)
		rec := httptest.NewRecorder()
		start := time.Now()
		r.ServeHTTP(rec, req)
		elapsed := time.Since(start)
		// Decode only after stopping the server timer; never log response/identity.
		valid := validAdminUsersPerformancePage(rec.Body.Bytes())
		return elapsed, rec.Code, valid
	}
	first, status, valid := measure()
	if status != 200 || !valid {
		t.Fatal("first read failed status or page contract")
	}
	const concurrency = 10
	const perWorker = 20
	type sample struct {
		duration time.Duration
		status   int
		valid    bool
	}
	samples := make(chan sample, concurrency*perWorker)
	for worker := 0; worker < concurrency; worker++ {
		go func() {
			for i := 0; i < perWorker; i++ {
				d, s, valid := measure()
				samples <- sample{d, s, valid}
			}
		}()
	}
	durations := make([]time.Duration, 0, concurrency*perWorker)
	for i := 0; i < concurrency*perWorker; i++ {
		s := <-samples
		if s.status != 200 || !s.valid {
			t.Errorf("performance request invalid status/page contract: status=%d", s.status)
		}
		durations = append(durations, s.duration)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[(len(durations)*95+99)/100-1]
	t.Logf("synthetic_users=100000 concurrency=10 page_size=20 measured_requests=%d analyze_ms=%.3f first_application_read_ms=%.3f warm_p95_ms=%.3f os=%s arch=%s logical_cpu=%d cache=database_warmed_by_seeding_and_analyze_first_application_read_not_disk_cold", len(durations), float64(analyzeElapsed.Microseconds())/1000, float64(first.Microseconds())/1000, float64(p95.Microseconds())/1000, runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	if p95 > 500*time.Millisecond {
		t.Fatalf("server-side warm P95 %s exceeds 500ms", p95)
	}
}

func TestAdminUsersReadPerformanceResponseValidation(t *testing.T) {
	items := make([]map[string]string, 20)
	for i := range items {
		items[i] = map[string]string{"guid": fmt.Sprint(i + 1)}
	}
	payload := map[string]interface{}{"items": items, "total": 100000, "page": 1, "page_size": 20}
	body, _ := json.Marshal(payload)
	if !validAdminUsersPerformancePage(body) {
		t.Fatal("valid measurement page rejected")
	}
	for _, field := range []string{"items", "total", "page", "page_size", "guid"} {
		invalid := map[string]interface{}{"items": items, "total": 100000, "page": 1, "page_size": 20}
		switch field {
		case "items":
			invalid[field] = items[:0]
		case "total":
			invalid[field] = 99999
		case "page":
			invalid[field] = 2
		case "page_size":
			invalid[field] = 50
		case "guid":
			badItems := append([]map[string]string(nil), items...)
			badItems[0] = map[string]string{"guid": "01"}
			invalid["items"] = badItems
		}
		body, _ := json.Marshal(invalid)
		if validAdminUsersPerformancePage(body) {
			t.Fatalf("invalid %s accepted", field)
		}
	}
}

func validAdminUsersPerformancePage(body []byte) bool {
	var page struct {
		Items []struct {
			GUID string `json:"guid"`
		} `json:"items"`
		Total    int64 `json:"total"`
		Page     int64 `json:"page"`
		PageSize int   `json:"page_size"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return false
	}
	if len(page.Items) != 20 || page.Total < 100000 || page.Page != 1 || page.PageSize != 20 {
		return false
	}
	for _, item := range page.Items {
		if _, err := service.ParseAdminPermissionGUID(item.GUID); err != nil {
			return false
		}
	}
	return true
}
