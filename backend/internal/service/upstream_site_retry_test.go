//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUpstreamSiteGeminiDoesNotRetryFailedGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, protocol := range []string{"native", "messages", "chat"} {
		for _, failure := range []string{"429", "503", "transport", "400"} {
			t.Run(protocol+"/"+failure, func(t *testing.T) {
				status := http.StatusTooManyRequests
				if failure == "503" {
					status = http.StatusServiceUnavailable
				} else if failure == "400" {
					status = http.StatusBadRequest
				}
				svc, upstream := newGeminiSkippedWriteService(status, `{"error":{"message":"engine unavailable"}}`)
				if failure == "transport" {
					upstream.err = io.ErrUnexpectedEOF
				}
				account := siteAutomaticAccount(siteAutomaticPolicy())
				account.Type, account.Platform = AccountTypeAPIKey, PlatformGemini
				account.Credentials["api_key"] = "test-key"
				account.Credentials["pool_mode"] = true
				account.Credentials["pool_mode_retry_count"] = 0
				pricing, _ := siteTestPricing()
				ctx := context.WithValue(context.Background(), sitePricingKey{}, pricing)
				c, rec := newGeminiNativeTestContext(t)
				var err error
				switch protocol {
				case "native":
					_, err = svc.ForwardNative(ctx, c, account, "gemini-2.5-flash", "generateContent", false,
						[]byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
				case "messages":
					_, err = svc.Forward(ctx, c, account, []byte(`{"model":"gemini-2.5-flash","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
				case "chat":
					_, err = svc.ForwardAsChatCompletions(ctx, c, account, []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}]}`))
				}
				require.Error(t, err)
				require.Equal(t, 1, upstream.calls, "failed generation must only reach this account once")
				var failover *UpstreamFailoverError
				if failure == "400" {
					require.False(t, errors.As(err, &failover))
					require.Equal(t, http.StatusBadRequest, rec.Code)
				} else {
					require.ErrorAs(t, err, &failover)
					require.Zero(t, rec.Body.Len(), "leave the response unwritten so the handler can switch accounts")
					require.Zero(t, account.GetPoolModeRetryCount())
				}
			})
		}
	}
}

func TestUpstreamSiteRetryDefaultAndExplicitOverride(t *testing.T) {
	account := &Account{Type: AccountTypeAPIKey, Credentials: map[string]any{"pool_mode": true}}
	require.Equal(t, 3, account.GetPoolModeRetryCount(), "standalone account defaults are preserved")
	require.Equal(t, geminiMaxRetries, geminiUpstreamMaxAttempts(account))
	account.Credentials[SiteBindingCredentialKey] = "binding"
	require.Zero(t, account.GetPoolModeRetryCount(), "legacy site bindings without a setting default to no retries")
	require.Equal(t, 1, geminiUpstreamMaxAttempts(account))
	account.Credentials["pool_mode_retry_count"] = 2
	require.Equal(t, 2, account.GetPoolModeRetryCount(), "an explicit administrator override remains available")
	require.Equal(t, geminiMaxRetries, geminiUpstreamMaxAttempts(account))
}
