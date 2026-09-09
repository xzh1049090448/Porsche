package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func RegisterPublicModelAdmin(r *gin.Engine, state *app.State) {
	g := r.Group("/admin/v2/public-models", gatewayRequestID(), publicAdminNoStore, middleware.RequireRootWithError(state, publicAdminAuthError))
	// Static paths must precede the GUID parameter.
	g.GET("/missing", func(c *gin.Context) {
		if c.Request.URL.RawQuery != "" || !publicAdminRequestHasNoBody(c.Request) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		out, err := state.PublicModels.Missing(c.Request.Context())
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.POST("/sync", func(c *gin.Context) {
		if c.Request.URL.RawQuery != "" || !publicAdminRequestHasNoBody(c.Request) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		if err := state.PublicModels.Sync(c.Request.Context(), state.UpstreamPriceMonitor); err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"accepted": true})
	})
	g.GET("", func(c *gin.Context) {
		if !publicAdminRequestHasNoBody(c.Request) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		q, ok := publicAdminQuery(c.Request.URL.RawQuery, map[string]bool{"search": true, "status": true, "upstream_state": true, "page": true, "page_size": true})
		if !ok {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		page, size := parsePublicAdminPage(q)
		if page < 0 {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid pagination"})
			return
		}
		out, err := state.PublicModels.List(c.Request.Context(), service.AdminModelListRequest{Search: q["search"], Status: q["status"], UpstreamState: q["upstream_state"], Page: page, PageSize: size})
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.POST("", func(c *gin.Context) {
		var in service.CreatePublicModelRequest
		if !publicAdminStrictJSON(c, &in) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		out, err := state.PublicModels.Create(c.Request.Context(), publicAdminActorID(c), in)
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(201, out)
	})
	g.GET("/:guid", func(c *gin.Context) {
		guid, ok := publicAdminGUID(c.Param("guid"))
		if !ok || c.Request.URL.RawQuery != "" || !publicAdminRequestHasNoBody(c.Request) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid guid"})
			return
		}
		out, err := state.PublicModels.Get(c.Request.Context(), guid)
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.PATCH("/:guid", func(c *gin.Context) {
		guid, ok := publicAdminGUID(c.Param("guid"))
		var in service.UpdatePublicModelRequest
		if !ok || !publicAdminStrictJSON(c, &in) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		out, err := state.PublicModels.Update(c.Request.Context(), publicAdminActorID(c), guid, in)
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.POST("/:guid/activate", func(c *gin.Context) {
		guid, ok := publicAdminGUID(c.Param("guid"))
		var in service.RevisionRequest
		if !ok || !publicAdminStrictJSON(c, &in) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		out, err := state.PublicModels.Activate(c.Request.Context(), publicAdminActorID(c), guid, in.ExpectedRevision)
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.POST("/:guid/deactivate", func(c *gin.Context) {
		guid, ok := publicAdminGUID(c.Param("guid"))
		var in service.DeactivationRequest
		if !ok || !publicAdminStrictJSON(c, &in) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		out, err := state.PublicModels.Deactivate(c.Request.Context(), publicAdminActorID(c), guid, in)
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.DELETE("/:guid", func(c *gin.Context) {
		guid, ok := publicAdminGUID(c.Param("guid"))
		var in service.DeletePublicModelRequest
		if !ok || !publicAdminStrictJSON(c, &in) {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		if !consumePublicAdminTicket(c, state, actionsecurity.ActionPublicModelDelete, &guid, actionsecurity.PublicModelDeleteIntent{ModelGUID: guid, ExpectedRevision: in.ExpectedRevision, Reason: in.Reason}, false) {
			return
		}
		if err := state.PublicModels.Delete(c.Request.Context(), publicAdminActorID(c), guid, in); err != nil {
			publicAdminError(c, err)
			return
		}
		c.Status(204)
	})
}
