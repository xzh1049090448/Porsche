package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/models"
)

// Exercise the network-facing boundary, not just the service return value:
// replay must produce the normal 401 envelope, not a recovered panic/500.
func TestRefreshReplayHTTPRejectsAfterCommittedRevocation(t *testing.T) {
	state := authHTTPTestState(t)
	t.Cleanup(func() { _ = state.AuthRedis.Close() })
	sqlDB, err := state.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	engine := gin.New()
	engine.Use(gin.RecoveryWithWriter(io.Discard))
	RegisterAuth(engine, state)
	server := httptest.NewTLSServer(engine)
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second

	request := func(method, path, body string, cookie *http.Cookie, access string) (int, []byte, []*http.Cookie) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://app.example.test")
		if cookie != nil {
			req.AddCookie(cookie)
			req.Header.Set("X-Auth-Session", refreshSID(t, cookie))
		}
		if access != "" {
			req.Header.Set("Authorization", "Bearer "+access)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, payload, response.Cookies()
	}
	username := fmt.Sprintf("u%019d", platformTestSnowflake.Next())
	body := fmt.Sprintf(`{"username":%q,"password":"Str0ng!Pass1"}`, username)
	if status, _, _ := request(http.MethodPost, "/api/v1/auth/register", body, nil, ""); status != http.StatusCreated {
		t.Fatalf("registration status=%d", status)
	}
	status, _, cookies := request(http.MethodPost, "/api/v1/auth/login", body, nil, "")
	if status != http.StatusOK || len(cookies) != 1 {
		t.Fatalf("login status=%d cookies=%d", status, len(cookies))
	}
	oldCookie := cookies[0]
	status, payload, cookies := request(http.MethodPost, "/api/v1/auth/refresh", "", oldCookie, "")
	if status != http.StatusOK || len(cookies) != 1 {
		t.Fatalf("rotation status=%d cookies=%d", status, len(cookies))
	}
	access := authAccessToken(t, payload)
	newCookie := cookies[0]
	var session models.Session
	if err := state.DB.Where("sid = ? AND is_deleted = 0", refreshSID(t, oldCookie)).First(&session).Error; err != nil {
		t.Fatal(err)
	}
	// Move only this fixture's grace deadline into the past; no wall-clock sleep.
	if err := state.DB.Model(&models.Session{}).Where("id = ?", session.ID).
		Update("previous_refresh_expires_at", time.Now().UnixMilli()-1).Error; err != nil {
		t.Fatal(err)
	}
	for _, cookie := range []*http.Cookie{oldCookie, oldCookie, newCookie} {
		status, payload, cookies := request(http.MethodPost, "/api/v1/auth/refresh", "", cookie, "")
		if status != http.StatusUnauthorized || len(cookies) != 0 {
			t.Fatalf("replay status=%d cookies=%d; want 401 with no issued cookie", status, len(cookies))
		}
		var envelope struct {
			Error struct {
				Code, Message, Type string
				RequestID           string `json:"request_id"`
			} `json:"error"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Error.Code != "auth_request_failed" || envelope.Error.Type != "authentication_error" || envelope.Error.Message == "" || envelope.Error.RequestID == "" {
			t.Fatalf("invalid replay error envelope: %s", payload)
		}
		assertAuthResponseHasNoSecrets(t, payload)
	}
	if err := state.DB.First(&session, session.ID).Error; err != nil || session.RevokedAt == nil {
		t.Fatalf("revocation not committed: err=%v", err)
	}
	var count int64
	if err := state.DB.Model(&models.AuthAuditEvent{}).Where("session_guid = ? AND event_type = ? AND is_deleted = 0", session.Guid, models.AuthAuditEventReplayRevoked).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("replay audit count=%d err=%v; want 1", count, err)
	}
	if revoked, err := state.AuthRedis.IsSessionRevoked(context.Background(), session.SID); err != nil || !revoked {
		t.Fatalf("Redis denial barrier=%t err=%v", revoked, err)
	}
	if status, _, _ := request(http.MethodGet, "/api/v1/auth/self", "", nil, access); status != http.StatusUnauthorized {
		t.Fatalf("revoked access status=%d; want 401", status)
	}
	t.Log("HTTPS: register=201 login=200 rotate=200 replay=401 repeat=401 current-refresh=401 self=401; committed revocation, one audit, Redis barrier verified")
}
