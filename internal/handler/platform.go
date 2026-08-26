package handler

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

func RegisterPlatform(r *gin.Engine, state *app.State) {
	g := r.Group("/api/v1/platform", gatewayRequestID(), middleware.RequireUser(state))

	g.GET("/models", func(c *gin.Context) {
		if state.WhiteLabel == nil {
			platformWhiteLabelError(c, whitelabel.ErrUpstreamUnavailable("white-label service unavailable"))
			return
		}
		catalog, err := state.WhiteLabel.ListModels(c.Request.Context(), middleware.CurrentUser(c).AllowedModels)
		if err != nil {
			platformWhiteLabelError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": catalog.Data, "catalog_stale": catalog.CatalogStale})
	})

	g.GET("/models/detail", func(c *gin.Context) {
		platformModelDetail(c, state, modelIDFromDetailQuery(c))
	})

	g.GET("/models/:id", func(c *gin.Context) {
		platformModelDetail(c, state, c.Param("id"))
	})

	g.POST("/chat/completions", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		var body platformChatBody
		if err := decodePlatformRequest(c, &body, false); err != nil {
			platformWhiteLabelError(c, err)
			return
		}
		params := body.toParams()
		if body.MaxTokens == nil {
			platformWhiteLabelError(c, &whitelabel.Error{Code: whitelabel.CodeMissingMaxTokens, Status: http.StatusBadRequest, Type: whitelabel.TypeInvalidRequest})
			return
		}
		if err := platformAuthorizeModels(c, state, user, []string{params.Model}); err != nil {
			platformWhiteLabelError(c, err)
			return
		}
		if body.Stream {
			started := false
			err := state.Platform.Stream(c.Request.Context(), state.DB, user, params, func(b []byte) error {
				if !started {
					c.Header("Content-Type", "text/event-stream")
					c.Header("Cache-Control", "no-cache")
					c.Header("Connection", "keep-alive")
				}
				started = true
				_, werr := c.Writer.Write(b)
				c.Writer.Flush()
				return werr
			})
			if err != nil {
				if !started {
					platformStreamPreError(c, err)
				} else {
					platformSSEError(c)
				}
			}
			return
		}
		result, err := state.Platform.Chat(c.Request.Context(), state.DB, user, params)
		if err != nil {
			if whiteLabelErr, ok := err.(*whitelabel.Error); ok {
				platformWhiteLabelError(c, whiteLabelErr)
				return
			}
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		uid := user.ID
		_ = state.Audit.Log(state.DB, "chat.complete", &uid, "", nil, httpx.ClientIP(c, state.Settings.TrustProxyHeaders, state.Settings.TrustedProxyCIDRs))
		c.JSON(http.StatusOK, result)
	})

	g.POST("/chat/compare", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		var body platformCompareBody
		if err := decodePlatformRequest(c, &body, true); err != nil {
			platformWhiteLabelError(c, err)
			return
		}
		params := body.toParams()
		if body.MaxTokens == nil {
			platformWhiteLabelError(c, &whitelabel.Error{Code: whitelabel.CodeMissingMaxTokens, Status: http.StatusBadRequest, Type: whitelabel.TypeInvalidRequest})
			return
		}
		if len(body.Models) == 0 || len(body.Models) > 3 {
			platformWhiteLabelError(c, &whitelabel.Error{Code: whitelabel.CodeInvalidRequest, Status: http.StatusBadRequest, Type: whitelabel.TypeInvalidRequest})
			return
		}
		if err := platformAuthorizeModels(c, state, user, body.Models); err != nil {
			platformWhiteLabelError(c, err)
			return
		}
		if body.Stream {
			started := false
			err := state.Platform.CompareStream(c.Request.Context(), state.DB, user, body.Models, params, c.Writer.Header().Get("X-Request-ID"), func(b []byte) error {
				if !started {
					c.Header("Content-Type", "text/event-stream")
					c.Header("Cache-Control", "no-cache")
					c.Header("Connection", "keep-alive")
				}
				started = true
				_, werr := c.Writer.Write(b)
				c.Writer.Flush()
				return werr
			})
			if err != nil {
				if !started {
					platformStreamPreError(c, err)
				} else {
					platformSSEError(c)
				}
			}
			return
		}
		result, err := state.Platform.Compare(c.Request.Context(), state.DB, user, body.Models, params)
		if err != nil {
			if whiteLabelErr, ok := err.(*whitelabel.Error); ok {
				platformWhiteLabelError(c, whiteLabelErr)
				return
			}
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		c.JSON(http.StatusOK, result)
	})
}

func platformModelDetail(c *gin.Context, state *app.State, modelID string) {
	if state.WhiteLabel == nil {
		platformWhiteLabelError(c, whitelabel.ErrUpstreamUnavailable("white-label service unavailable"))
		return
	}
	model, err := state.WhiteLabel.GetModel(c.Request.Context(), modelID, middleware.CurrentUser(c).AllowedModels)
	if err != nil {
		platformWhiteLabelError(c, err)
		return
	}
	c.JSON(http.StatusOK, model)
}

// modelIDFromDetailQuery leaves the previous /models/detail route behavior
// intact for a legacy model literally named "detail". An explicitly supplied
// query value, including an empty one, always uses the new query-ID contract.
func modelIDFromDetailQuery(c *gin.Context) string {
	if modelID, present := c.GetQuery("id"); present {
		return modelID
	}
	return "detail"
}

func platformStreamPreError(c *gin.Context, err error) {
	if whiteLabelErr, ok := err.(*whitelabel.Error); ok {
		platformWhiteLabelError(c, whiteLabelErr)
		return
	}
	platformWhiteLabelError(c, whitelabel.ErrUpstreamUnavailable("platform stream failed"))
}

// decodePlatformRequest separates local conversation fields from the
// OpenAI-compatible payload, then applies the same closed validation used by
// /v1 before any catalog lookup or upstream request.
func decodePlatformRequest(c *gin.Context, dest interface{}, compare bool) *whitelabel.Error {
	mediaType, _, contentTypeErr := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if contentTypeErr != nil || mediaType != "application/json" {
		return &whitelabel.Error{Code: whitelabel.CodeInvalidRequest, Status: http.StatusUnsupportedMediaType, Type: whitelabel.TypeInvalidRequest}
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, whitelabel.MaxRequestBodyBytes)
	raw, readErr := io.ReadAll(c.Request.Body)
	if readErr != nil {
		return &whitelabel.Error{Code: whitelabel.CodeRequestTooLarge, Status: http.StatusRequestEntityTooLarge, Type: whitelabel.TypeInvalidRequest}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return &whitelabel.Error{Code: whitelabel.CodeInvalidRequest, Status: http.StatusBadRequest, Type: whitelabel.TypeInvalidRequest}
	}
	if _, legacy := fields["conversation_id"]; legacy {
		return &whitelabel.Error{Code: whitelabel.CodeInvalidRequest, Status: http.StatusBadRequest, Type: whitelabel.TypeInvalidRequest}
	}
	for _, field := range []string{"conversation_guid", "context_window"} {
		delete(fields, field)
	}
	if compare {
		delete(fields, "models")
	}
	upstreamBody, marshalErr := json.Marshal(fields)
	if marshalErr != nil {
		return whitelabel.ErrUpstreamUnavailable("request encoding failed")
	}
	if validationErr := whitelabel.ValidateRequest(upstreamBody, whitelabel.PlatformValidation); validationErr != nil {
		return validationErr
	}
	if json.Unmarshal(raw, dest) != nil {
		return &whitelabel.Error{Code: whitelabel.CodeInvalidRequest, Status: http.StatusBadRequest, Type: whitelabel.TypeInvalidRequest}
	}
	switch body := dest.(type) {
	case *platformChatBody:
		body.WhiteLabelBody = upstreamBody
	case *platformCompareBody:
		body.WhiteLabelBody = upstreamBody
	}
	return nil
}

func platformSSEError(c *gin.Context) {
	response := whitelabel.PublicError(whitelabel.ErrUpstreamUnavailable("platform stream failed"), c.Writer.Header().Get("X-Request-ID"))
	payload, _ := json.Marshal(response)
	_, _ = c.Writer.Write([]byte("event: error\n"))
	_, _ = c.Writer.Write([]byte("data: "))
	_, _ = c.Writer.Write(payload)
	_, _ = c.Writer.Write([]byte("\n\ndata: [DONE]\n\n"))
	c.Writer.Flush()
}

// platformAuthorizeModels resolves the catalog once before billing or chat.
// This prevents a user ACL miss or a removed model from reaching an upstream.
func platformAuthorizeModels(c *gin.Context, state *app.State, user *models.User, ids []string) *whitelabel.Error {
	if state.WhiteLabel == nil {
		return whitelabel.ErrUpstreamUnavailable("white-label service unavailable")
	}
	catalog, err := state.WhiteLabel.ListModels(c.Request.Context(), user.AllowedModels)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !catalogContains(catalog, id) {
			return &whitelabel.Error{Code: whitelabel.CodeModelUnavailable, Status: http.StatusNotFound, Type: whitelabel.TypeInvalidRequest}
		}
		if authErr := state.WhiteLabel.AuthorizeModel(id, user.AllowedModels); authErr != nil {
			return authErr
		}
	}
	return nil
}

// platformWhiteLabelError uses the same scrubbed envelope as the public
// gateway. Platform callers never receive upstream response text or IDs.
func platformWhiteLabelError(c *gin.Context, err *whitelabel.Error) {
	response := whitelabel.PublicError(err, c.Writer.Header().Get("X-Request-ID"))
	c.AbortWithStatusJSON(response.Status, response)
}

type platformChatBody struct {
	Model            string                   `json:"model" binding:"required"`
	Messages         []map[string]interface{} `json:"messages" binding:"required"`
	ConversationGUID *string                  `json:"conversation_guid"`
	Temperature      *float64                 `json:"temperature"`
	MaxTokens        *int                     `json:"max_tokens"`
	ContextWindow    *int                     `json:"context_window"`
	Stream           bool                     `json:"stream"`
	WhiteLabelBody   []byte                   `json:"-"`
}

func (b platformChatBody) toParams() service.ChatParams {
	return service.ChatParams{
		Model:            b.Model,
		Messages:         b.Messages,
		ConversationGUID: b.ConversationGUID,
		Temperature:      b.Temperature,
		MaxTokens:        b.MaxTokens,
		ContextWindow:    b.ContextWindow,
		WhiteLabelBody:   b.WhiteLabelBody,
	}
}

type platformCompareBody struct {
	Models           []string                 `json:"models" binding:"required"`
	Messages         []map[string]interface{} `json:"messages" binding:"required"`
	ConversationGUID *string                  `json:"conversation_guid"`
	Temperature      *float64                 `json:"temperature"`
	MaxTokens        *int                     `json:"max_tokens"`
	ContextWindow    *int                     `json:"context_window"`
	Stream           bool                     `json:"stream"`
	WhiteLabelBody   []byte                   `json:"-"`
}

func (b platformCompareBody) toParams() service.ChatParams {
	return service.ChatParams{
		Messages:         b.Messages,
		ConversationGUID: b.ConversationGUID,
		Temperature:      b.Temperature,
		MaxTokens:        b.MaxTokens,
		ContextWindow:    b.ContextWindow,
		WhiteLabelBody:   b.WhiteLabelBody,
	}
}
