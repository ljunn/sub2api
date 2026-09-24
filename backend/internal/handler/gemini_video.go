package handler

import (
	"context"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type geminiVideoScheduler interface {
	SelectAccountWithLoadAwareness(context.Context, *int64, string, string, map[int64]struct{}, string, int64) (*service.AccountSelectionResult, error)
}

// GeminiVideos keeps native Gemini account selection while reusing media
// concurrency, failover, task ownership, polling and completion billing.
func (h *GatewayHandler) GeminiVideos(c *gin.Context, media *OpenAIGatewayHandler, endpoint service.GrokMediaEndpoint, taskID string) {
	if h == nil || h.gatewayService == nil || media == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "Gemini video gateway is unavailable"}})
		return
	}
	media.handleGrokMedia(c, endpoint, taskID, h.gatewayService)
}
