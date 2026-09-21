//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type kongfangForwardTransport struct {
	HTTPUpstream
	body     []byte
	path     string
	calls    int
	response string
}

func (s *kongfangForwardTransport) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.calls++
	s.body, _ = io.ReadAll(req.Body)
	s.path = req.URL.Path
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(s.response))}, nil
}
func kongfangForwardAccount(platform string) *Account {
	p := SiteAccountPolicy{SiteKind: "kongfang", BindingID: "b", Image: true, LocalModel: "local-image", Enabled: true, FreshUntil: time.Now().Add(time.Minute)}
	for _, tier := range []string{"1K", "2K", "4K"} {
		p.Tiers = append(p.Tiers, SitePriceTier{Key: tier, Unit: "USD/image", Prices: map[string]float64{"request": .01}})
		p.Limits = append(p.Limits, SiteTierLimit{Key: tier, Unit: "USD/image", Enabled: true, Limits: map[string]float64{"request": .1}})
	}
	a := siteAutomaticAccount(p)
	a.Platform = platform
	a.Type = AccountTypeAPIKey
	a.Credentials["api_key"] = "test-key"
	a.Credentials["base_url"] = "https://example.test"
	return a
}
func TestKongfangForwardImagesNormalizesAndPreservesAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := kongfangForwardAccount(PlatformOpenAI)
	a.Credentials["model_mapping"] = map[string]any{"local-image": "gpt-image-2"}
	transport := &kongfangForwardTransport{response: `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: transport}
	body := []byte(`{"model":"local-image","prompt":"test","size":"2560x1440"}`)
	ctx := WithSitePriceRequest(context.Background(), body)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	parsed := &OpenAIImagesRequest{Endpoint: "/v1/images/generations", ContentType: "application/json", N: 1}
	err := parseOpenAIImagesJSONRequest(body, parsed)
	parsed.SizeTier = normalizeOpenAIImageSizeTier(parsed.Size)
	require.NoError(t, err)
	result, err := svc.ForwardImages(ctx, c, a, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "/v1/images/generations", transport.path)
	require.Equal(t, "2k", gjson.GetBytes(transport.body, "quality").String())
	require.Equal(t, "gpt-image-2", gjson.GetBytes(transport.body, "model").String())
	// Normalization at a mapped upstream request must keep the client alias used by pricing.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2","quality":"2k","size":"2560x1440"}`))
	require.NoError(t, prepareKongfangRequest(req))
	stored := req.Context().Value(siteRequestKey{}).(SitePriceRequest)
	require.Equal(t, "local-image", stored.Model)
}
func TestKongfangForwardGeminiFlatResolutionBillsCorrectTier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{
		`{"contents":[{"parts":[{"text":"test"}]}],"generationConfig":{"resolution":"1k","aspectRatio":"16:9"}}`,
		`{"contents":[{"parts":[{"text":"test"}]}],"generationConfig":{"imageConfig":{"imageSize":"1K","aspectRatio":"16:9"}}}`,
	} {
		transport := &kongfangForwardTransport{response: `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]},"finishReason":"STOP"}]}`}
		svc := &GeminiMessagesCompatService{httpUpstream: transport, cfg: &config.Config{}}
		a := kongfangForwardAccount(PlatformGemini)
		c, _ := newGeminiNativeTestContext(t)
		result, err := svc.ForwardNative(context.Background(), c, a, "gemini-3.1-flash-image", "generateContent", false, []byte(body))
		require.NoError(t, err)
		require.Equal(t, "1K", result.ImageSize)
		require.Equal(t, 1, result.ImageCount)
		require.Equal(t, "1k", gjson.GetBytes(transport.body, "generationConfig.resolution").String())
		require.Equal(t, "16:9", gjson.GetBytes(transport.body, "generationConfig.aspectRatio").String())
	}
}
