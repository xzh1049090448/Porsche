package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/service"
)

type publicHomeContentAdminService interface {
	GetHomeDraft(context.Context, int64) (*service.PublicHomeDraft, error)
	CreateAnnouncement(context.Context, int64, service.AnnouncementCreateRequest) (*service.PublicHomeDraft, error)
	UpdateAnnouncement(context.Context, int64, string, service.AnnouncementUpdateRequest) (*service.PublicHomeDraft, error)
	DeleteAnnouncement(context.Context, int64, string, service.AnnouncementDeleteRequest) (int64, error)
	CreateFAQ(context.Context, int64, service.FAQCreateRequest) (*service.PublicHomeDraft, error)
	UpdateFAQ(context.Context, int64, string, service.FAQUpdateRequest) (*service.PublicHomeDraft, error)
	DeleteFAQ(context.Context, int64, string, service.FAQDeleteRequest) (int64, error)
	ReplaceFeaturedModels(context.Context, int64, service.FeaturedModelsSaveRequest) (*service.PublicHomeDraft, error)
	GetDocumentsDraft(context.Context, int64) (*service.PublicHomeDocumentsDraft, error)
	SaveDocumentsDraft(context.Context, int64, service.DocumentsDraftSaveRequest) (*service.PublicHomeDocumentsDraft, error)
	GetReleaseHomeConfig(context.Context, int64) (*service.PublicHomeConfig, error)
}

type publicHomeOptional[T any] struct {
	Set   bool
	Value T
}

func (o *publicHomeOptional[T]) UnmarshalJSON(raw []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("null is not allowed")
	}
	return json.Unmarshal(raw, &o.Value)
}

type publicHomeNullableTime struct {
	Set   bool
	Value *int64
}

func (v *publicHomeNullableTime) UnmarshalJSON(raw []byte) error {
	v.Set = true
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		v.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.UTC().Format(time.RFC3339) != value {
		return fmt.Errorf("invalid canonical UTC time")
	}
	millis := parsed.UTC().UnixMilli()
	v.Value = &millis
	return nil
}

type publicHomeAnnouncementCreateBody struct {
	ExpectedRevision publicHomeOptional[int64]  `json:"expected_revision"`
	Title            publicHomeOptional[string] `json:"title"`
	BodyMarkdown     publicHomeOptional[string] `json:"body_markdown"`
	EffectiveAt      publicHomeNullableTime     `json:"effective_at"`
	IsVisible        publicHomeOptional[bool]   `json:"is_visible"`
	SortOrder        publicHomeOptional[int]    `json:"sort_order"`
}

type publicHomeAnnouncementUpdateBody struct {
	ExpectedRevision publicHomeOptional[int64]  `json:"expected_revision"`
	Title            publicHomeOptional[string] `json:"title"`
	BodyMarkdown     publicHomeOptional[string] `json:"body_markdown"`
	EffectiveAt      publicHomeNullableTime     `json:"effective_at"`
	IsVisible        publicHomeOptional[bool]   `json:"is_visible"`
	SortOrder        publicHomeOptional[int]    `json:"sort_order"`
}

type publicHomeFAQCreateBody struct {
	ExpectedRevision publicHomeOptional[int64]  `json:"expected_revision"`
	Question         publicHomeOptional[string] `json:"question"`
	AnswerMarkdown   publicHomeOptional[string] `json:"answer_markdown"`
	IsVisible        publicHomeOptional[bool]   `json:"is_visible"`
	SortOrder        publicHomeOptional[int]    `json:"sort_order"`
}

type publicHomeFAQUpdateBody struct {
	ExpectedRevision publicHomeOptional[int64]  `json:"expected_revision"`
	Question         publicHomeOptional[string] `json:"question"`
	AnswerMarkdown   publicHomeOptional[string] `json:"answer_markdown"`
	IsVisible        publicHomeOptional[bool]   `json:"is_visible"`
	SortOrder        publicHomeOptional[int]    `json:"sort_order"`
}

type publicHomeRevisionBody struct {
	ExpectedRevision publicHomeOptional[int64] `json:"expected_revision"`
}

type publicHomeFeaturedModelsBody struct {
	ExpectedRevision  publicHomeOptional[int64]    `json:"expected_revision"`
	FeaturedModelKeys publicHomeOptional[[]string] `json:"featured_model_keys"`
}

type publicHomeDocumentsBody struct {
	ExpectedRevision publicHomeOptional[int64]  `json:"expected_revision"`
	About            publicHomeOptional[string] `json:"about"`
	Terms            publicHomeOptional[string] `json:"terms"`
	Privacy          publicHomeOptional[string] `json:"privacy"`
	LegalReviewed    publicHomeOptional[bool]   `json:"legal_reviewed"`
}

func registerPublicHomeContentAdminRoutes(g *gin.RouterGroup, admin publicHomeContentAdminService) {
	unavailable := func(c *gin.Context) bool {
		if admin != nil {
			return false
		}
		publicAdminError(c, &service.HTTPError{Status: http.StatusServiceUnavailable, Message: "public administration unavailable"})
		return true
	}
	noBody := func(c *gin.Context) bool {
		if c.Request.URL.RawPath != "" || c.Request.URL.RawQuery != "" || !publicAdminRequestHasNoBody(c.Request) {
			publicAdminError(c, &service.HTTPError{Status: http.StatusBadRequest, Message: "invalid request"})
			return false
		}
		return true
	}
	write := func(c *gin.Context, status int, out any, err error) {
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(status, out)
	}

	g.GET("/home-draft", func(c *gin.Context) {
		if unavailable(c) || !noBody(c) {
			return
		}
		out, err := admin.GetHomeDraft(c.Request.Context(), publicAdminActorID(c))
		write(c, http.StatusOK, out, err)
	})
	g.POST("/home-draft/announcements", func(c *gin.Context) {
		if unavailable(c) {
			return
		}
		var body publicHomeAnnouncementCreateBody
		if !publicAdminStrictJSON(c, &body) || !body.ExpectedRevision.Set || !body.Title.Set || !body.BodyMarkdown.Set || !body.EffectiveAt.Set || !body.IsVisible.Set || !body.SortOrder.Set {
			publicAdminError(c, &service.HTTPError{Status: http.StatusBadRequest, Message: "invalid request"})
			return
		}
		out, err := admin.CreateAnnouncement(c.Request.Context(), publicAdminActorID(c), service.AnnouncementCreateRequest{ExpectedRevision: body.ExpectedRevision.Value, Title: body.Title.Value, BodyMarkdown: body.BodyMarkdown.Value, EffectiveAt: body.EffectiveAt.Value, IsVisible: body.IsVisible.Value, SortOrder: body.SortOrder.Value})
		write(c, http.StatusCreated, out, err)
	})
	g.PATCH("/home-draft/announcements/:guid", func(c *gin.Context) {
		if unavailable(c) {
			return
		}
		guid, ok := publicAdminGUID(c.Param("guid"))
		var body publicHomeAnnouncementUpdateBody
		if !ok || !publicAdminStrictJSON(c, &body) || !body.ExpectedRevision.Set {
			publicAdminError(c, &service.HTTPError{Status: http.StatusBadRequest, Message: "invalid request"})
			return
		}
		in := service.AnnouncementUpdateRequest{ExpectedRevision: body.ExpectedRevision.Value, EffectiveAt: service.OptionalNullableUnixMillis{Set: body.EffectiveAt.Set, Value: body.EffectiveAt.Value}}
		if body.Title.Set {
			in.Title = &body.Title.Value
		}
		if body.BodyMarkdown.Set {
			in.BodyMarkdown = &body.BodyMarkdown.Value
		}
		if body.IsVisible.Set {
			in.IsVisible = &body.IsVisible.Value
		}
		if body.SortOrder.Set {
			in.SortOrder = &body.SortOrder.Value
		}
		out, err := admin.UpdateAnnouncement(c.Request.Context(), publicAdminActorID(c), strconv.FormatInt(guid, 10), in)
		write(c, http.StatusOK, out, err)
	})
	deleteAnnouncement := func(c *gin.Context) {
		if unavailable(c) {
			return
		}
		guid, ok := publicAdminGUID(c.Param("guid"))
		var body publicHomeRevisionBody
		if !ok || !publicAdminStrictJSON(c, &body) || !body.ExpectedRevision.Set {
			publicAdminError(c, &service.HTTPError{Status: http.StatusBadRequest, Message: "invalid request"})
			return
		}
		revision, err := admin.DeleteAnnouncement(c.Request.Context(), publicAdminActorID(c), strconv.FormatInt(guid, 10), service.AnnouncementDeleteRequest{ExpectedRevision: body.ExpectedRevision.Value})
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.Header("X-Content-Draft-Revision", strconv.FormatInt(revision, 10))
		c.Status(http.StatusNoContent)
	}
	g.DELETE("/home-draft/announcements/:guid", deleteAnnouncement)

	g.POST("/home-draft/faqs", func(c *gin.Context) {
		if unavailable(c) {
			return
		}
		var body publicHomeFAQCreateBody
		if !publicAdminStrictJSON(c, &body) || !body.ExpectedRevision.Set || !body.Question.Set || !body.AnswerMarkdown.Set || !body.IsVisible.Set || !body.SortOrder.Set {
			publicAdminError(c, &service.HTTPError{Status: http.StatusBadRequest, Message: "invalid request"})
			return
		}
		out, err := admin.CreateFAQ(c.Request.Context(), publicAdminActorID(c), service.FAQCreateRequest{ExpectedRevision: body.ExpectedRevision.Value, Question: body.Question.Value, AnswerMarkdown: body.AnswerMarkdown.Value, IsVisible: body.IsVisible.Value, SortOrder: body.SortOrder.Value})
		write(c, http.StatusCreated, out, err)
	})
	g.PATCH("/home-draft/faqs/:guid", func(c *gin.Context) {
		if unavailable(c) {
			return
		}
		guid, ok := publicAdminGUID(c.Param("guid"))
		var body publicHomeFAQUpdateBody
		if !ok || !publicAdminStrictJSON(c, &body) || !body.ExpectedRevision.Set {
			publicAdminError(c, &service.HTTPError{Status: http.StatusBadRequest, Message: "invalid request"})
			return
		}
		in := service.FAQUpdateRequest{ExpectedRevision: body.ExpectedRevision.Value}
		if body.Question.Set {
			in.Question = &body.Question.Value
		}
		if body.AnswerMarkdown.Set {
			in.AnswerMarkdown = &body.AnswerMarkdown.Value
		}
		if body.IsVisible.Set {
			in.IsVisible = &body.IsVisible.Value
		}
		if body.SortOrder.Set {
			in.SortOrder = &body.SortOrder.Value
		}
		out, err := admin.UpdateFAQ(c.Request.Context(), publicAdminActorID(c), strconv.FormatInt(guid, 10), in)
		write(c, http.StatusOK, out, err)
	})
	g.DELETE("/home-draft/faqs/:guid", func(c *gin.Context) {
		if unavailable(c) {
			return
		}
		guid, ok := publicAdminGUID(c.Param("guid"))
		var body publicHomeRevisionBody
		if !ok || !publicAdminStrictJSON(c, &body) || !body.ExpectedRevision.Set {
			publicAdminError(c, &service.HTTPError{Status: http.StatusBadRequest, Message: "invalid request"})
			return
		}
		revision, err := admin.DeleteFAQ(c.Request.Context(), publicAdminActorID(c), strconv.FormatInt(guid, 10), service.FAQDeleteRequest{ExpectedRevision: body.ExpectedRevision.Value})
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.Header("X-Content-Draft-Revision", strconv.FormatInt(revision, 10))
		c.Status(http.StatusNoContent)
	})
	g.PUT("/home-draft/featured-models", func(c *gin.Context) {
		if unavailable(c) {
			return
		}
		var body publicHomeFeaturedModelsBody
		if !publicAdminStrictJSON(c, &body) || !body.ExpectedRevision.Set || !body.FeaturedModelKeys.Set {
			publicAdminError(c, &service.HTTPError{Status: http.StatusBadRequest, Message: "invalid request"})
			return
		}
		out, err := admin.ReplaceFeaturedModels(c.Request.Context(), publicAdminActorID(c), service.FeaturedModelsSaveRequest{ExpectedRevision: body.ExpectedRevision.Value, FeaturedModelKeys: body.FeaturedModelKeys.Value})
		write(c, http.StatusOK, out, err)
	})
	g.GET("/home-preview", func(c *gin.Context) {
		if unavailable(c) || !publicAdminRequestHasNoBody(c.Request) || c.Request.URL.RawPath != "" {
			if !c.IsAborted() {
				publicAdminError(c, &service.HTTPError{Status: http.StatusBadRequest, Message: "invalid request"})
			}
			return
		}
		q, ok := publicAdminQuery(c.Request.URL.RawQuery, map[string]bool{"revision": true})
		if !ok {
			publicAdminError(c, &service.HTTPError{Status: http.StatusBadRequest, Message: "invalid request"})
			return
		}
		draft, err := admin.GetHomeDraft(c.Request.Context(), publicAdminActorID(c))
		if err != nil {
			publicAdminError(c, err)
			return
		}
		if q["revision"] != "" {
			revision, parseErr := strconv.ParseInt(q["revision"], 10, 64)
			if parseErr != nil || revision < 1 || strconv.FormatInt(revision, 10) != q["revision"] {
				publicAdminError(c, &service.HTTPError{Status: http.StatusBadRequest, Message: "invalid revision"})
				return
			}
			if revision != draft.Revision {
				publicAdminError(c, &service.HTTPError{Status: http.StatusConflict, Message: "public content draft revision conflict"})
				return
			}
		}
		c.Header("X-Robots-Tag", "noindex, nofollow")
		c.JSON(http.StatusOK, draft)
	})
	g.GET("/releases/:guid/home-config", func(c *gin.Context) {
		if unavailable(c) {
			return
		}
		guid, ok := publicAdminGUID(c.Param("guid"))
		if !ok || c.Request.URL.RawPath != "" || c.Request.URL.RawQuery != "" || !publicAdminRequestHasNoBody(c.Request) {
			publicAdminError(c, &service.HTTPError{Status: http.StatusBadRequest, Message: "invalid request"})
			return
		}
		out, err := admin.GetReleaseHomeConfig(c.Request.Context(), guid)
		write(c, http.StatusOK, out, err)
	})
	g.GET("/documents-draft", func(c *gin.Context) {
		if unavailable(c) || !noBody(c) {
			return
		}
		out, err := admin.GetDocumentsDraft(c.Request.Context(), publicAdminActorID(c))
		write(c, http.StatusOK, out, err)
	})
	g.PUT("/documents-draft", func(c *gin.Context) {
		if unavailable(c) {
			return
		}
		var body publicHomeDocumentsBody
		if !publicAdminStrictJSON(c, &body) || !body.ExpectedRevision.Set || !body.About.Set || !body.Terms.Set || !body.Privacy.Set || !body.LegalReviewed.Set {
			publicAdminError(c, &service.HTTPError{Status: http.StatusBadRequest, Message: "invalid request"})
			return
		}
		out, err := admin.SaveDocumentsDraft(c.Request.Context(), publicAdminActorID(c), service.DocumentsDraftSaveRequest{ExpectedRevision: body.ExpectedRevision.Value, About: body.About.Value, Terms: body.Terms.Value, Privacy: body.Privacy.Value, LegalReviewed: body.LegalReviewed.Value})
		write(c, http.StatusOK, out, err)
	})
}
