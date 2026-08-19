package handler

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/gateway"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/registry"
	"github.com/porsche/ai-gateway-go/internal/service"
	"gorm.io/gorm"
)

func registerGatewayRoutes(r *gin.Engine, state *app.State) {
	g := r.Group("/v1", gatewayRequestID())
	g.GET("/models", func(c *gin.Context) {
		token, ok := authenticateGatewayToken(c, state, "")
		if !ok {
			return
		}
		data := make([]gin.H, 0)
		for name, route := range state.Models.Routes() {
			if modelAllowedForToken(token, name) {
				data = append(data, gin.H{"id": name, "object": "model", "owned_by": route.Provider})
			}
		}
		c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
	})
	g.POST("/chat/completions", func(c *gin.Context) {
		var body gateway.ChatCompletionRequest
		if err := c.ShouldBindJSON(&body); err != nil {
			gatewayError(c, http.StatusBadRequest, "gateway_invalid_request", "invalid chat completion request")
			return
		}
		client, ok := authenticateGatewayToken(c, state, body.Model)
		if !ok {
			return
		}
		if body.Stream {
			resp, err := state.Gateway.Stream(c.Request.Context(), client, body)
			if err != nil {
				gatewayError(c, http.StatusBadGateway, "gateway_upstream_error", "upstream request failed")
				return
			}
			defer resp.Body.Close()
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Status(resp.StatusCode)
			buf := make([]byte, 4096)
			for {
				n, readErr := resp.Body.Read(buf)
				if n > 0 {
					if _, err := c.Writer.Write(buf[:n]); err != nil {
						return
					}
					c.Writer.Flush()
				}
				if readErr == io.EOF {
					return
				}
				if readErr != nil {
					return
				}
			}
		}
		data, err := state.Gateway.Complete(c.Request.Context(), client, body)
		if err != nil {
			gatewayError(c, http.StatusBadGateway, "gateway_upstream_error", "upstream request failed")
			return
		}
		c.JSON(http.StatusOK, data)
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
		gatewayError(c, http.StatusUnauthorized, "gateway_invalid_token", "missing bearer token")
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
	code := "gateway_invalid_token"
	if service.IsGatewayTokenError(err, service.GatewayTokenModelDenied) || service.IsGatewayTokenError(err, service.GatewayTokenIPDenied) {
		status = http.StatusForbidden
		code = err.Error()
	}
	if service.IsGatewayTokenError(err, service.GatewayTokenExpired) || service.IsGatewayTokenError(err, service.GatewayTokenDisabled) || service.IsGatewayTokenError(err, service.GatewayTokenRevoked) {
		code = err.Error()
	}
	gatewayError(c, status, code, "gateway token is not authorized")
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
func gatewayError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"message": message, "type": "gateway_error", "code": code}})
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
