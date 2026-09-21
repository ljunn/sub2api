//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestWuzuForwardImagesPinsModelConfiguration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := wuzuTestPolicy()
	a := siteAutomaticAccount(p)
	a.Platform, a.Type = PlatformOpenAI, AccountTypeAPIKey
	a.Credentials["api_key"] = "inference-key"
	a.Credentials["base_url"] = "https://wuzu.example.test"
	a.Credentials["model_mapping"] = map[string]any{"local-image": "gpt-image-2-web"}
	transport := &kongfangForwardTransport{response: `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: transport}
	body := []byte(`{"model":"local-image","prompt":"test","size":"1024x1024","quality":"high"}`)
	ctx := WithSitePriceRequest(context.Background(), body)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	parsed := &OpenAIImagesRequest{Endpoint: "/v1/images/generations", ContentType: "application/json", N: 1}
	require.NoError(t, parseOpenAIImagesJSONRequest(body, parsed))
	parsed.SizeTier = normalizeOpenAIImageSizeTier(parsed.Size)
	result, err := svc.ForwardImages(ctx, c, a, body, parsed, "")
	require.NoError(t, err)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 1, transport.calls)
	require.Equal(t, "gpt-image-2", gjson.GetBytes(transport.body, "model").String())
	require.Equal(t, "gpt-image-2-web", gjson.GetBytes(transport.body, "model_config_key").String())
	require.Equal(t, "high", gjson.GetBytes(transport.body, "quality").String())
	for _, blocked := range []string{
		`{"model":"local-image","prompt":"test","size":"1024x1024","async":true}`,
		`{"model":"local-image","prompt":"test","size":"1024x1024","output_width":4096,"output_height":4096}`,
		`{"model":"local-image","prompt":"test","size":"1024x1024","model_config_key":"other-config"}`,
	} {
		body = []byte(blocked)
		require.NoError(t, parseOpenAIImagesJSONRequest(body, parsed))
		_, err := svc.ForwardImages(WithSitePriceRequest(context.Background(), body), c, a, body, parsed, "")
		require.Error(t, err)
	}
	require.Equal(t, 1, transport.calls, "unpriced overrides and asynchronous task creation never reach WUZU")
}

func TestWuzuMultipartEditingPreservesImagesAndPriceSelectors(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for key, value := range map[string]string{"model": "local-image", "prompt": "merge the images", "size": "1024x1024", "quality": "high"} {
		require.NoError(t, w.WriteField(key, value))
	}
	for i, name := range []string{"first.png", "second.png"} {
		part, err := w.CreateFormFile("image[]", name)
		require.NoError(t, err)
		_, err = part.Write([]byte{0, 255, 137, byte(i), 13, 10})
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	ctx := WithSiteImageSize(context.Background(), "1024x1024")
	ctx = WithWuzuImageRequest(ctx, body.Bytes(), w.FormDataContentType())
	p := wuzuTestPolicy()
	veto, _ := SitePriceVeto(ctx, siteAutomaticAccount(p))
	require.False(t, veto)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", w.FormDataContentType())
	require.NoError(t, prepareWuzuRequest(req, p.Wuzu))
	require.NoError(t, prepareWuzuRequest(req, p.Wuzu), "preparing another network attempt preserves the request")
	raw, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, int64(len(raw)), req.ContentLength)
	_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	require.NoError(t, err)
	reader := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
	files := 0
	values := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		data, err := io.ReadAll(part)
		require.NoError(t, err)
		if part.FileName() != "" {
			require.Equal(t, []byte{0, 255, 137, byte(files), 13, 10}, data)
			files++
		} else {
			values[part.FormName()] = string(data)
		}
	}
	require.Equal(t, 2, files)
	require.Equal(t, "gpt-image-2", values["model"])
	require.Equal(t, "gpt-image-2-web", values["model_config_key"])
	require.Equal(t, "merge the images", values["prompt"])
	require.Equal(t, "high", values["quality"])
	require.Equal(t, "1024x1024", values["size"])
	for _, path := range []string{"/v1/images/edits", "/v1/responses", "/v1/chat/completions"} {
		r, _ := http.NewRequest(http.MethodPost, "https://example.test"+path, strings.NewReader(`{"size":"1024x1024"}`))
		r.Header.Set("Content-Type", "application/json")
		require.Error(t, prepareWuzuRequest(r, p.Wuzu))
	}
}
