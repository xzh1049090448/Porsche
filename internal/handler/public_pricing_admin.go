package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func RegisterPublicPricingAdmin(r *gin.Engine, state *app.State) {
	g := r.Group("/admin/v2/public-pricing", gatewayRequestID(), publicAdminNoStore, middleware.RequireRootWithError(state, publicAdminAuthError), publicAdminHeaderBoundary)
	g.GET("/draft", func(c *gin.Context) {
		if c.Request.URL.RawQuery != "" || !publicAdminRequestHasNoBody(c.Request) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		out, err := state.PublicPriceSnapshots.GetDraft(c.Request.Context(), publicAdminActorID(c))
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.PUT("/draft", func(c *gin.Context) {
		var in service.PublicPriceDraftSaveRequest
		if !publicAdminStrictJSON(c, &in) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		out, err := state.PublicPriceSnapshots.SaveDraft(c.Request.Context(), publicAdminActorID(c), in)
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
		out, err := state.PublicPriceSnapshots.Validate(c.Request.Context(), publicAdminActorID(c), in.ExpectedRevision)
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.POST("/publish", func(c *gin.Context) {
		var in service.RevisionRequest
		if !publicAdminStrictJSON(c, &in) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		key := c.GetHeader("Idempotency-Key")
		ticket, ok := publicAdminTicketOption(c, state, actionsecurity.ActionPublicPricingPublish, nil, actionsecurity.PublicPricingPublishIntent{ExpectedRevision: in.ExpectedRevision}, true)
		if !ok {
			return
		}
		out, err := state.PublicPriceSnapshots.Publish(c.Request.Context(), service.PublicPriceSnapshotRequest{ActorID: publicAdminActorID(c), ExpectedRevision: in.ExpectedRevision, IdempotencyKey: key}, ticket)
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
		out, err := state.PublicPriceSnapshots.ListReleases(c.Request.Context(), page, size)
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
		out, err := state.PublicPriceSnapshots.GetRelease(c.Request.Context(), guid)
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
		ticket, ticketOK := publicAdminTicketOption(c, state, actionsecurity.ActionPublicPricingRestore, &guid, actionsecurity.PublicPricingRestoreIntent{ReleaseGUID: guid, ExpectedRevision: in.ExpectedRevision}, true)
		if !ticketOK {
			return
		}
		out, err := state.PublicPriceSnapshots.Restore(c.Request.Context(), service.PublicPriceSnapshotRestoreRequest{ActorID: publicAdminActorID(c), ExpectedRevision: in.ExpectedRevision, SnapshotGUID: strconv.FormatInt(guid, 10), IdempotencyKey: key}, ticket)
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(http.StatusCreated, out)
	})
}
