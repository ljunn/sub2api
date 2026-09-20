//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerSnapshotPreservesMediaProtocolEligibility(t *testing.T) {
	for _, tc := range []struct {
		name         string
		extra        map[string]any
		capabilities []string
		seedance     bool
	}{
		{"vividai", map[string]any{service.AccountExtraVividAI: true}, nil, true},
		{"longxia", map[string]any{service.AccountExtraLongXia: true}, nil, true},
		{"native_ark", nil, []string{"seedance"}, true},
		{"chat_only", nil, []string{"chat_completions"}, false},
		{"conflicting_protocols", map[string]any{service.AccountExtraVividAI: true, service.AccountExtraLongXia: true}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cache := newSchedulerCacheUnit(t)
			account := service.Account{
				ID: 24, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Status: "active", Schedulable: true, GroupIDs: []int64{9}, Extra: tc.extra,
				Credentials: map[string]any{
					"base_url": "https://media.example/v1", "api_key": "test-key",
					"model_mapping":       map[string]any{"video": "upstream-video"},
					"openai_capabilities": tc.capabilities,
				},
			}
			bucket := service.SchedulerBucket{GroupID: 9, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))
			candidates, hit, err := cache.GetSnapshot(ctx, bucket)
			require.NoError(t, err)
			require.True(t, hit)
			require.Len(t, candidates, 1)
			candidate := candidates[0]
			require.Equal(t, tc.seedance, candidate.SupportsOpenAIEndpointCapability(service.OpenAIEndpointCapabilitySeedance))
			for _, capability := range []service.OpenAIEndpointCapability{"", service.OpenAIEndpointCapabilityResponses, service.OpenAIEndpointCapabilityChatCompletions, service.OpenAIEndpointCapabilityEmbeddings} {
				require.Equal(t, account.SupportsOpenAIEndpointCapability(capability), candidate.SupportsOpenAIEndpointCapability(capability), "capability %s changed after caching", capability)
			}
			require.Equal(t, account.IsVividAI(), candidate.IsVividAI())
			require.Equal(t, account.IsLongXia(), candidate.IsLongXia())
			require.Equal(t, "upstream-video", candidate.GetMappedModel("video"))
		})
	}
}
