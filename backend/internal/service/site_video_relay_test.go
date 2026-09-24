//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type siteVideoTestAdmin struct {
	siteTestAdmin
	platform string
}

func (a siteVideoTestAdmin) GetGroup(context.Context, int64) (*Group, error) {
	return &Group{ID: 9, Platform: a.platform, Status: StatusActive}, nil
}

func TestSiteVideoBindingCreatesAccountOnLocalPlatform(t *testing.T) {
	for _, tc := range []struct{ source, local, model, format string }{
		{"powerby-h3", PlatformMiniMax, "minimax-h3", SiteVideoFormatH3},
		{"wan3", PlatformOpenAI, "wan3.0-video-prime", SiteVideoFormatWan3},
	} {
		svc, _, accounts := siteTestService()
		svc.admin = siteVideoTestAdmin{platform: tc.local}
		now := time.Now()
		b := SiteBinding{ID: "pending", GroupID: "36", Model: tc.model, LocalModel: "local-video", LocalGroupID: 9, Enabled: true}
		site := &UpstreamSite{ID: "mafu", Kind: "sub2api", BaseURL: "https://relay.example", Enabled: true, LastSuccess: &now, Bindings: []SiteBinding{b}, Models: []SiteModel{{GroupID: b.GroupID, Model: tc.model, Platform: tc.source, Tiers: []SitePriceTier{{Key: "720p", Unit: "USD/second", Prices: map[string]float64{"second": .08}}}}}}
		require.NoError(t, svc.saveSecret(context.Background(), site, &SiteCredentials{Keys: map[string]string{b.ID: "test-key"}}))
		saved, err := svc.Bind(context.Background(), site.ID, b)
		require.NoError(t, err)
		account := accounts.accounts[saved.Bindings[0].AccountID]
		require.Equal(t, tc.local, account.Platform)
		require.Equal(t, tc.model, account.GetMappedModel("local-video"))
		require.Equal(t, tc.format, account.GetCredential(siteVideoAPIFormatCredentialKey))
		require.True(t, account.SupportsSiteVideoRelay())
	}
}

func TestSiteVideoRelayPriceGateUsesVideoResolution(t *testing.T) {
	pricing, groups := siteTestPricing()
	for _, tc := range []struct{ platform, format, model string }{
		{PlatformMiniMax, SiteVideoFormatH3, "minimax-h3"}, {PlatformOpenAI, SiteVideoFormatWan3, "wan3.0-video-prime"},
	} {
		groups.group.Platform = tc.platform
		price := 0.1
		groups.group.ModelPricing = []ChannelModelPricing{{Platform: tc.platform, Models: []string{tc.model}, BillingMode: BillingModeVideo, PerRequestPrice: &price}}
		p := SiteAccountPolicy{BindingID: "video", LocalGroupID: 9, LocalModel: tc.model, UpstreamModel: tc.model, VideoAPIFormat: tc.format, Enabled: true, FreshUntil: time.Now().Add(time.Hour), Tiers: []SitePriceTier{{Key: "720p", Unit: "USD/second", Prices: map[string]float64{"second": 0.08}}, {Key: "1080p", Unit: "USD/second", Prices: map[string]float64{"second": 0.4}}}}
		account := siteAutomaticAccount(p)
		account.Platform = tc.platform
		account.Type = AccountTypeAPIKey
		account.Credentials["base_url"] = "https://relay.example"
		ctx := context.WithValue(context.Background(), sitePricingKey{}, pricing)
		for _, resolution := range []string{"", "720p", "1080p"} {
			request := WithSitePriceRequest(ctx, []byte(`{"model":"`+tc.model+`","resolution":"`+resolution+`","duration":6}`))
			veto, reason := SitePriceVeto(request, account)
			require.Equal(t, resolution == "1080p", veto, "%s %s: %s", tc.model, resolution, reason)
		}
		groups.group.ModelPricing = nil
		veto, _ := SitePriceVeto(ctx, account)
		require.True(t, veto, "missing selling price must fail closed")
		accepted := ContextWithSelectionProfitGate(ctx, &AccountSelectionResult{Account: account, acceptedSiteVideoTask: true})
		veto, _ = SitePriceVeto(accepted, account)
		require.False(t, veto, "accepted task remains readable after price changes")
	}
}

func TestSiteVideoRelayContractsAndBilling(t *testing.T) {
	for _, tc := range []struct {
		platform, format, model, create, poll string
		duration                              int
	}{
		{PlatformMiniMax, SiteVideoFormatH3, "minimax-h3", "/proxy/v1/videos", "/proxy/v1/videos/task", 15},
		{PlatformOpenAI, SiteVideoFormatWan3, "wan3.0-video-prime", "/proxy/v1/videos/generations", "/proxy/v1/videos/tasks/task", 30},
	} {
		t.Run(tc.format, func(t *testing.T) {
			a := &Account{Platform: tc.platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key", "base_url": "https://relay.example/proxy/v1", GrokMediaAPIFormatCredentialKey: tc.format, "model_mapping": map[string]any{"local-video": tc.model}}}
			require.True(t, a.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySiteVideo))
			body := []byte(`{"model":"local-video","prompt":"waves","duration":` + strconv.Itoa(tc.duration) + `,"resolution":"720p","image_urls":["https://assets.example/ref.png"]}`)
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"task_id":"task","status":"queued"}`))}}
			svc := &OpenAIGatewayService{httpUpstream: upstream}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(body))
			c.Request.Header.Set("Idempotency-Key", "unique-task")
			result, err := svc.ForwardGrokMedia(context.Background(), c, a, GrokMediaEndpointVideosGenerations, "", body, "application/json")
			require.NoError(t, err)
			require.Equal(t, tc.create, upstream.lastReq.URL.Path)
			require.Equal(t, "unique-task", upstream.lastReq.Header.Get("Idempotency-Key"))
			require.Equal(t, tc.model, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, int64(tc.duration), gjson.GetBytes(upstream.lastBody, "duration").Int())
			require.Equal(t, "720P", gjson.GetBytes(upstream.lastBody, "resolution").String())
			require.Equal(t, "task", result.ResponseID)
			require.Zero(t, result.VideoCount)
			require.Equal(t, tc.duration, result.VideoDurationSeconds)
			target, err := buildGrokMediaURL(a, nil, GrokMediaEndpointVideoStatus, "task")
			require.NoError(t, err)
			require.Equal(t, "https://relay.example"+tc.poll, target)
			upstream.resp = &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"task_id":"task","status":"succeeded","duration":"` + strconv.Itoa(tc.duration) + `s","video_url":"https://assets.example/result.mp4"}`))}
			c, _ = gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/task", nil)
			result, err = svc.ForwardGrokMedia(context.Background(), c, a, GrokMediaEndpointVideoStatus, "task", nil, "")
			require.NoError(t, err)
			require.Equal(t, tc.duration, result.VideoDurationSeconds)
			require.Empty(t, result.BillingModel)
			price := 0.3
			groupID := int64(1)
			key := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Platform: tc.platform, Hydrated: true, ModelPricing: []ChannelModelPricing{{Platform: tc.platform, Models: []string{"local-video"}, BillingMode: BillingModeVideo, PerRequestPrice: &price}}}}
			billing := &BillingService{}
			svc.billingService = billing
			svc.resolver = NewModelPricingResolver(nil, billing)
			result.Model = "local-video"
			result.UpstreamModel = tc.model
			result.VideoResolution = "720p"
			cost := svc.calculateOpenAIVideoCost(context.Background(), "local-video", key, result, 1)
			require.InDelta(t, price*float64(tc.duration), cost.ActualCost, 1e-12)
		})
	}
}

func TestSiteVideoRelayRejectsInvalidParameters(t *testing.T) {
	for _, body := range []string{`{"model":"m","prompt":"p","duration":31}`, `{"model":"m","prompt":"p","duration":2.5}`, `{"model":"m","prompt":"p","duration":5,"seconds":6}`, `{"model":"m","prompt":"p","resolution":"2k"}`} {
		_, err := ParseSiteVideoRelayRequest(PlatformOpenAI, "application/json", []byte(body))
		require.Error(t, err)
	}
	_, err := ParseSiteVideoRelayRequest(PlatformMiniMax, "application/json", []byte(`{"model":"m","prompt":"p","duration":3}`))
	require.Error(t, err)
	info, err := ParseSiteVideoRelayRequest(PlatformMiniMax, "application/json", []byte(`{"model":"m","prompt":"p"}`))
	require.NoError(t, err)
	require.Equal(t, 15, info.DurationSeconds)
	require.Equal(t, "720p", info.Resolution)
	require.False(t, siteVideoPlatformCompatible("sub2api", SiteModel{Platform: "wan3", Model: "wan3.0"}, PlatformOpenAI))
}

func TestSiteVideoResultDownloadNeverForwardsGatewayCredentialsToCDN(t *testing.T) {
	for _, rawURL := range []string{"https://cdn.example/result.mp4", "https://127.0.0.1/result.mp4"} {
		a := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.example", "api_key": "private-key", GrokMediaAPIFormatCredentialKey: SiteVideoFormatWan3, "header_overrides": map[string]any{"X-Private": "private-value"}}}
		upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{
			{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"task","status":"succeeded","duration":30,"video_url":"` + rawURL + `"}`))},
			{StatusCode: 206, Header: http.Header{"Content-Type": {"video/mp4"}}, Body: io.NopCloser(strings.NewReader("video"))},
		}}
		svc := &OpenAIGatewayService{httpUpstream: upstream}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/task/content", nil)
		result, err := svc.ForwardGrokMedia(context.Background(), c, a, GrokMediaEndpointVideoContent, "task", nil, "")
		if strings.Contains(rawURL, "127.0.0.1") {
			require.Error(t, err)
			require.Equal(t, 1, upstream.callCount)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, 30, result.VideoDurationSeconds)
		require.Empty(t, result.BillingModel)
		require.Equal(t, rawURL, upstream.reqs[1].URL.String())
		require.Empty(t, upstream.reqs[1].Header.Get("Authorization"))
		require.Empty(t, upstream.reqs[1].Header.Get("X-Private"))
		require.Equal(t, "video", w.Body.String())
	}
}

func TestSiteVideoRelayPublicPriceTiers(t *testing.T) {
	for _, tc := range []struct {
		platform, model string
		prices          map[string]float64
	}{
		{"powerby-h3", "minimax-h3", map[string]float64{"720p": 0.08}},
		{"wan3", "wan3.0-video-prime", map[string]float64{"480p": 0.25, "720p": 0.30, "1080p": 0.40}},
	} {
		group := gjson.Parse(`{"id":36,"platform":"` + tc.platform + `","name":"video","rate_multiplier":1}`)
		raw := `{"name":"` + tc.model + `","pricing":{"billing_mode":"per_second","per_request_price":0.08,"intervals":[{"tier_label":"480p","per_request_price":0.25},{"tier_label":"720p","per_request_price":0.30},{"tier_label":"1080p","per_request_price":0.40}]}}`
		if tc.platform == "powerby-h3" {
			raw = `{"name":"minimax-h3","platform":"minimax-h3","pricing":{"billing_mode":"per_second","per_request_price":0.08,"intervals":[{"tier_label":"720p","per_request_price":0.08}]}}`
		}
		m, err := siteAuthenticatedChannelModel(group, gjson.Parse(raw), gjson.Parse(`{}`))
		require.NoError(t, err)
		require.Len(t, m.Tiers, len(tc.prices))
		require.NotEmpty(t, m.VideoAPIFormat)
		for _, tier := range m.Tiers {
			require.Equal(t, "USD/second", tier.Unit)
			require.InDelta(t, tc.prices[tier.Key], tier.Prices["second"], 1e-12)
		}
	}
}
