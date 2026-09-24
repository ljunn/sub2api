//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiVideoRelayForwardAndImageIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := openAIFormatGrokAccount()
	a.Platform = PlatformGemini
	a.Credentials["base_url"] = "https://relay.example/proxy/v1beta"
	a.Credentials["model_mapping"] = map[string]any{"local-veo": "veo-3.1"}
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"veo-task","status":"queued"}`))}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := []byte(`{"model":"local-veo","prompt":"waves","seconds":"8","resolution":"720p","aspect_ratio":"16:9"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(body))
	result, err := svc.ForwardGrokMedia(context.Background(), c, a, GrokMediaEndpointVideosGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "https://relay.example/proxy/v1/videos", upstream.lastReq.URL.String())
	require.Equal(t, "veo-3.1", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "720p", gjson.GetBytes(upstream.lastBody, "resolution").String())
	require.Equal(t, "veo-task", result.ResponseID)
	require.Zero(t, result.VideoCount, "creation must not bill a completed video")
	require.Equal(t, PlatformGemini, a.Platform)
	_, err = svc.ForwardGrokMedia(context.Background(), c, a, GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.Error(t, err, "Gemini images must retain their native handler")
	a.Type = AccountTypeOAuth
	require.False(t, a.SupportsGeminiVideoRelay())
}

func TestGeminiVideoSiteFormatSurvivesLegacyRefresh(t *testing.T) {
	for _, endpoint := range []string{"openai-video", "openai-videos"} {
		catalog := `{"data":[{"model_name":"veo-3.1","enable_groups":["veo"],"supported_endpoint_types":["` + endpoint + `","gemini"],"quota_type":1,"model_price":0.48}],"group_ratio":{"veo":1},"usable_group":{"veo":"Veo"}}`
		models, err := parseNewAPISiteCatalog(gjson.Parse(catalog))
		require.NoError(t, err)
		require.Len(t, models, 1)
		require.Equal(t, "openai", models[0].VideoAPIFormat)
		policy := BuildSiteAccountPolicy(&UpstreamSite{Models: models}, &SiteBinding{ID: "veo", GroupID: "veo", Model: "veo-3.1"})
		a := &Account{Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{SiteBindingCredentialKey: "veo", "base_url": "https://relay.example"}}
		require.True(t, cacheSiteGrokMediaAPIFormat(a, policy))
		a.Extra = map[string]any{SitePolicyExtraKey: map[string]any{"binding_id": "veo"}}
		require.True(t, a.SupportsGeminiVideoRelay())
	}
}

func TestGeminiVideoLookupKeepsOwnerWhenCreationPaused(t *testing.T) {
	groupID := int64(24)
	a := openAIFormatGrokAccount()
	a.Platform, a.Status, a.Schedulable, a.GroupIDs = PlatformGemini, StatusActive, false, []int64{groupID}
	var acquired, released []int64
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{*a}},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{a.ID: true}, acquiredIDs: &acquired, releasedIDs: &released})}
	selection, _, err := svc.SelectGeminiVideoRequestAccount(context.Background(), &groupID, a.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, selection.Account.ID)
	require.True(t, selection.acceptedSiteVideoTask)
	selection.ReleaseFunc()
	require.Equal(t, acquired, released)
	wrongGroup := int64(25)
	_, _, err = svc.SelectGeminiVideoRequestAccount(context.Background(), &wrongGroup, a.ID)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	_, _, err = svc.SelectGeminiVideoRequestAccount(context.Background(), &groupID, a.ID+1)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
}

func TestGeminiVideoUsesGroupPricePerCompletedRequest(t *testing.T) {
	groupID := int64(17)
	price := 0.6
	key := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformGemini, Hydrated: true,
		ModelPricing: []ChannelModelPricing{{Platform: PlatformGemini, Models: []string{"veo-3.1"}, BillingMode: BillingModePerRequest, PerRequestPrice: &price}}}}
	billing := &BillingService{}
	svc := &OpenAIGatewayService{billingService: billing, resolver: NewModelPricingResolver(nil, billing)}
	for _, seconds := range []int{4, 8, 12} {
		cost := svc.calculateOpenAIVideoCost(context.Background(), "veo-3.1", key, &OpenAIForwardResult{VideoCount: 1, VideoDurationSeconds: seconds, VideoResolution: "720p"}, 1)
		require.InDelta(t, price, cost.ActualCost, 1e-12, "per-request price must not multiply duration")
	}
}

func TestGeminiVideoRelayRetryLaterAndParameterErrors(t *testing.T) {
	for _, tc := range []struct {
		body  string
		retry bool
	}{
		{`{"error":{"message":"Upstream request failed. Please retry later."}}`, true},
		{`{"error":{"message":"Invalid duration","param":"seconds"}}`, false},
		{`{"error":{"message":"Content policy violation","code":"content_policy_violation"}}`, false},
	} {
		a := openAIFormatGrokAccount()
		a.Platform = PlatformGemini
		upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}}
		svc := &OpenAIGatewayService{httpUpstream: upstream}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		body := []byte(`{"model":"veo-3.1","prompt":"waves"}`)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(body))
		_, err := svc.ForwardGrokMedia(context.Background(), c, a, GrokMediaEndpointVideosGenerations, "", body, "application/json")
		var failover *UpstreamFailoverError
		require.Equal(t, tc.retry, errors.As(err, &failover), tc.body)
	}
}
