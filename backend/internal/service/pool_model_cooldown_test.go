//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPoolModelErrorsDoNotPersistDefaultCooldowns(t *testing.T) {
	for _, customPolicy := range []bool{false, true} {
		for _, kind := range []string{"model_not_found", "image_rate_limit", "image_capability_loss"} {
			t.Run(kind+map[bool]string{false: "/pool", true: "/custom_policy"}[customPolicy], func(t *testing.T) {
				repo := &modelNotFoundAccountRepoStub{}
				svc := &RateLimitService{accountRepo: repo}
				account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
					Credentials: map[string]any{"pool_mode": true}}
				if customPolicy {
					account.Credentials["custom_error_codes_enabled"] = true
					account.Credentials["custom_error_codes"] = []any{float64(400), float64(404), float64(429)}
				}
				var handled bool
				switch kind {
				case "model_not_found":
					handled = svc.HandleUpstreamModelNotFound(context.Background(), account, "gpt-image-2", 404, []byte(`{"error":{"code":"model_not_found","message":"model not found"}}`))
				case "image_rate_limit":
					handled = svc.HandleOpenAIImageRateLimit(context.Background(), account, 429, http.Header{}, []byte(`{"error":{"code":"image_generation_user_error","message":"Rate limit reached for gpt-image-2. Please try again later."}}`))
				case "image_capability_loss":
					handled = svc.HandleOpenAIImageCapabilityLoss(context.Background(), account, 400, []byte(`{"error":{"message":"Tool choice 'image_generation' not found in 'tools' parameter"}}`))
				}
				require.True(t, handled, "the current request must still recognize the upstream failure")
				require.Zero(t, repo.tempCalls)
				if customPolicy {
					require.Len(t, repo.modelRateLimitCalls, 1)
				} else {
					require.Empty(t, repo.modelRateLimitCalls)
				}
			})
		}
	}
}

func TestPoolIgnoresPersistedDefaultModelCooldowns(t *testing.T) {
	for _, reason := range []string{upstreamModelNotFoundReason, openAIImageRateLimitReason, openAIImageCapabilityLossReason, "temp_unschedulable_rule", ""} {
		t.Run(reason, func(t *testing.T) {
			account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{"pool_mode": true, "model_mapping": map[string]any{"image-alias": "gpt-image-2"}}}
			scope := "gpt-image-2"
			if reason == openAIImageRateLimitReason || reason == openAIImageCapabilityLossReason {
				scope = openAIImageGenerationRateLimitKey
			}
			setAccountModelRateLimitSnapshot(account, scope, time.Now().Add(30*time.Minute), reason, time.Now())
			automatic := reason == upstreamModelNotFoundReason || reason == openAIImageRateLimitReason || reason == openAIImageCapabilityLossReason
			require.Equal(t, automatic, account.IsSchedulableForModel("image-alias"))
			require.Equal(t, !automatic, account.GetModelRateLimitRemainingTime("image-alias") > 0)
			account.Schedulable = false
			require.False(t, account.IsSchedulableForModel("image-alias"), "manual disable must still apply")
			account.Schedulable = true
			account.Credentials["pool_mode"] = false
			require.False(t, account.IsSchedulableForModel("image-alias"), "ordinary accounts retain cooldowns")
		})
	}
}

func TestPoolIgnoresEarlierInMemoryModelCooldown(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	for i := 0; i < 3; i++ {
		svc.recordOpenAIAccountModelTransientFailure(account, "gpt-image-2", time.Now())
	}
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-image-2"))
	account.Credentials = map[string]any{"pool_mode": true}
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-image-2"))
}
