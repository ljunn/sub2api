package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Connection checks only discover visible models; they never create paid jobs.
func (s *AccountTestService) testLongXiaAccount(c *gin.Context, account *Account) error {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Writer.Flush()
	s.sendEvent(c, TestEvent{Type: "test_start", Model: "LongXia"})
	base, err := s.validateUpstreamBaseURL(account.GetCredential("base_url"))
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid LongXia base URL")
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, buildOpenAIEndpointURL(base, "/v1/models"), nil)
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid LongXia models URL")
	}
	account.ApplyHeaderOverrides(req.Header)
	req.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		return s.sendErrorAndEnd(c, "LongXia models request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s.sendErrorAndEnd(c, fmt.Sprintf("LongXia models HTTP %d", resp.StatusCode))
	}
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&models); err != nil || models.Data == nil {
		return s.sendErrorAndEnd(c, "Invalid LongXia models response")
	}
	s.sendEvent(c, TestEvent{Type: "content", Text: fmt.Sprintf("LongXia connection OK; %d visible models. No generation submitted.", len(models.Data))})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}
