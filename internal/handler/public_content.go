package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/publiccontent"
	"github.com/porsche/ai-gateway-go/internal/service"
)

const publicCacheControl = "public, max-age=60, stale-while-revalidate=300"

func RegisterPublicContent(r *gin.Engine, state *app.State) {
	var reader publicProjectionReader
	if state != nil {
		reader = state.PublicCatalogReads
	}
	registerPublicContentWithReader(r, reader, func(c *gin.Context) bool {
		return middleware.AuthenticateUserWithError(c, state, func(c *gin.Context, status int, _ string) {
			publicReadError(c, &service.HTTPError{Status: status, Message: "authentication required"})
		})
	})
}

type publicProjectionReader interface {
	Projection(context.Context) (*service.PublicCatalogProjection, error)
}

func registerPublicContentWithReader(r *gin.Engine, reader publicProjectionReader, authenticate func(*gin.Context) bool) {
	// Preserve escaped path bytes so an encoded slash cannot be normalized into
	// another public resource before the model-key validator sees it.
	r.UseRawPath = true
	r.UnescapePathValues = false
	g := r.Group("/api/v1/public", gatewayRequestID())
	read := func(c *gin.Context) (*service.PublicCatalogProjection, bool) {
		if !publicReadHasNoBody(c.Request) {
			publicReadError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return nil, false
		}
		authenticated := false
		if c.GetHeader("Authorization") != "" {
			if authenticate == nil || !authenticate(c) {
				return nil, false
			}
			authenticated = true
		}
		if reader == nil {
			publicReadError(c, &service.HTTPError{Status: 503, Message: "public content unavailable"})
			return nil, false
		}
		projection, err := reader.Projection(c.Request.Context())
		if err != nil {
			publicReadError(c, err)
			return nil, false
		}
		if projection.PriceVisibility.String() == "authenticated_only" {
			c.Header("Vary", "Authorization")
		}
		if authenticated {
			c.Header("Cache-Control", "private, no-store")
		} else {
			c.Header("Cache-Control", publicCacheControl)
		}
		return projection, authenticated
	}
	plain := func(document func(*service.PublicCatalogProjection) string) gin.HandlerFunc {
		return func(c *gin.Context) {
			if !publicReadNoQuery(c) {
				publicReadError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
				return
			}
			p, _ := read(c)
			if p == nil {
				return
			}
			c.Header("X-Public-Release-Version", strconv.FormatInt(p.ContentReleaseVersion, 10))
			publicWriteJSON(c, p, c.FullPath(), gin.H{"document": document(p), "release_version": p.ContentReleaseVersion})
		}
	}
	g.GET("/site", func(c *gin.Context) {
		if !publicReadNoQuery(c) {
			publicReadError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		p, _ := read(c)
		if p == nil {
			return
		}
		c.Header("X-Public-Release-Version", strconv.FormatInt(p.ContentReleaseVersion, 10))
		publicWriteJSON(c, p, c.FullPath(), gin.H{"content_release_version": p.ContentReleaseVersion, "price_release_version": p.PriceReleaseVersion, "price_visibility": p.PriceVisibility.String()})
	})
	g.GET("/home", plain(func(p *service.PublicCatalogProjection) string { return p.Content.Home }))
	g.GET("/pages/about", plain(func(p *service.PublicCatalogProjection) string { return p.Content.About }))
	g.GET("/pages/terms", plain(func(p *service.PublicCatalogProjection) string { return p.Content.Terms }))
	g.GET("/pages/privacy", plain(func(p *service.PublicCatalogProjection) string { return p.Content.Privacy }))
	g.GET("/models", func(c *gin.Context) {
		q, ok := publicReadQuery(c.Request.URL.RawQuery, map[string]bool{"search": true, "provider": true, "capability": true, "endpoint_type": true, "public_display_group": true, "pricing_type": true, "sort": true, "order": true, "page": true, "page_size": true})
		if !ok {
			publicReadError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		req, ok := parsePublicCatalogQuery(q)
		if !ok {
			publicReadError(c, &service.HTTPError{Status: 400, Message: "invalid request"})
			return
		}
		p, auth := read(c)
		if p == nil {
			return
		}
		if !auth && p.PriceVisibility == models.PublicPriceVisibilityAuthenticatedOnly && (req.Sort == "input_price" || req.Sort == "output_price") {
			publicReadError(c, &service.HTTPError{Status: http.StatusUnauthorized, Message: "authentication required"})
			return
		}
		c.Header("X-Public-Release-Version", strconv.FormatInt(p.PriceReleaseVersion, 10))
		identity := fmt.Sprintf("%s|%#v", c.FullPath(), req)
		publicWriteJSON(c, p, identity, p.List(req, auth))
	})
	g.GET("/models/:modelKey", func(c *gin.Context) {
		if !publicReadNoQuery(c) || c.Request.URL.RawPath != "" || !publiccontent.ValidModelKey(c.Param("modelKey")) {
			publicReadError(c, &service.HTTPError{Status: 400, Message: "invalid model key"})
			return
		}
		p, auth := read(c)
		if p == nil {
			return
		}
		c.Header("X-Public-Release-Version", strconv.FormatInt(p.PriceReleaseVersion, 10))
		out, status := p.Detail(c.Param("modelKey"), auth)
		if status != 200 {
			publicReadError(c, &service.HTTPError{Status: status, Message: map[int]string{404: "public model not found", 410: "public model unavailable"}[status]})
			return
		}
		publicWriteJSON(c, p, c.FullPath()+"|model_key="+c.Param("modelKey"), out)
	})
}

func publicReadNoQuery(c *gin.Context) bool { return c.Request.URL.RawQuery == "" }
func publicReadHasNoBody(r *http.Request) bool {
	return r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0
}
func publicReadQuery(raw string, allowed map[string]bool) (map[string]string, bool) {
	out := map[string]string{}
	if raw == "" {
		return out, true
	}
	v, e := url.ParseQuery(raw)
	if e != nil {
		return nil, false
	}
	for k, values := range v {
		if !allowed[k] || len(values) != 1 || values[0] == "" {
			return nil, false
		}
		out[k] = values[0]
	}
	return out, true
}
func parsePublicCatalogQuery(q map[string]string) (service.PublicCatalogListRequest, bool) {
	out := service.PublicCatalogListRequest{Search: q["search"], Provider: q["provider"], Capability: q["capability"], EndpointType: q["endpoint_type"], PublicDisplayGroup: q["public_display_group"], PricingType: q["pricing_type"], Sort: q["sort"], Order: q["order"], Page: 1, PageSize: 20}
	for _, value := range []string{out.Search, out.Provider, out.Capability, out.EndpointType, out.PublicDisplayGroup, out.PricingType} {
		if len(value) > 128 || strings.TrimSpace(value) != value {
			return out, false
		}
	}
	if out.PricingType != "" && out.PricingType != "token" {
		return out, false
	}
	if out.Sort != "" && out.Sort != "default" && out.Sort != "name" && out.Sort != "input_price" && out.Sort != "output_price" {
		return out, false
	}
	if out.Order == "" {
		out.Order = "asc"
	} else if out.Order != "asc" && out.Order != "desc" {
		return out, false
	}
	if out.Sort == "" && q["order"] != "" {
		return out, false
	}
	if out.Sort == "" {
		out.Sort = "default"
	}
	if q["page"] != "" {
		v, e := strconv.Atoi(q["page"])
		if e != nil || v < 1 || strconv.Itoa(v) != q["page"] {
			return out, false
		}
		out.Page = v
	}
	if q["page_size"] != "" {
		v, e := strconv.Atoi(q["page_size"])
		if e != nil || (v != 20 && v != 50 && v != 100) || strconv.Itoa(v) != q["page_size"] {
			return out, false
		}
		out.PageSize = v
	}
	maxInt := int(^uint(0) >> 1)
	if out.Page-1 > maxInt/out.PageSize {
		return out, false
	}
	return out, true
}
func publicNotModified(c *gin.Context, etag string) bool {
	if publicIfNoneMatch(strings.Join(c.Request.Header.Values("If-None-Match"), ","), etag) {
		c.Status(http.StatusNotModified)
		return true
	}
	return false
}

func publicWriteJSON(c *gin.Context, p *service.PublicCatalogProjection, identity string, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		publicReadError(c, &service.HTTPError{Status: 503, Message: "public content unavailable"})
		return
	}
	sum := sha256.Sum256(append(append([]byte(p.ETag+"\x00"+identity+"\x00"), raw...), byte('\n')))
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	c.Header("ETag", etag)
	if publicNotModified(c, etag) {
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

func publicIfNoneMatch(raw, current string) bool {
	currentOpaque, ok := publicEntityTag(current)
	if !ok || raw == "" {
		return false
	}
	if strings.TrimSpace(raw) == "*" {
		return true
	}
	matched := false
	for _, part := range publicEntityTagMembers(raw) {
		part = strings.TrimSpace(part)
		if part == "" || part == "*" {
			continue
		}
		opaque, valid := publicEntityTag(part)
		if !valid {
			continue
		}
		if opaque == currentOpaque {
			matched = true
		}
	}
	return matched
}

func publicEntityTagMembers(raw string) []string {
	members := make([]string, 0, 1+strings.Count(raw, ","))
	start, quoted := 0, false
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				members = append(members, raw[start:i])
				start = i + 1
			}
		}
	}
	members = append(members, raw[start:])
	return members
}

func publicEntityTag(raw string) (string, bool) {
	if strings.HasPrefix(raw, "W/") {
		raw = raw[2:]
	}
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", false
	}
	body := raw[1 : len(raw)-1]
	for i := 0; i < len(body); i++ {
		if body[i] == '"' || body[i] < 0x21 || body[i] == 0x7f {
			return "", false
		}
	}
	return body, true
}
func publicReadError(c *gin.Context, err error) {
	status, message := service.StatusFromError(err)
	if status == 500 {
		status, message = 503, "public content unavailable"
	}
	code := map[int]string{400: "invalid_request", 401: "authentication_required", 404: "not_found", 410: "gone", 503: "unavailable"}[status]
	if code == "" {
		status, code, message = 503, "unavailable", "public content unavailable"
	}
	c.Writer.Header().Del("ETag")
	c.Writer.Header().Del("X-Public-Release-Version")
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": message, "request_id": c.Writer.Header().Get("X-Request-ID")}})
}
