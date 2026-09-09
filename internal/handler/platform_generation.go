package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/service"
)

const (
	platformGenerationInvalidCode     = "invalid_request"
	platformGenerationNotFoundCode    = "generation_not_found"
	platformGenerationUnavailableCode = "generation_status_unavailable"

	platformGenerationInvalidMessage     = "Invalid request."
	platformGenerationNotFoundMessage    = "Generation not found."
	platformGenerationUnavailableMessage = "Generation status is temporarily unavailable."

	platformGenerationInvalidType = "invalid_request_error"
	platformGenerationAPIType     = "api_error"
)

type platformGenerationPublicError struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

func platformGenerationNoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

func platformGenerationGet(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		generationID := c.Param("generation_id")
		if !isCanonicalUUID(generationID) {
			platformGenerationError(c, http.StatusBadRequest, platformGenerationInvalidCode, platformGenerationInvalidMessage, platformGenerationInvalidType)
			return
		}
		if state == nil || state.PlatformGenerationControl == nil {
			platformGenerationUnavailable(c)
			return
		}
		user := middleware.CurrentUser(c)
		if user == nil || user.ID <= 0 {
			platformGenerationUnavailable(c)
			return
		}
		view, err := state.PlatformGenerationControl.Get(c.Request.Context(), user.ID, generationID)
		if err != nil {
			platformGenerationServiceError(c, err, true)
			return
		}
		c.JSON(http.StatusOK, view)
	}
}

func platformGenerationCancel(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		generationID := c.Param("generation_id")
		if !isCanonicalUUID(generationID) {
			platformGenerationError(c, http.StatusBadRequest, platformGenerationInvalidCode, platformGenerationInvalidMessage, platformGenerationInvalidType)
			return
		}
		if state == nil || state.PlatformGenerationControl == nil {
			platformGenerationUnavailable(c)
			return
		}
		user := middleware.CurrentUser(c)
		if user == nil || user.ID <= 0 {
			platformGenerationUnavailable(c)
			return
		}
		view, pending, err := state.PlatformGenerationControl.Cancel(c.Request.Context(), user.ID, generationID)
		if err != nil {
			platformGenerationServiceError(c, err, false)
			return
		}
		if pending {
			if view.Status != "cancelling" && view.Status != "committing" {
				platformGenerationUnavailable(c)
				return
			}
			c.Header("Retry-After", "1")
			c.JSON(http.StatusAccepted, view)
			return
		}
		if view.Status != "cancelled" && view.Status != "completed" && view.Status != "failed" {
			platformGenerationUnavailable(c)
			return
		}
		c.JSON(http.StatusOK, view)
	}
}

func platformGenerationServiceError(c *gin.Context, err error, allowNotFound bool) {
	if errors.Is(err, service.ErrPlatformGenerationControlInvalid) {
		platformGenerationError(c, http.StatusBadRequest, platformGenerationInvalidCode, platformGenerationInvalidMessage, platformGenerationInvalidType)
		return
	}
	if allowNotFound && errors.Is(err, service.ErrPlatformGenerationControlNotFound) {
		platformGenerationError(c, http.StatusNotFound, platformGenerationNotFoundCode, platformGenerationNotFoundMessage, platformGenerationInvalidType)
		return
	}
	platformGenerationUnavailable(c)
}

func platformGenerationUnavailable(c *gin.Context) {
	platformGenerationError(c, http.StatusServiceUnavailable, platformGenerationUnavailableCode, platformGenerationUnavailableMessage, platformGenerationAPIType)
}

func platformGenerationError(c *gin.Context, status int, code, message, errorType string) {
	var response platformGenerationPublicError
	response.Error.Code = code
	response.Error.Message = message
	response.Error.Type = errorType
	response.Error.RequestID = c.Writer.Header().Get("X-Request-ID")
	c.AbortWithStatusJSON(status, response)
}
