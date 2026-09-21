//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const kongfangTestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="

func kongfangMultipartRequest(t *testing.T, fields [][2]string, mask bool) ([]byte, *OpenAIImagesRequest, *gin.Context) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, field := range append([][2]string{{"model", "gpt-image-2.5-sunburst"}, {"prompt", "  preserve reference\nadd triangle  "}, {"n", "1"}, {"stream", "false"}}, fields...) {
		require.NoError(t, w.WriteField(field[0], field[1]))
	}
	data, err := base64.StdEncoding.DecodeString(kongfangTestPNG)
	require.NoError(t, err)
	for _, name := range []string{"image[]", "image[]"} {
		part, err := w.CreateFormFile(name, "reference.png") // application/octet-stream
		require.NoError(t, err)
		_, err = part.Write(data)
		require.NoError(t, err)
	}
	if mask {
		part, err := w.CreateFormFile("mask", "mask.png")
		require.NoError(t, err)
		_, err = part.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, openAIImagesEditsEndpoint, bytes.NewReader(body.Bytes()))
	c.Request.Header.Set("Content-Type", w.FormDataContentType())
	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body.Bytes())
	require.NoError(t, err)
	return body.Bytes(), parsed, c
}

func TestKongfangMultipartPricingPreservesPriceProtection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name                 string
		fields               [][2]string
		mask                 bool
		wantTier, wantReason string
	}{
		{"production 4K edit", [][2]string{{"size", "5464x3072"}}, false, "4K", ""},
		{"explicit purchase tier", [][2]string{{"size", "1024x1024"}, {"resolution", "4k"}}, false, "4K", ""},
		{"conflicting selectors", [][2]string{{"size", "1024x1024"}, {"resolution", "2k"}, {"quality", "4k"}}, false, "unknown", "site_price_unknown"},
		{"duplicate selector", [][2]string{{"size", "1024x1024"}, {"quality", "2k"}, {"quality", "4k"}}, false, "unknown", "site_price_unknown"},
		{"unsupported mask", [][2]string{{"size", "1024x1024"}}, true, "1K", "site_endpoint_unsupported"},
		{"unpriced fast tier", [][2]string{{"size", "1024x1024"}, {"service_tier", "priority"}}, false, "1K", "site_price_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, parsed, _ := kongfangMultipartRequest(t, tc.fields, tc.mask)
			ctx := WithSiteImageSize(WithSitePriceRequest(context.Background(), body), parsed.Size)
			ctx = WithKongfangImageRequest(ctx, body, parsed)
			request := ctx.Value(siteRequestKey{}).(SitePriceRequest)
			require.Equal(t, tc.wantTier, request.KongfangTier)
			require.Equal(t, parsed.Model, request.Model)
			veto, reason := SitePriceVeto(ctx, kongfangForwardAccount(PlatformOpenAI))
			require.Equal(t, tc.wantReason, reason)
			require.Equal(t, tc.wantReason != "", veto)
		})
	}
	// A valid parsed edit still cannot bypass a purchase price above its cap.
	body, parsed, _ := kongfangMultipartRequest(t, [][2]string{{"size", "1024x1024"}, {"quality", "4k"}}, false)
	ctx := WithKongfangImageRequest(WithSiteImageSize(context.Background(), parsed.Size), body, parsed)
	a := kongfangForwardAccount(PlatformOpenAI)
	p, _ := a.SitePolicy()
	p.Limits[0].Limits["request"] = .005
	veto, reason := SitePriceVeto(ctx, siteAutomaticAccount(p))
	require.True(t, veto)
	require.Equal(t, "site_price_exceeded", reason)
}

func TestKongfangForwardMultipartEditsPreservesImagesAndBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body, parsed, c := kongfangMultipartRequest(t, [][2]string{{"size", "5464x3072"}}, false)
	original := append([]byte(nil), body...)
	a := kongfangForwardAccount(PlatformOpenAI)
	a.Credentials["model_mapping"] = map[string]any{parsed.Model: "gpt-image-2.5-flare"}
	transport := &kongfangForwardTransport{response: `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: transport}
	result, err := svc.ForwardImages(WithSitePriceRequest(context.Background(), body), c, a, body, parsed, "")
	require.NoError(t, err)
	require.Equal(t, 1, transport.calls)
	require.Equal(t, openAIImagesGenerationsEndpoint, transport.path)
	require.Equal(t, openAIImagesGenerationsEndpoint, GetActualOpenAIUpstreamEndpoint(c))
	require.Equal(t, "4k", gjson.GetBytes(transport.body, "quality").String())
	require.Equal(t, "gpt-image-2.5-flare", gjson.GetBytes(transport.body, "model").String())
	require.Equal(t, "  preserve reference\nadd triangle  ", gjson.GetBytes(transport.body, "prompt").String())
	require.Equal(t, gjson.Number, gjson.GetBytes(transport.body, "n").Type)
	require.Equal(t, gjson.False, gjson.GetBytes(transport.body, "stream").Type)
	images := gjson.GetBytes(transport.body, "image_urls").Array()
	require.Len(t, images, 2)
	for _, img := range images {
		require.Equal(t, "data:image/png;base64,"+kongfangTestPNG, img.String())
	}
	require.Equal(t, "5464x3072", result.ImageInputSize)
	require.Equal(t, "4K", result.ImageSize)
	require.Equal(t, original, body)
	require.True(t, parsed.Multipart)
	require.Equal(t, openAIImagesEditsEndpoint, parsed.Endpoint)
	// A subsequent failover to an ordinary account receives the original form.
	other := &Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}}
	_, err = svc.ForwardImages(context.Background(), c, other, body, parsed, "")
	require.NoError(t, err)
	require.Equal(t, openAIImagesEditsEndpoint, transport.path)
	require.Equal(t, openAIImagesEditsEndpoint, GetActualOpenAIUpstreamEndpoint(c))
	require.False(t, gjson.ValidBytes(transport.body))
}

func TestKongfangForwardJSONEditsPreservesReferencesAndRetryPricing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"edit","size":"1024x1024","quality":"4k","images":[{"image_url":"https://example.test/reference.png"},{"image_url":"data:image/png;base64,abc"}]}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, openAIImagesEditsEndpoint, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	ctx := WithKongfangImageRequest(WithSitePriceRequest(context.Background(), body), body, parsed)
	forward, err := kongfangImagesForwardBody(body, parsed, "gpt-image-2")
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(forward, "images").Exists())
	require.Len(t, gjson.GetBytes(forward, "image_urls").Array(), 2)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/v1/images/generations", bytes.NewReader(forward))
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, prepareKongfangRequest(req))
		data, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Len(t, gjson.GetBytes(data, "image_urls").Array(), 2)
		price := req.Context().Value(siteRequestKey{}).(SitePriceRequest)
		require.Equal(t, "4K", price.KongfangTier)
		require.Equal(t, "1K", price.KongfangBillingTier)
	}
}
