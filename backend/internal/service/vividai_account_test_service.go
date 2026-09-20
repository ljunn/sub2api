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

// Account connection tests query balance and do not submit a paid generation.
func (s *AccountTestService) testVividAIAccount(c *gin.Context, account *Account) error {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Writer.Flush()
	s.sendEvent(c, TestEvent{Type: "test_start", Model: "VividAI"})
	base, err := s.validateUpstreamBaseURL(account.GetCredential("base_url"))
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid VividAI base URL")
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, buildOpenAIEndpointURL(base, "/v1/balance"), nil)
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid VividAI balance URL")
	}
	account.ApplyHeaderOverrides(req.Header)
	req.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		return s.sendErrorAndEnd(c, "VividAI balance request failed")
	}
	defer resp.Body.Close()
	var balance struct {
		Object           string  `json:"object"`
		Balance          float64 `json:"balance"`
		Running          int64   `json:"running"`
		ConcurrencyLimit int64   `json:"concurrency_limit"`
	}
	if resp.StatusCode != http.StatusOK {
		return s.sendErrorAndEnd(c, fmt.Sprintf("VividAI balance HTTP %d", resp.StatusCode))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&balance); err != nil || balance.Object != "user.balance" {
		return s.sendErrorAndEnd(c, "Invalid VividAI balance response")
	}
	s.sendEvent(c, TestEvent{Type: "content", Text: fmt.Sprintf("VividAI balance: %g credits; running: %d; concurrency limit: %d. No generation submitted.", balance.Balance, balance.Running, balance.ConcurrencyLimit)})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}
