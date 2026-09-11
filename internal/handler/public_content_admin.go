package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/publiccontent"
	"github.com/porsche/ai-gateway-go/internal/service"
)

type publicContentValidationResult struct {
	Issues []publicContentValidationIssue `json:"issues"`
	Valid  bool                           `json:"valid"`
}

type publicContentValidationIssue struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

func publicContentValidationResponse(issues []publiccontent.ValidationIssue) publicContentValidationResult {
	stable := make([]publicContentValidationIssue, len(issues))
	for index, issue := range issues {
		stable[index] = publicContentValidationIssue{Field: issue.Field, Code: issue.Code}
	}
	return publicContentValidationResult{Issues: stable, Valid: len(stable) == 0}
}

func RegisterPublicContentAdmin(r *gin.Engine, state *app.State) {
	g := r.Group("/admin/v2/public-content", gatewayRequestID(), publicAdminNoStore, middleware.RequireRootWithError(state, publicAdminAuthError), publicAdminHeaderBoundary)
	g.GET("/draft", func(c *gin.Context) {
		if c.Request.URL.RawQuery != "" || !publicAdminRequestHasNoBody(c.Request) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		out, err := state.PublicContent.GetDraft(c.Request.Context(), publicAdminActorID(c))
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.PUT("/draft", func(c *gin.Context) {
		var in service.PublicContentDraftSaveRequest
		if !publicAdminStrictJSON(c, &in) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		out, err := state.PublicContent.SaveDraft(c.Request.Context(), publicAdminActorID(c), in)
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.POST("/validate", func(c *gin.Context) {
		var in service.RevisionRequest
		if !publicAdminStrictJSON(c, &in) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		issues, err := state.PublicContent.Validate(c.Request.Context(), publicAdminActorID(c), in.ExpectedRevision)
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(200, publicContentValidationResponse(issues))
	})
	g.GET("/preview", func(c *gin.Context) {
		if !publicAdminRequestHasNoBody(c.Request) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		q, ok := publicAdminQuery(c.Request.URL.RawQuery, map[string]bool{"revision": true})
		if !ok {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		revision := int64(0)
		var err error
		if q["revision"] != "" {
			revision, err = strconv.ParseInt(q["revision"], 10, 64)
			if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != q["revision"] {
				publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid revision"})
				return
			}
		}
		out, err := state.PublicContent.Preview(c.Request.Context(), publicAdminActorID(c), revision)
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.Header("X-Robots-Tag", "noindex, nofollow")
		c.JSON(200, out)
	})
	g.POST("/publish", func(c *gin.Context) {
		var in service.PublicContentPublicationRequest
		if !publicAdminStrictJSON(c, &in) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		priceGUID, ok := publicAdminGUID(in.PriceReleaseGUID)
		if !ok {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid price release guid"})
			return
		}
		in.ActorID = publicAdminActorID(c)
		in.IdempotencyKey = c.GetHeader("Idempotency-Key")
		ticket, ticketOK := publicAdminTicketOption(c, state, actionsecurity.ActionPublicContentPublish, &priceGUID, actionsecurity.PublicContentPublishIntent{PriceReleaseGUID: priceGUID, ExpectedRevision: in.ExpectedRevision}, true)
		if !ticketOK {
			return
		}
		out, err := state.PublicContent.Publish(c.Request.Context(), in, ticket)
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(201, out)
	})
	g.GET("/releases", func(c *gin.Context) {
		if !publicAdminRequestHasNoBody(c.Request) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		q, ok := publicAdminQuery(c.Request.URL.RawQuery, map[string]bool{"page": true, "page_size": true})
		if !ok {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		page, size := parsePublicAdminPage(q)
		if page < 0 {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid pagination"})
			return
		}
		out, err := state.PublicContent.ListReleases(c.Request.Context(), page, size)
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.GET("/releases/:guid", func(c *gin.Context) {
		guid, ok := publicAdminGUID(c.Param("guid"))
		if !ok || c.Request.URL.RawQuery != "" || !publicAdminRequestHasNoBody(c.Request) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid guid"})
			return
		}
		out, err := state.PublicContent.GetRelease(c.Request.Context(), guid)
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.POST("/releases/:guid/restore", func(c *gin.Context) {
		guid, ok := publicAdminGUID(c.Param("guid"))
		var in service.RevisionRequest
		if !ok || !publicAdminStrictJSON(c, &in) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		key := c.GetHeader("Idempotency-Key")
		ticket, ticketOK := publicAdminTicketOption(c, state, actionsecurity.ActionPublicContentRestore, &guid, actionsecurity.PublicContentRestoreIntent{ReleaseGUID: guid, ExpectedRevision: in.ExpectedRevision}, true)
		if !ticketOK {
			return
		}
		out, err := state.PublicContent.Restore(c.Request.Context(), service.PublicContentRestoreRequest{ActorID: publicAdminActorID(c), ExpectedRevision: in.ExpectedRevision, ReleaseGUID: strconv.FormatInt(guid, 10), IdempotencyKey: key}, ticket)
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(http.StatusCreated, out)
	})
}
