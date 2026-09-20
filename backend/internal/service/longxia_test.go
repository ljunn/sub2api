//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const longXiaTestModel = "LongXia-video-seedance2_5-standard-720p-express-PerSecond"

func longXiaTestAccount() *Account {
	return &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{AccountExtraLongXia: true}, Credentials: map[string]any{
		"base_url": "https://api8.longxiaai.store", "api_key": "test-key", "model_mapping": map[string]any{"video": longXiaTestModel},
	}}
}

func longXiaTestPricing(mode BillingMode) *ModelPricingResolver {
	cache := newEmptyChannelCache()
	price := 0.2
	cache.pricingByGroupModel[channelModelKey{groupID: 24, model: "video"}] = &ChannelModelPricing{BillingMode: mode, PerRequestPrice: &price}
	cache.channelByGroupID[24] = &Channel{ID: 24, Status: StatusActive}
	cache.groupPlatform[24] = ""
	cache.loadedAt = time.Now()
	cs := &ChannelService{}
	cs.cache.Store(cache)
	return NewModelPricingResolver(cs, NewBillingService(&config.Config{}, nil))
}

func TestLongXiaRequestConversion(t *testing.T) {
	body := []byte(`{"model":"video","content":[{"type":"text","text":"Animate @image1 with @audio1 and @video1"},{"type":"image_url","image_url":{"url":"https://example.com/ref.png"},"role":"reference_image"},{"type":"audio_url","audio_url":{"url":"data:audio/mpeg;base64,SUQz"}},{"type":"video_url","video_url":{"url":"https://example.com/ref.mp4"}}],"duration":15,"ratio":"16:9","resolution":"720p","generate_audio":true}`)
	p, caps, err := parseLongXiaRequest(body, "LongXia-video-seedance2_5-standard-720p-reference-video-express")
	require.NoError(t, err)
	require.Equal(t, "720p", caps.resolution)
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"LongXia-video-seedance2_5-standard-720p-reference-video-express","prompt":"Animate @image1 with @audio1 and @video1","size":"16:9","duration":15,"assets":[{"category":"image","url":"https://example.com/ref.png"},{"category":"audio","data_base64":"SUQz"},{"category":"video","url":"https://example.com/ref.mp4"}]}`, string(raw))
	p, _, err = parseLongXiaRequest([]byte(`{"model":"video","content":[{"type":"text","text":"waves"}]}`), longXiaTestModel)
	require.NoError(t, err)
	require.Equal(t, 8, p.Duration)
	require.Equal(t, "9:16", p.Size)
}

func TestLongXiaRejectsUnsupportedRequests(t *testing.T) {
	base := `{"model":"video","content":[{"type":"text","text":"waves"}]}`
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"seconds alias", "seconds", 4}, {"quality", "quality", "high"}, {"count", "n", 1}, {"typo", "duraton", 8},
		{"fraction", "duration", 4.5}, {"string duration", "duration", "8"}, {"null duration", "duration", nil}, {"too long", "duration", 26},
		{"resolution mismatch", "resolution", "1080p"}, {"audio disabled", "generate_audio", false}, {"invalid ratio", "ratio", "auto"},
		{"missing marker", "content", []any{map[string]any{"type": "text", "text": "waves"}, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "https://example.com/a.png"}}}},
		{"missing reference", "content", []any{map[string]any{"type": "text", "text": "@image1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := sjson.Set(base, tc.field, tc.value)
			require.NoError(t, err)
			_, _, err = parseLongXiaRequest([]byte(body), longXiaTestModel)
			require.Error(t, err)
		})
	}
	for _, source := range []string{"http://example.com/a.png", "https://127.0.0.1/a.png", "https://user:pass@example.com/a.png", "data:audio/wav;base64,SUQz", "data:audio/mpeg;base64,???"} {
		_, err := longXiaReference("audio", source)
		require.Error(t, err, source)
	}
	_, _, err := parseLongXiaRequest([]byte(base), "LongXia-video-gemini-omni-flash-express")
	require.Error(t, err)
	_, _, err = parseLongXiaRequest([]byte(base), "unknown")
	require.Error(t, err)
	for _, prompt := range []string{"@image2", "@image01", "@image0", "@image1 @image3"} {
		body := `{"model":"video","content":[{"type":"text","text":"` + prompt + `"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}},{"type":"image_url","image_url":{"url":"https://example.com/b.png"}}]}`
		_, _, err = parseLongXiaRequest([]byte(body), longXiaTestModel)
		require.Error(t, err, prompt)
	}
}

func TestLongXiaForwardLifecycle(t *testing.T) {
	for _, base := range []string{"https://api8.longxiaai.store", "https://api8.longxiaai.store/v1/"} {
		upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"task_id":"task_1","status":"queued","progress":0}`)}
		svc := &OpenAIGatewayService{httpUpstream: upstream, resolver: longXiaTestPricing(BillingModeVideo)}
		account := longXiaTestAccount()
		account.Credentials["base_url"] = base
		c, w := grokMediaContentTestContext(http.MethodPost, "/api/v3/contents/generations/tasks", nil)
		c.Set("api_key", &APIKey{Group: &Group{ID: 24}})
		result, err := svc.ForwardSeedance(context.Background(), c, account, SeedanceEndpointCreate, "", []byte(`{"model":"video","duration":25,"content":[{"type":"text","text":"waves"}]}`))
		require.NoError(t, err)
		require.Equal(t, "seedance:longxia:task_1", result.ResponseID)
		require.Equal(t, 25, result.VideoDurationSeconds)
		require.Zero(t, result.VideoCount)
		require.Equal(t, "longxia:task_1", gjson.Get(w.Body.String(), "id").String())
		require.Equal(t, "https://api8.longxiaai.store/v1/videos", upstream.request.URL.String())
		require.Equal(t, "Bearer test-key", upstream.request.Header.Get("Authorization"))
		raw, err := io.ReadAll(upstream.request.Body)
		require.NoError(t, err)
		require.Equal(t, longXiaTestModel, gjson.GetBytes(raw, "model").String())
		for state, expected := range map[string]string{"queued": "queued", "in_progress": "running", "completed": "succeeded", "failed": "failed", "cancelled": "cancelled"} {
			upstream.response = grokMediaContentStatusResponse(`{"id":"task_1","status":"` + state + `","data":[{"url":"https://media.longxiaai.store/video.mp4","media_type":"video/mp4","width":1280,"height":720}],"error":{"code":"generation_failed","message":"Task failed"}}`)
			c, w = grokMediaContentTestContext(http.MethodGet, "/api/v3/contents/generations/tasks/longxia:task_1", nil)
			result, err = svc.ForwardSeedance(context.Background(), c, account, SeedanceEndpointStatus, "seedance:longxia:task_1", nil)
			require.NoError(t, err)
			require.Equal(t, expected, gjson.Get(w.Body.String(), "status").String())
			require.Equal(t, "/v1/videos/task_1", upstream.request.URL.Path)
			require.Zero(t, result.Usage.OutputTokens)
			if state == "completed" {
				require.Equal(t, 1, result.VideoCount)
				require.Equal(t, "https://media.longxiaai.store/video.mp4", gjson.Get(w.Body.String(), "content.video_url").String())
			} else {
				require.Zero(t, result.VideoCount)
			}
		}
	}
}

func TestLongXiaResponseValidation(t *testing.T) {
	for _, raw := range []string{`{}`, `{"id":"a/b","status":"queued"}`, `{"id":"other","status":"completed"}`, `{"id":"task_1","task_id":"other","status":"queued"}`, `{"id":"task_1","status":"unknown"}`, `{"id":"task_1","status":"completed","data":[]}`, `{"id":"task_1","status":"completed","data":[{"url":"https://media.longxiaai.store/result.jpg","media_type":"image/jpeg"}]}`} {
		_, err := normalizeLongXiaResponse([]byte(raw), SeedanceEndpointStatus, "seedance:longxia:task_1", &OpenAIForwardResult{})
		require.Error(t, err, raw)
	}
}

func TestLongXiaAccountConnectionOnlyListsModels(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"object":"list","data":[{"id":"LongXia-video-seedance2_5-standard-720p-express"}]}`)}
	svc := &AccountTestService{cfg: &config.Config{}, httpUpstream: upstream}
	c, w := grokMediaContentTestContext(http.MethodGet, "/test", nil)
	require.NoError(t, svc.testOpenAIAccountConnection(c, longXiaTestAccount(), "video", "waves", ""))
	require.Len(t, upstream.requests, 1)
	require.Equal(t, http.MethodGet, upstream.request.Method)
	require.Equal(t, "https://api8.longxiaai.store/v1/models", upstream.request.URL.String())
	require.Contains(t, w.Body.String(), "No generation submitted")
	require.Contains(t, w.Body.String(), "test_complete")
}

func TestLongXiaReceiptOnErrorNeverFailsOver(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"task_id":"task_accepted","error":{"code":"limited","message":"limit reached"}}`)}
	upstream.response.StatusCode = http.StatusTooManyRequests
	svc := &OpenAIGatewayService{httpUpstream: upstream, resolver: longXiaTestPricing(BillingModeVideo)}
	c, w := grokMediaContentTestContext(http.MethodPost, "/api/v3/contents/generations/tasks", nil)
	c.Set("api_key", &APIKey{Group: &Group{ID: 24}})
	_, err := svc.ForwardSeedance(context.Background(), c, longXiaTestAccount(), SeedanceEndpointCreate, "", []byte(`{"model":"video","content":[{"type":"text","text":"waves"}]}`))
	var failover *UpstreamFailoverError
	require.Error(t, err)
	require.NotErrorAs(t, err, &failover)
	require.Equal(t, 429, w.Code)
	require.Len(t, upstream.requests, 1)
}

func TestLongXiaErrorAndCapabilityGates(t *testing.T) {
	a := longXiaTestAccount()
	require.True(t, a.SupportsOpenAIImageCapability(""))
	require.True(t, a.SupportsOpenAIEndpointCapability(""))
	require.True(t, a.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySeedance))
	for _, capability := range []OpenAIEndpointCapability{OpenAIEndpointCapabilityResponses, OpenAIEndpointCapabilityChatCompletions, OpenAIEndpointCapabilityEmbeddings} {
		require.False(t, a.SupportsOpenAIEndpointCapability(capability))
	}
	require.False(t, a.SupportsOpenAIImageCapability(OpenAIImagesCapabilityBasic))
	a.Extra["vividai_enabled"] = true
	require.False(t, a.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySeedance))
	delete(a.Extra, "vividai_enabled")
	for _, status := range []int{400, 401, 403, 429, 500, 502, 503} {
		upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"error":{"code":"test","message":"rejected"}}`)}
		upstream.response.StatusCode = status
		svc := &OpenAIGatewayService{httpUpstream: upstream, resolver: longXiaTestPricing(BillingModeVideo)}
		c, w := grokMediaContentTestContext(http.MethodPost, "/api/v3/contents/generations/tasks", nil)
		c.Set("api_key", &APIKey{Group: &Group{ID: 24}})
		_, err := svc.ForwardSeedance(context.Background(), c, a, SeedanceEndpointCreate, "", []byte(`{"model":"video","content":[{"type":"text","text":"waves"}]}`))
		require.Error(t, err)
		require.Len(t, upstream.requests, 1)
		var failover *UpstreamFailoverError
		if status == 401 || status == 403 || status == 429 {
			require.ErrorAs(t, err, &failover)
		} else {
			require.NotErrorAs(t, err, &failover)
			require.Equal(t, status, w.Code)
		}
	}
	upstream := &grokMediaContentUpstreamStub{}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	for _, endpoint := range []GrokMediaEndpoint{SeedanceEndpointCreate, SeedanceEndpointDelete} {
		c, _ := grokMediaContentTestContext(http.MethodPost, "/api/v3/contents/generations/tasks", nil)
		_, err := svc.ForwardSeedance(context.Background(), c, a, endpoint, "seedance:longxia:task_1", []byte(`{"model":"video","content":[{"type":"text","text":"waves"}]}`))
		require.Error(t, err)
	}
	require.Empty(t, upstream.requests)
}

func TestLongXiaConfiguredBillingPreservesDurationAndResolution(t *testing.T) {
	for _, mode := range []BillingMode{BillingModePerRequest, BillingModeVideo} {
		for _, resolution := range []string{"480p", "720p", "1440p"} {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			svc.resolver = longXiaTestPricing(mode)
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{Model: "video", BillingModel: "video", UpstreamModel: longXiaTestModel, ResponseID: "seedance:longxia:task_1", RequestID: "poll-1", VideoCount: 1, VideoDurationSeconds: 25, VideoResolution: resolution},
				APIKey: &APIKey{ID: 10, GroupID: i64p(24), Group: &Group{ID: 24, RateMultiplier: 1}}, User: &User{ID: 20}, Account: longXiaTestAccount(),
			})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			expected := 0.2
			if mode == BillingModeVideo {
				expected = 5
			}
			require.InDelta(t, expected, usageRepo.lastLog.TotalCost, 1e-9)
			require.Equal(t, 25, *usageRepo.lastLog.VideoDurationSeconds)
			require.Equal(t, resolution, *usageRepo.lastLog.VideoResolution)
			require.True(t, strings.Contains(usageRepo.lastLog.RequestID, "seedance:longxia:task_1"))
			require.Zero(t, usageRepo.lastLog.OutputTokens)
		}
	}
}
