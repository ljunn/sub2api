//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func openAIFormatGrokAccount() *Account {
	return &Account{ID: 63, Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "test-key", "base_url": "https://relay.example/v1", GrokMediaAPIFormatCredentialKey: GrokMediaAPIFormatOpenAI,
			"model_mapping": map[string]any{"local-video": "grok-imagine-video-1.5", "local-image": "grok-imagine-image"}}}
}

func TestGrokOpenAIMediaForwardPreservesPlatformAndVideoBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := openAIFormatGrokAccount()
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"task_outer","status":"queued","seconds":"8"}`))}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"local-video","prompt":"waves","duration":8,"resolution":"720p","image":{"url":"https://images.example/frame.png"}}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(body))
	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideosGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, PlatformGrok, account.Platform)
	require.Equal(t, "https://relay.example/v1/videos", upstream.lastReq.URL.String())
	require.Equal(t, "grok-imagine-video-1.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "8", gjson.GetBytes(upstream.lastBody, "seconds").String())
	require.Equal(t, "720p", gjson.GetBytes(upstream.lastBody, "resolution").String())
	require.Equal(t, "https://images.example/frame.png", gjson.GetBytes(upstream.lastBody, "input_reference").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "duration").Exists())
	require.Equal(t, "task_outer", result.ResponseID)
	require.Equal(t, 8, result.VideoDurationSeconds)
	require.Equal(t, "task_outer", gjson.Get(recorder.Body.String(), "request_id").String())
	require.Equal(t, "pending", gjson.Get(recorder.Body.String(), "status").String())
	require.Zero(t, result.VideoCount, "submission alone must not charge video output")

	status := normalizeAccountGrokVideoResponse(account, GrokMediaEndpointVideoStatus, []byte(`{"id":"task_outer","status":"completed","data":{"status":"SUCCESS","data":{"id":"inner_vendor_id","video":{"url":"https://cdn.example/result.mp4","duration":8}}}}`))
	require.Equal(t, "task_outer", gjson.GetBytes(status, "request_id").String())
	pending := &GrokVideoPendingBilling{Model: "grok-imagine-video-1.5", VideoDurationSeconds: 8, VideoResolution: "720p"}
	billed := ExtractGrokVideoBillingFromStatusBody(status, pending, "task_outer")
	require.NotNil(t, billed)
	require.Equal(t, 8, billed.VideoDurationSeconds)
	require.Equal(t, "720p", billed.VideoResolution)
}

func TestGrokOpenAIMediaMultipartFirstFrameAndSeconds(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for key, value := range map[string]string{"model": "grok-imagine-video", "prompt": "waves", "seconds": "9", "resolution": "720p", "input_reference": "https://images.example/frame.png"} {
		require.NoError(t, w.WriteField(key, value))
	}
	require.NoError(t, w.Close())
	parsed := ParseGrokMediaRequest(w.FormDataContentType(), body.Bytes())
	require.Equal(t, 9, parsed.DurationSeconds)
	require.Contains(t, string(parsed.ModerationBody()), "https://images.example/frame.png")
	out, contentType, err := prepareAccountGrokMediaBody(openAIFormatGrokAccount(), GrokMediaEndpointVideosGenerations, body.Bytes(), w.FormDataContentType())
	require.NoError(t, err)
	require.Equal(t, "application/json", contentType)
	require.Equal(t, "9", gjson.GetBytes(out, "seconds").String())
	require.Equal(t, "720p", gjson.GetBytes(out, "resolution").String())
	require.Equal(t, "https://images.example/frame.png", gjson.GetBytes(out, "input_reference").String())
}

func TestAccountTestGrokOpenAIVideoUsesMappedModelAndCompletedResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := openAIFormatGrokAccount()
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{
		{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"task_outer","status":"queued"}`))},
		{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"task_outer","status":"completed","data":{"data":{"video":{"url":"https://cdn.example/video.mp4","duration":6}}}}`))},
	}}
	svc := &AccountTestService{accountRepo: &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}, httpUpstream: upstream}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
	require.NoError(t, svc.TestAccountConnection(c, account.ID, "local-video", "waves", "video"))
	require.Equal(t, 2, upstream.callCount)
	require.Equal(t, "https://relay.example/v1/videos", upstream.reqs[0].URL.String())
	require.Equal(t, "https://relay.example/v1/videos/task_outer", upstream.reqs[1].URL.String())
	require.Contains(t, recorder.Body.String(), `"video_url":"https://cdn.example/video.mp4"`)
	require.Contains(t, recorder.Body.String(), `"model":"grok-imagine-video-1.5"`)
	require.Contains(t, recorder.Body.String(), `"success":true`)
}

func TestAccountTestGrokOpenAIImageUsesSelectedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := openAIFormatGrokAccount()
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://cdn.example/image.png"}]}`))}}
	svc := &AccountTestService{accountRepo: &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}, httpUpstream: upstream}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
	require.NoError(t, svc.TestAccountConnection(c, account.ID, "local-image", "a cat", "image"))
	require.Equal(t, "https://relay.example/v1/images/generations", upstream.lastReq.URL.String())
	require.Equal(t, "grok-imagine-image", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, recorder.Body.String(), `"success":true`)
}

func TestUpstreamSiteNewAPIVideoMetadataAndUnits(t *testing.T) {
	models, err := parseNewAPISiteCatalog(gjson.Parse(`{"data":[
	{"model_name":"grok-imagine-video","model_price_type":"second","quota_type":1,"model_price":0.05,"enable_groups":["grok"],"supported_endpoint_types":["openai-videos"]},
	{"model_name":"grok-imagine-video-1.5","model_price_type":"second","quota_type":1,"model_price":0.064,"enable_groups":["grok"],"supported_endpoint_types":["openai-videos"],"billing_labels":["按像素计费"],"description":"480p and 720p prices differ"},
	{"model_name":"grok-imagine-video-1.5-preview","quota_type":1,"model_price":0.6,"enable_groups":["grok"],"supported_endpoint_types":["openai"]}
	],"group_ratio":{"grok":0.5},"usable_group":{"grok":"Grok"}}`))
	require.NoError(t, err)
	require.Len(t, models, 3)
	require.Equal(t, "USD/second", models[0].Tiers[0].Unit)
	require.Equal(t, .025, models[0].Tiers[0].Prices["second"])
	require.Empty(t, models[0].Reason)
	require.Contains(t, models[1].Reason, "分辨率")
	require.Equal(t, "USD/request", models[2].Tiers[0].Unit)
	require.Empty(t, models[2].VideoAPIFormat)
	policy := BuildSiteAccountPolicy(&UpstreamSite{Models: models}, &SiteBinding{ID: "b", GroupID: "grok", Model: models[0].Model})
	account := siteAutomaticAccount(policy)
	account.Type = AccountTypeAPIKey
	require.Equal(t, GrokMediaAPIFormatOpenAI, accountGrokMediaAPIFormat(account))
	account.Credentials[GrokMediaAPIFormatCredentialKey] = "xai"
	require.Equal(t, "xai", accountGrokMediaAPIFormat(account), "explicit format overrides the site default")
}
