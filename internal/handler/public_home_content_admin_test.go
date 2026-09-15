package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

type publicHomeAdminStub struct {
	draft      *service.PublicHomeDraft
	documents  *service.PublicHomeDocumentsDraft
	homeConfig *service.PublicHomeConfig
	err        error
}

func (s *publicHomeAdminStub) result() (*service.PublicHomeDraft, error) { return s.draft, s.err }
func (s *publicHomeAdminStub) GetHomeDraft(context.Context, int64) (*service.PublicHomeDraft, error) {
	return s.result()
}
func (s *publicHomeAdminStub) CreateAnnouncement(context.Context, int64, service.AnnouncementCreateRequest) (*service.PublicHomeDraft, error) {
	return s.result()
}
func (s *publicHomeAdminStub) UpdateAnnouncement(context.Context, int64, string, service.AnnouncementUpdateRequest) (*service.PublicHomeDraft, error) {
	return s.result()
}
func (s *publicHomeAdminStub) DeleteAnnouncement(context.Context, int64, string, service.AnnouncementDeleteRequest) (int64, error) {
	return 8, s.err
}
func (s *publicHomeAdminStub) CreateFAQ(context.Context, int64, service.FAQCreateRequest) (*service.PublicHomeDraft, error) {
	return s.result()
}
func (s *publicHomeAdminStub) UpdateFAQ(context.Context, int64, string, service.FAQUpdateRequest) (*service.PublicHomeDraft, error) {
	return s.result()
}
func (s *publicHomeAdminStub) DeleteFAQ(context.Context, int64, string, service.FAQDeleteRequest) (int64, error) {
	return 8, s.err
}
func (s *publicHomeAdminStub) ReplaceFeaturedModels(context.Context, int64, service.FeaturedModelsSaveRequest) (*service.PublicHomeDraft, error) {
	return s.result()
}
func (s *publicHomeAdminStub) GetDocumentsDraft(context.Context, int64) (*service.PublicHomeDocumentsDraft, error) {
	return s.documents, s.err
}
func (s *publicHomeAdminStub) SaveDocumentsDraft(context.Context, int64, service.DocumentsDraftSaveRequest) (*service.PublicHomeDocumentsDraft, error) {
	return s.documents, s.err
}
func (s *publicHomeAdminStub) GetReleaseHomeConfig(context.Context, int64) (*service.PublicHomeConfig, error) {
	return s.homeConfig, s.err
}

func publicHomeAdminTestEngine(stub *publicHomeAdminStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/admin/v2/public-content", gatewayRequestID(), publicAdminNoStore, publicAdminHeaderBoundary)
	g.Use(func(c *gin.Context) {
		c.Set("user", &models.User{ID: 41, Role: models.UserRoleRoot})
		c.Next()
	})
	registerPublicHomeContentAdminRoutes(g, stub)
	return r
}

func TestPublicHomeAdminRoutesMapSuccessfulRequests(t *testing.T) {
	effective := "2026-09-15T00:00:00Z"
	stub := &publicHomeAdminStub{
		draft:      &service.PublicHomeDraft{Revision: 8, Announcements: []service.PublicHomeAnnouncementDraft{}, FAQs: []service.PublicHomeFAQDraft{}, FeaturedModelKeys: []string{}},
		documents:  &service.PublicHomeDocumentsDraft{Revision: 8, About: "about", Terms: "terms", Privacy: "privacy", LegalReviewed: true},
		homeConfig: &service.PublicHomeConfig{Announcements: []service.PublicHomeConfigAnnouncement{}, FAQs: []service.PublicHomeConfigFAQ{}, FeaturedModelKeys: []string{}, ContentReleaseVersion: 7, PriceReleaseVersion: 9},
	}
	r := publicHomeAdminTestEngine(stub)
	cases := []struct {
		method, path, body string
		status             int
	}{
		{http.MethodGet, "/admin/v2/public-content/home-draft", "", 200},
		{http.MethodPost, "/admin/v2/public-content/home-draft/announcements", `{"expected_revision":7,"title":"notice","body_markdown":"body","effective_at":"` + effective + `","is_visible":true,"sort_order":1}`, 201},
		{http.MethodPatch, "/admin/v2/public-content/home-draft/announcements/353589505447432192", `{"expected_revision":7,"effective_at":null}`, 200},
		{http.MethodDelete, "/admin/v2/public-content/home-draft/announcements/353589505447432192", `{"expected_revision":7}`, 204},
		{http.MethodPost, "/admin/v2/public-content/home-draft/faqs", `{"expected_revision":7,"question":"q","answer_markdown":"a","is_visible":true,"sort_order":1}`, 201},
		{http.MethodPatch, "/admin/v2/public-content/home-draft/faqs/353589501827747840", `{"expected_revision":7,"question":"q2"}`, 200},
		{http.MethodDelete, "/admin/v2/public-content/home-draft/faqs/353589501827747840", `{"expected_revision":7}`, 204},
		{http.MethodPut, "/admin/v2/public-content/home-draft/featured-models", `{"expected_revision":7,"featured_model_keys":["alpha-chat"]}`, 200},
		{http.MethodGet, "/admin/v2/public-content/home-preview?revision=8", "", 200},
		{http.MethodGet, "/admin/v2/public-content/releases/353589500000000001/home-config", "", 200},
		{http.MethodGet, "/admin/v2/public-content/documents-draft", "", 200},
		{http.MethodPut, "/admin/v2/public-content/documents-draft", `{"expected_revision":7,"about":"about","terms":"terms","privacy":"privacy","legal_reviewed":true}`, 200},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != tc.status || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
			t.Fatalf("%s %s status=%d headers=%#v body=%s", tc.method, tc.path, rec.Code, rec.Header(), rec.Body.String())
		}
		if strings.Contains(tc.path, "home-preview") && rec.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
			t.Fatalf("preview headers=%#v", rec.Header())
		}
		if tc.method == http.MethodDelete && (rec.Header().Get("X-Content-Draft-Revision") != "8" || rec.Body.Len() != 0) {
			t.Fatalf("delete headers/body=%#v/%q", rec.Header(), rec.Body.String())
		}
	}
}

func TestPublicHomeAdminRoutesRejectMalformedRequestsAndMapSafeErrors(t *testing.T) {
	stub := &publicHomeAdminStub{draft: &service.PublicHomeDraft{Revision: 8, Announcements: []service.PublicHomeAnnouncementDraft{}, FAQs: []service.PublicHomeFAQDraft{}, FeaturedModelKeys: []string{}}}
	r := publicHomeAdminTestEngine(stub)
	bad := []struct{ method, path, body string }{
		{http.MethodPost, "/admin/v2/public-content/home-draft/announcements", `{"expected_revision":7,"title":"x","body_markdown":"x","is_visible":true,"sort_order":0}`},
		{http.MethodPost, "/admin/v2/public-content/home-draft/announcements", `{"expected_revision":7,"title":"x","body_markdown":"x","effective_at":"2026-09-15T00:00:00+00:00","is_visible":true,"sort_order":0}`},
		{http.MethodPatch, "/admin/v2/public-content/home-draft/announcements/01", `{"expected_revision":7}`},
		{http.MethodPatch, "/admin/v2/public-content/home-draft/faqs/2", `{"expected_revision":7,"unknown":true}`},
		{http.MethodGet, "/admin/v2/public-content/home-preview?revision=01", ""},
		{http.MethodGet, "/admin/v2/public-content/documents-draft?x=1", ""},
	}
	for _, tc := range bad {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != 400 || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" || !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("%s %s status=%d headers=%#v body=%s", tc.method, tc.path, rec.Code, rec.Header(), rec.Body.String())
		}
	}
	for _, status := range []int{404, 409, 422, 503} {
		stub.err = &service.HTTPError{Status: status, Message: "safe failure"}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/v2/public-content/home-draft", nil))
		if rec.Code != status || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), `"request_id"`) || strings.Contains(rec.Body.String(), "body_markdown") {
			t.Fatalf("status=%d response=%d headers=%#v body=%s", status, rec.Code, rec.Header(), rec.Body.String())
		}
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.Error.Code == "" {
			t.Fatalf("status=%d envelope=%s", status, rec.Body.String())
		}
	}
}

func TestPublicHomeAdminPreviewRevisionConflictIsSafe(t *testing.T) {
	stub := &publicHomeAdminStub{draft: &service.PublicHomeDraft{Revision: 8, Announcements: []service.PublicHomeAnnouncementDraft{}, FAQs: []service.PublicHomeFAQDraft{}, FeaturedModelKeys: []string{}}}
	rec := httptest.NewRecorder()
	publicHomeAdminTestEngine(stub).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/v2/public-content/home-preview?revision=7", nil))
	if rec.Code != http.StatusConflict || rec.Header().Get("X-Robots-Tag") != "" || strings.Contains(rec.Body.String(), "body_markdown") || strings.Contains(rec.Body.String(), "featured_model_keys") {
		t.Fatalf("status=%d headers=%#v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
}

func TestPublicHomeAdminRoutesRequireRootSession(t *testing.T) {
	state := adminAuthzHTTPState(t)
	admin := adminAuthzHTTPUser(t, state, models.UserRoleAdmin)
	engine := gin.New()
	RegisterPublicContentAdmin(engine, &app.State{Settings: state.Settings, DB: state.DB, Sessions: state.Sessions})

	for _, tc := range []struct {
		name  string
		token string
		want  int
		code  string
	}{
		{name: "anonymous", want: http.StatusUnauthorized, code: "authentication_required"},
		{name: "admin", token: platformJWT(t, state, admin), want: http.StatusForbidden, code: "root_role_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/admin/v2/public-content/home-draft", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)
			if rec.Code != tc.want || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("status=%d headers=%#v body=%s", rec.Code, rec.Header(), rec.Body.String())
			}
		})
	}
}
