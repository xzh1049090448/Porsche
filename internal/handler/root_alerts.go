package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/service"
)

const publicAdminBodyLimit int64 = 1 << 20

func publicAdminNoStore(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() }

func publicAdminError(c *gin.Context, err error) {
	status, message := service.StatusFromError(err)
	if status == http.StatusInternalServerError {
		status, message = http.StatusServiceUnavailable, "public administration unavailable"
	}
	code := map[int]string{400: "invalid_request", 401: "authentication_required", 403: "root_role_required", 404: "not_found", 409: "conflict", 410: "gone", 422: "validation_failed", 503: "unavailable"}[status]
	if code == "" {
		code = "unavailable"
	}
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": message, "request_id": c.Writer.Header().Get("X-Request-ID")}})
}

func publicAdminGUID(raw string) (int64, bool) {
	if raw == "" || raw[0] == '0' || len(raw) > 19 {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	return v, err == nil && v > 0 && strconv.FormatInt(v, 10) == raw
}

func publicAdminQuery(raw string, allowed map[string]bool) (map[string]string, bool) {
	out := map[string]string{}
	if raw == "" {
		return out, true
	}
	for _, part := range strings.Split(raw, "&") {
		p := strings.SplitN(part, "=", 2)
		if len(p) != 2 || !allowed[p[0]] || p[1] == "" {
			return nil, false
		}
		if _, exists := out[p[0]]; exists {
			return nil, false
		}
		out[p[0]] = p[1]
	}
	return out, true
}

func publicAdminStrictJSON(c *gin.Context, out any) bool {
	if c.Request.URL.RawQuery != "" || c.Request.URL.RawPath != "" || c.Request.Body == nil {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, publicAdminBodyLimit+1))
	if err != nil || int64(len(raw)) > publicAdminBodyLimit {
		return false
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	tok, err := scan.Token()
	if err != nil {
		return false
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return false
	}
	seen := map[string]bool{}
	for scan.More() {
		k, err := scan.Token()
		if err != nil {
			return false
		}
		key, ok := k.(string)
		if !ok || seen[key] {
			return false
		}
		seen[key] = true
		var discard json.RawMessage
		if scan.Decode(&discard) != nil {
			return false
		}
	}
	if _, err = scan.Token(); err != nil {
		return false
	}
	if scan.Decode(&struct{}{}) != io.EOF {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return false
	}
	return true
}

func publicAdminActorID(c *gin.Context) int64 {
	u := middleware.CurrentUser(c)
	if u == nil {
		return 0
	}
	return u.ID
}

func consumePublicAdminTicket(c *gin.Context, state *app.State, action actionsecurity.Action, target *int64, intent any, requireIdempotency bool) bool {
	if state.ActionVerifications == nil {
		publicAdminError(c, service.ErrActionVerificationUnavailable)
		return false
	}
	tickets, canonical := exactHeaderValues(c, "X-Action-Ticket")
	if !canonical {
		publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid action ticket"})
		return false
	}
	if requireIdempotency {
		keys, keyCanonical := exactHeaderValues(c, "Idempotency-Key")
		if !keyCanonical {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid idempotency key"})
			return false
		}
		if _, err := actionsecurity.ParseIdempotencyKey(keys); err != nil {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid idempotency key"})
			return false
		}
	}
	if err := state.ActionVerifications.Consume(c.Request.Context(), service.VerificationConsume{Actor: adminUserActionActor(c), Action: action, TargetGUID: target, Intent: intent, TicketValues: tickets}); err != nil {
		publicAdminError(c, err)
		return false
	}
	return true
}

func RegisterRootAlerts(r *gin.Engine, state *app.State) {
	g := r.Group("/admin/v2/notifications", gatewayRequestID(), publicAdminNoStore, middleware.RequireRoot(state))
	g.GET("/unread-count", func(c *gin.Context) {
		if c.Request.URL.RawQuery != "" || c.Request.URL.RawPath != "" {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		out, err := state.RootAlerts.UnreadCount(c.Request.Context(), publicAdminActorID(c))
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.GET("", func(c *gin.Context) {
		q, ok := publicAdminQuery(c.Request.URL.RawQuery, map[string]bool{"state": true, "page": true, "page_size": true})
		if !ok {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		page, size := parsePublicAdminPage(q)
		if page < 0 {
			publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid pagination"})
			return
		}
		out, err := state.RootAlerts.List(c.Request.Context(), publicAdminActorID(c), service.RootAlertListQuery{State: q["state"], Page: page, PageSize: size})
		if err != nil {
			publicAdminError(c, err)
			return
		}
		c.JSON(200, out)
	})
	mutate := func(ack bool) gin.HandlerFunc {
		return func(c *gin.Context) {
			if c.Request.URL.RawQuery != "" || c.Request.ContentLength > 0 {
				publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
				return
			}
			if _, ok := publicAdminGUID(c.Param("guid")); !ok {
				publicAdminError(c, &service.HTTPError{Status: 400, Message: "invalid guid"})
				return
			}
			var out any
			var err error
			if ack {
				out, err = state.RootAlerts.Acknowledge(c.Request.Context(), publicAdminActorID(c), c.Param("guid"))
			} else {
				out, err = state.RootAlerts.MarkRead(c.Request.Context(), publicAdminActorID(c), c.Param("guid"))
			}
			if err != nil {
				publicAdminError(c, err)
				return
			}
			c.JSON(200, out)
		}
	}
	g.POST("/:guid/read", mutate(false))
	g.POST("/:guid/acknowledge", mutate(true))
}

func parsePublicAdminPage(q map[string]string) (int, int) {
	page, size := 1, 20
	var err error
	if q["page"] != "" {
		page, err = strconv.Atoi(q["page"])
		if err != nil || strconv.Itoa(page) != q["page"] {
			return -1, -1
		}
	}
	if q["page_size"] != "" {
		size, err = strconv.Atoi(q["page_size"])
		if err != nil || strconv.Itoa(size) != q["page_size"] {
			return -1, -1
		}
	}
	if page < 1 || (size != 20 && size != 50 && size != 100) {
		return -1, -1
	}
	return page, size
}
