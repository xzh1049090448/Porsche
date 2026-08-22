package handler

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/registry"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

func registerGatewayRoutes(r *gin.Engine, state *app.State) {
	g := r.Group("/v1", gatewayRequestID())
	g.GET("/models", func(c *gin.Context) {
		token, ok := authenticateGatewayToken(c, state, "")
		if !ok {
			return
		}
		if state.WhiteLabel == nil {
			gatewayWhiteLabelError(c, whitelabel.ErrUpstreamUnavailable("white-label service unavailable"))
			return
		}
		catalog, err := state.WhiteLabel.ListModels(c.Request.Context(), token.AllowedModels)
		if err != nil {
			gatewayWhiteLabelError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"object": "list", "data": catalog.Data})
	})
	g.GET("/models/:id", func(c *gin.Context) {
		token, ok := authenticateGatewayToken(c, state, "")
		if !ok {
			return
		}
		if state.WhiteLabel == nil {
			gatewayWhiteLabelError(c, whitelabel.ErrUpstreamUnavailable("white-label service unavailable"))
			return
		}
		model, err := state.WhiteLabel.GetModel(c.Request.Context(), c.Param("id"), token.AllowedModels)
		if err != nil {
			gatewayWhiteLabelError(c, err)
			return
		}
		c.JSON(http.StatusOK, model)
	})
	g.POST("/chat/completions", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, whitelabel.MaxRequestBodyBytes)
		token, ok := authenticateGatewayToken(c, state, "")
		if !ok {
			return
		}
		mediaType, _, contentTypeErr := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if contentTypeErr != nil || mediaType != "application/json" {
			gatewayWhiteLabelError(c, &whitelabel.Error{Code: whitelabel.CodeInvalidRequest, Status: http.StatusUnsupportedMediaType, Type: whitelabel.TypeInvalidRequest})
			return
		}
		body, readErr := io.ReadAll(c.Request.Body)
		if readErr != nil {
			gatewayWhiteLabelError(c, &whitelabel.Error{Code: whitelabel.CodeRequestTooLarge, Status: http.StatusRequestEntityTooLarge, Type: whitelabel.TypeInvalidRequest})
			return
		}
		if validationErr := whitelabel.ValidateRequest(body, whitelabel.GatewayValidation); validationErr != nil {
			gatewayWhiteLabelError(c, validationErr)
			return
		}
		modelID, stream := whitelabel.RequestModelAndStream(body)
		if !modelAllowedForToken(token, modelID) {
			gatewayAuthenticationError(c, http.StatusForbidden, service.GatewayTokenModelDenied)
			return
		}
		if state.WhiteLabel == nil {
			gatewayWhiteLabelError(c, whitelabel.ErrUpstreamUnavailable("white-label service unavailable"))
			return
		}
		catalog, catalogErr := state.WhiteLabel.ListModels(c.Request.Context(), token.AllowedModels)
		if catalogErr != nil {
			gatewayWhiteLabelError(c, catalogErr)
			return
		}
		if !catalogContains(catalog, modelID) {
			gatewayWhiteLabelError(c, &whitelabel.Error{Code: whitelabel.CodeModelUnavailable, Status: http.StatusNotFound, Type: whitelabel.TypeInvalidRequest})
			return
		}
		if authErr := state.WhiteLabel.AuthorizeModel(modelID, token.AllowedModels); authErr != nil {
			gatewayWhiteLabelError(c, authErr)
			return
		}
		response, upstreamErr := state.WhiteLabel.Chat(c.Request.Context(), body)
		if upstreamErr != nil {
			gatewayWhiteLabelError(c, upstreamErr)
			return
		}
		defer response.Body.Close()
		if !stream {
			data, err := io.ReadAll(io.LimitReader(response.Body, whitelabel.MaxRequestBodyBytes))
			if err != nil {
				gatewayWhiteLabelError(c, whitelabel.ErrUpstreamUnavailable("chat body read failed"))
				return
			}
			completion, completionErr := state.WhiteLabel.ProjectChatCompletion(data, modelID)
			if completionErr != nil {
				gatewayWhiteLabelError(c, completionErr)
				return
			}
			c.JSON(http.StatusOK, completion)
			return
		}
		buf := make([]byte, 4096)
		n, readErr := response.Body.Read(buf)
		if n == 0 {
			gatewayWhiteLabelError(c, whitelabel.ErrUpstreamUnavailable("stream ended before first payload"))
			return
		}
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		chunk := buf[:n]
		sawDone := bytes.Contains(chunk, []byte("[DONE]"))
		if _, writeErr := c.Writer.Write(chunk); writeErr != nil {
			return
		}
		c.Writer.Flush()
		if readErr == io.EOF && sawDone {
			return
		}
		if readErr != nil {
			gatewaySSEError(c)
			return
		}
		for {
			n, err := response.Body.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				sawDone = sawDone || bytes.Contains(chunk, []byte("[DONE]"))
				if _, writeErr := c.Writer.Write(chunk); writeErr != nil {
					return
				}
				c.Writer.Flush()
			}
			if err == io.EOF && sawDone {
				return
			}
			if err != nil || n == 0 {
				gatewaySSEError(c)
				return
			}
		}
	})
}

func RegisterGatewayTokens(r *gin.Engine, state *app.State) {
	g := r.Group("/api/v1/tokens", middleware.RequireUser(state))
	g.GET("", func(c *gin.Context) {
		tokens, err := state.GatewayTokens.List(middleware.CurrentUserID(c))
		if err != nil {
			httpx.AbortJSON(c, http.StatusInternalServerError, "读取令牌失败")
			return
		}
		out := make([]gin.H, 0, len(tokens))
		for i := range tokens {
			out = append(out, gatewayTokenResponse(&tokens[i]))
		}
		c.JSON(http.StatusOK, out)
	})
	g.POST("", func(c *gin.Context) {
		var body gatewayTokenInput
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, "令牌参数无效")
			return
		}
		expiresAt, err := body.expiry()
		if err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, "expires_at 必须为 RFC3339 时间")
			return
		}
		token, secret, err := state.GatewayTokens.Create(middleware.CurrentUser(c), service.GatewayTokenCreateInput{Name: body.Name, AllowedModels: body.AllowedModels, IPAllowlist: body.IPAllowlist, ExpiresAt: expiresAt})
		if err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, "令牌创建失败，请检查输入")
			return
		}
		uid := middleware.CurrentUserID(c)
		_ = state.Audit.Log(state.DB, "gateway_token.create", &uid, strconv.Itoa(token.ID), nil, httpx.ClientIP(c, state.Settings.TrustProxyHeaders, state.Settings.TrustedProxyCIDRs))
		out := gatewayTokenResponse(token)
		out["token"] = secret // The plaintext is intentionally returned only from this creation response.
		c.JSON(http.StatusCreated, out)
	})
	g.GET("/:id", func(c *gin.Context) { gatewayTokenGet(c, state) })
	g.PATCH("/:id", func(c *gin.Context) {
		id, ok := gatewayTokenID(c)
		if !ok {
			return
		}
		var body gatewayTokenUpdateInput
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, "令牌参数无效")
			return
		}
		expiresAt, err := body.expiry()
		if err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, "expires_at 必须为 RFC3339 时间")
			return
		}
		token, err := state.GatewayTokens.Update(middleware.CurrentUserID(c), id, service.GatewayTokenUpdateInput{Name: body.Name, AllowedModels: body.AllowedModels, IPAllowlist: body.IPAllowlist, ExpiresAt: expiresAt, Status: body.Status})
		if err != nil {
			gatewayTokenWriteError(c, err)
			return
		}
		c.JSON(http.StatusOK, gatewayTokenResponse(token))
	})
	g.POST("/:id/revoke", func(c *gin.Context) { revokeGatewayToken(c, state) })
	g.DELETE("/:id", func(c *gin.Context) { revokeGatewayToken(c, state) })
}

func authenticateGatewayToken(c *gin.Context, state *app.State, model string) (registry.ClientConfig, bool) {
	secret := httpx.BearerToken(c)
	if secret == "" {
		gatewayAuthenticationError(c, http.StatusUnauthorized, service.GatewayTokenInvalid)
		return registry.ClientConfig{}, false
	}
	ip := httpx.ClientIP(c, state.Settings.TrustProxyHeaders, state.Settings.TrustedProxyCIDRs)
	token, err := state.GatewayTokens.Authenticate(secret, ip, model, time.Now().UTC())
	if err == nil {
		return registry.ClientConfig{Name: token.Name, AllowedModels: token.AllowedModels, IPAllowlist: token.IPAllowlist}, true
	}
	if state.Settings.GatewayAllowLegacyClients && !strings.HasPrefix(secret, "sk-gw-") {
		if client, found := state.Clients.GetBySecret(secret); found && registry.IPAllowed(client, ip) && (model == "" || registry.ModelAllowed(client, model)) {
			return client, true
		}
	}
	status := http.StatusUnauthorized
	code := service.GatewayTokenInvalid
	if service.IsGatewayTokenError(err, service.GatewayTokenModelDenied) || service.IsGatewayTokenError(err, service.GatewayTokenIPDenied) {
		status = http.StatusForbidden
		code = service.GatewayTokenError(err.Error())
	}
	if service.IsGatewayTokenError(err, service.GatewayTokenExpired) || service.IsGatewayTokenError(err, service.GatewayTokenDisabled) || service.IsGatewayTokenError(err, service.GatewayTokenRevoked) {
		code = service.GatewayTokenError(err.Error())
	}
	gatewayAuthenticationError(c, status, code)
	return registry.ClientConfig{}, false
}

func modelAllowedForToken(client registry.ClientConfig, model string) bool {
	return registry.ModelAllowed(client, model)
}
func gatewayRequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if !validRequestID(id) {
			b := make([]byte, 16)
			if _, err := rand.Read(b); err == nil {
				id = hex.EncodeToString(b)
			} else {
				id = strconv.FormatInt(time.Now().UnixNano(), 36)
			}
		}
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
func gatewayAuthenticationError(c *gin.Context, status int, code service.GatewayTokenError) {
	gatewayWhiteLabelError(c, whitelabel.ErrGatewayAuthentication(whitelabel.Code(code), status))
}

func gatewayWhiteLabelError(c *gin.Context, err *whitelabel.Error) {
	response := whitelabel.PublicError(err, c.Writer.Header().Get("X-Request-ID"))
	c.AbortWithStatusJSON(response.Status, response)
}

func catalogContains(catalog whitelabel.Catalog, id string) bool {
	for _, model := range catalog.Data {
		if model.ID == id {
			return true
		}
	}
	return false
}

func gatewaySSEError(c *gin.Context) {
	response := whitelabel.PublicError(whitelabel.ErrUpstreamUnavailable("stream ended before done"), c.Writer.Header().Get("X-Request-ID"))
	payload, _ := json.Marshal(response.Error)
	_, _ = c.Writer.Write([]byte("event: error\n"))
	_, _ = c.Writer.Write([]byte("data: "))
	_, _ = c.Writer.Write(payload)
	_, _ = c.Writer.Write([]byte("\n\ndata: [DONE]\n\n"))
	c.Writer.Flush()
}

type gatewayTokenInput struct {
	Name          string           `json:"name" binding:"required"`
	AllowedModels models.JSONSlice `json:"allowed_models"`
	IPAllowlist   models.JSONSlice `json:"ip_allowlist"`
	ExpiresAt     *string          `json:"expires_at"`
}

func (i gatewayTokenInput) expiry() (*time.Time, error) {
	if i.ExpiresAt == nil {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, *i.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

type gatewayTokenUpdateInput struct {
	Name          *string                    `json:"name"`
	AllowedModels *models.JSONSlice          `json:"allowed_models"`
	IPAllowlist   *models.JSONSlice          `json:"ip_allowlist"`
	ExpiresAt     *string                    `json:"expires_at"`
	Status        *models.GatewayTokenStatus `json:"status"`
}

func (i gatewayTokenUpdateInput) expiry() (**time.Time, error) {
	if i.ExpiresAt == nil {
		return nil, nil
	}
	if *i.ExpiresAt == "" {
		return new(*time.Time), nil
	}
	t, err := time.Parse(time.RFC3339, *i.ExpiresAt)
	if err != nil {
		return nil, err
	}
	expiresAt := &t
	return &expiresAt, nil
}
func gatewayTokenResponse(token *models.GatewayAPIToken) gin.H {
	return gin.H{"id": token.ID, "name": token.Name, "token_prefix": token.TokenPrefix, "status": token.Status, "allowed_models": token.AllowedModels, "ip_allowlist": token.IPAllowlist, "expires_at": token.ExpiresAt, "last_used_at": token.LastUsedAt, "created_at": token.CreatedAt}
}
func gatewayTokenID(c *gin.Context) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		httpx.AbortJSON(c, http.StatusNotFound, "令牌不存在")
		return 0, false
	}
	return id, true
}
func gatewayTokenGet(c *gin.Context, state *app.State) {
	id, ok := gatewayTokenID(c)
	if !ok {
		return
	}
	token, err := state.GatewayTokens.Get(middleware.CurrentUserID(c), id)
	if err != nil {
		gatewayTokenWriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gatewayTokenResponse(token))
}
func revokeGatewayToken(c *gin.Context, state *app.State) {
	id, ok := gatewayTokenID(c)
	if !ok {
		return
	}
	if err := state.GatewayTokens.Revoke(middleware.CurrentUserID(c), id); err != nil {
		gatewayTokenWriteError(c, err)
		return
	}
	uid := middleware.CurrentUserID(c)
	_ = state.Audit.Log(state.DB, "gateway_token.revoke", &uid, strconv.Itoa(id), nil, httpx.ClientIP(c, state.Settings.TrustProxyHeaders, state.Settings.TrustedProxyCIDRs))
	c.Status(http.StatusNoContent)
}
func gatewayTokenWriteError(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.AbortJSON(c, http.StatusNotFound, "令牌不存在")
		return
	}
	httpx.AbortJSON(c, http.StatusUnprocessableEntity, "令牌更新失败，请检查输入")
}
