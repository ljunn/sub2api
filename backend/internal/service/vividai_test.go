//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type vividAIUpstream struct {
	HTTPUpstream
	call func(*http.Request) (*http.Response, error)
}

func (u *vividAIUpstream) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.call(r)
}
func vividAIAccount() *Account {
	return &Account{ID: 91, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{AccountExtraVividAI: true}, Credentials: map[string]any{"api_key": "vk-test", "base_url": "https://vivid.example/prefix/v1", "model_mapping": map[string]any{"gpt-image-2": "upstream-image", "video": "upstream-video"}}}
}
func vividAIResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestVividAIImagesUploadResumeAndDownload(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	body := []byte(fmt.Sprintf(`{"model":"gpt-image-2","prompt":"cat","size":"2048x1152","quality":"high","images":[{"image_url":"data:image/png;base64,%s"}],"response_format":"b64_json"}`, base64.StdEncoding.EncodeToString(png)))
	calls, creates, uploads := 0, 0, 0
	upstream := &vividAIUpstream{call: func(req *http.Request) (*http.Response, error) {
		calls++
		var raw []byte
		if req.Body != nil {
			raw, _ = io.ReadAll(req.Body)
		}
		if req.URL.Host == "cdn.example" {
			require.Empty(t, req.Header.Get("Authorization"))
			if req.Method == http.MethodPut {
				uploads++
				require.Equal(t, "image/png", req.Header.Get("Content-Type"))
				require.Equal(t, png, raw)
				return vividAIResponse(200, ""), nil
			}
			require.Equal(t, http.MethodGet, req.Method)
			return vividAIResponse(200, string(png)), nil
		}
		require.Equal(t, "Bearer vk-test", req.Header.Get("Authorization"))
		switch req.URL.Path {
		case "/prefix/v1/uploads":
			require.JSONEq(t, `{"kind":"image","mime":"image/png"}`, string(raw))
			return vividAIResponse(200, fmt.Sprintf(`{"key":"user/refs/a.png","url":"https://cdn.example/a.png?signature=x","expiresAt":%d}`, time.Now().Add(time.Hour).Unix())), nil
		case "/prefix/v1/generate":
			require.Equal(t, "120", req.Header.Get("X-Wait-Seconds"))
			if gjson.GetBytes(raw, "jobId").String() == "" {
				creates++
				require.Equal(t, "upstream-image", gjson.GetBytes(raw, "model").String())
				require.Equal(t, "2K", gjson.GetBytes(raw, "quality").String())
				require.Equal(t, "16:9", gjson.GetBytes(raw, "ratio").String())
				require.Equal(t, "user/refs/a.png", gjson.GetBytes(raw, "refs.0").String())
				return vividAIResponse(200, "\n{\"jobId\":\"task-1\",\"status\":\"running\"}\n\n"), nil
			}
			require.JSONEq(t, `{"jobId":"task-1"}`, string(raw))
			return vividAIResponse(200, "\n{\"jobId\":\"task-1\",\"status\":\"running\"}\n{\"jobId\":\"task-1\",\"status\":\"succeeded\",\"result\":\"https://cdn.example/result.png\",\"creditsCharged\":10}\n"), nil
		}
		t.Fatalf("unexpected request %s", req.URL)
		return nil, nil
	}}
	svc := newOpenAIImagesTestService(upstream)
	c, rec := newOpenAIImagesTestContext(t, body)
	c.Request.URL.Path = "/v1/images/edits"
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	result, err := svc.ForwardImages(context.Background(), c, vividAIAccount(), body, parsed, "")
	require.NoError(t, err)
	require.Equal(t, 1, creates)
	require.Equal(t, 1, uploads)
	require.Equal(t, 5, calls)
	require.Equal(t, base64.StdEncoding.EncodeToString(png), gjson.Get(rec.Body.String(), "data.0.b64_json").String())
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, "2K", result.ImageSize)
	require.Zero(t, result.Usage.OutputTokens)
}

func TestVividAIErrorRetryBoundaries(t *testing.T) {
	for _, tt := range []struct {
		code     string
		status   int
		failover bool
	}{
		{"4011", 401, true}, {"4002", 400, true}, {"4293", 429, true}, {"4001", 400, false}, {"5001", 500, false}, {"4011", 500, false}, {"", 502, false},
	} {
		t.Run(tt.code+fmt.Sprint(tt.status), func(t *testing.T) {
			calls := 0
			svc := newOpenAIImagesTestService(&vividAIUpstream{call: func(req *http.Request) (*http.Response, error) {
				calls++
				return vividAIResponse(tt.status, fmt.Sprintf(`{"error":{"code":%q,"message":"upstream refusal"}}`, tt.code)), nil
			}})
			body := []byte(`{"model":"gpt-image-2","prompt":"cat"}`)
			c, rec := newOpenAIImagesTestContext(t, body)
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			_, err = svc.ForwardImages(context.Background(), c, vividAIAccount(), body, parsed, "")
			require.Error(t, err)
			var failover *UpstreamFailoverError
			if tt.failover {
				require.ErrorAs(t, err, &failover)
				require.Empty(t, rec.Body.String())
			} else {
				require.NotErrorAs(t, err, &failover)
				require.Contains(t, rec.Body.String(), "upstream refusal")
			}
			require.Equal(t, 1, calls)
		})
	}
}

func TestVividAIInventoryRefusalIsUnavailableWithoutResubmission(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		status         int
		code           string
	}{
		{"inventory", `{"error":{"code":"4001","message":"该渠道暂无可用账号，请稍后再试"}}`, http.StatusServiceUnavailable, "upstream_capacity_unavailable"},
		{"parameters", `{"error":{"code":"4001","message":"quality 不在白名单"}}`, http.StatusBadRequest, "4001"},
		{"receipt", `{"jobId":"already-accepted","error":{"code":"4001","message":"该渠道暂无可用账号，请稍后再试"}}`, http.StatusBadRequest, "4001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			svc := newOpenAIImagesTestService(&vividAIUpstream{call: func(req *http.Request) (*http.Response, error) {
				calls++
				return vividAIResponse(http.StatusBadRequest, tc.response), nil
			}})
			body := []byte(`{"model":"video","content":[{"type":"text","text":"waves"}],"resolution":"720p","duration":15}`)
			c, rec := newOpenAIImagesTestContext(t, body)
			_, err := svc.ForwardSeedance(context.Background(), c, vividAIAccount(), SeedanceEndpointCreate, "", body)
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
			require.Equal(t, 1, calls)
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, tc.code, gjson.Get(rec.Body.String(), "error.code").String())
		})
	}
}

func TestVividAIAmbiguousCreateAndAcceptedFailureNeverFailover(t *testing.T) {
	for _, response := range []string{"", `{"jobId":"j","status":"failed","error":"policy rejected","refunded":true}`, `{"jobId":"j","status":"succeeded","result":"https://cdn.example/result.png"}`} {
		t.Run(response, func(t *testing.T) {
			calls := 0
			svc := newOpenAIImagesTestService(&vividAIUpstream{call: func(req *http.Request) (*http.Response, error) {
				calls++
				if response == "" || req.Method == http.MethodGet {
					return nil, io.ErrUnexpectedEOF
				}
				return vividAIResponse(200, response), nil
			}})
			body := []byte(`{"model":"gpt-image-2","prompt":"cat"}`)
			c, _ := newOpenAIImagesTestContext(t, body)
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			_, err = svc.ForwardImages(context.Background(), c, vividAIAccount(), body, parsed, "")
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
			if strings.Contains(response, "succeeded") {
				require.Equal(t, 2, calls)
			} else {
				require.Equal(t, 1, calls)
			}
		})
	}
}

type vividAIBrokenReader struct{}

func (*vividAIBrokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestVividAIStreamValidation(t *testing.T) {
	for _, body := range []string{`{}`, `{"jobId":"a","status":"unknown"}`, `{"jobId":"a","status":"succeeded"}`, "{\"jobId\":\"a\",\"status\":\"running\"}\n{\"jobId\":\"b\",\"status\":\"failed\"}", `{"jobId":"../x","status":"running"}`} {
		_, err := readVividAIStream(strings.NewReader(body), "", false)
		require.Error(t, err, body)
	}
	frame, err := readVividAIStream(io.MultiReader(strings.NewReader("{\"jobId\":\"a\",\"status\":\"running\"}\n"), &vividAIBrokenReader{}), "", false)
	require.Error(t, err)
	require.Equal(t, "a", frame.JobID)
}
func TestVividAIValidationBeforeNetwork(t *testing.T) {
	for _, extra := range []string{`"n":2`, `"stream":true`, `"background":"transparent"`, `"size":"bad"`, `"quality":"best"`, `"output_format":"jpeg"`} {
		body := []byte(`{"model":"gpt-image-2","prompt":"cat",` + extra + `}`)
		svc := newOpenAIImagesTestService(&vividAIUpstream{call: func(*http.Request) (*http.Response, error) { t.Fatal("must not create job"); return nil, nil }})
		c, rec := newOpenAIImagesTestContext(t, body)
		parsed, err := svc.ParseOpenAIImagesRequest(c, body)
		require.NoError(t, err)
		_, err = svc.ForwardImages(context.Background(), c, vividAIAccount(), body, parsed, "")
		require.Error(t, err)
		require.Equal(t, 400, rec.Code)
	}
	account := vividAIAccount()
	require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySeedance))
	require.True(t, account.SupportsOpenAIImageCapability(OpenAIImagesCapabilityNative))
	for _, capability := range []OpenAIEndpointCapability{OpenAIEndpointCapabilityResponses, OpenAIEndpointCapabilityChatCompletions, OpenAIEndpointCapabilityEmbeddings} {
		require.False(t, account.SupportsOpenAIEndpointCapability(capability))
	}
}
func TestVividAIReferenceSafetyAndMime(t *testing.T) {
	svc := newOpenAIImagesTestService(&vividAIUpstream{call: func(*http.Request) (*http.Response, error) {
		t.Fatal("must not request private hosts")
		return nil, nil
	}})
	for _, url := range []string{"http://127.0.0.1/a", "https://169.254.169.254/latest", "https://localhost/a"} {
		_, err := svc.vividAIReference(context.Background(), vividAIAccount(), url)
		require.Error(t, err)
	}
	_, err := vividAIMime("image", []byte("GIF89a123456"))
	require.Error(t, err)
	_, err = vividAIMime("audio", []byte("not audio"))
	require.Error(t, err)
	_, err = svc.vividAIReference(context.Background(), vividAIAccount(), "data:image/png;base64,invalid!")
	require.Error(t, err)
}

func TestVividAIVideoCreateStatusAndBilling(t *testing.T) {
	calls := 0
	svc := newOpenAIImagesTestService(&vividAIUpstream{call: func(req *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(req.Body)
		require.Equal(t, "/prefix/v1/generate", req.URL.Path)
		require.Equal(t, "1", req.Header.Get("X-Wait-Seconds"))
		if calls == 1 {
			require.JSONEq(t, `{"model":"upstream-video","prompt":"waves","quality":"720p","duration":5}`, string(raw))
			return vividAIResponse(200, "{\"jobId\":\"v1\",\"status\":\"running\"}\n"), nil
		}
		require.JSONEq(t, `{"jobId":"v1"}`, string(raw))
		return vividAIResponse(200, "{\"jobId\":\"v1\",\"status\":\"running\"}\n{\"jobId\":\"v1\",\"status\":\"succeeded\",\"result\":\"https://cdn.example/v.mp4\",\"creditsCharged\":7}"), nil
	}})
	body := []byte(`{"model":"video","content":[{"type":"text","text":"waves"}],"resolution":"720p","duration":5}`)
	c, rec := newOpenAIImagesTestContext(t, body)
	result, err := svc.ForwardSeedance(context.Background(), c, vividAIAccount(), SeedanceEndpointCreate, "", body)
	require.NoError(t, err)
	require.Equal(t, "seedance:vividai:v1", result.ResponseID)
	require.JSONEq(t, `{"id":"vividai:v1"}`, rec.Body.String())
	require.Zero(t, result.Usage.OutputTokens)
	c, rec = newOpenAIImagesTestContext(t, nil)
	result, err = svc.ForwardSeedance(context.Background(), c, vividAIAccount(), SeedanceEndpointStatus, "seedance:vividai:v1", nil)
	require.NoError(t, err)
	require.Equal(t, 7000, result.Usage.OutputTokens)
	require.Zero(t, result.VideoCount)
	require.Equal(t, "https://cdn.example/v.mp4", gjson.Get(rec.Body.String(), "content.video_url").String())
	require.Equal(t, "credit_equivalent", gjson.Get(rec.Body.String(), "billing.unit").String())
	require.False(t, gjson.Get(rec.Body.String(), "billing.measured_tokens").Bool())
	require.Equal(t, 2, calls)
}
func TestVividAIVideoRejectsUnsupportedSemantics(t *testing.T) {
	for _, body := range []string{
		`{"model":"v","content":[{"type":"text","text":"a"}],"resolution":"720p","duration":-1}`,
		`{"model":"v","content":[{"type":"text","text":"a"}],"resolution":"720p","generate_audio":true}`,
		`{"model":"v","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"https://cdn.example/a"},"role":"first_frame"}],"resolution":"720p"}`,
	} {
		_, _, err := vividAIVideoRequest([]byte(body), "upstream")
		require.Error(t, err)
	}
}
func TestVividAIAccountTestUsesBalanceOnly(t *testing.T) {
	svc := &AccountTestService{cfg: &config.Config{}, httpUpstream: &vividAIUpstream{call: func(req *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, "/prefix/v1/balance", req.URL.Path)
		require.Equal(t, "Bearer vk-test", req.Header.Get("Authorization"))
		return vividAIResponse(200, `{"object":"user.balance","balance":980,"running":1,"concurrency_limit":3}`), nil
	}}}
	c, rec := newOpenAIImagesTestContext(t, nil)
	require.NoError(t, svc.testOpenAIAccountConnection(c, vividAIAccount(), "gpt-image-2", "cat", ""))
	require.Contains(t, rec.Body.String(), "980")
	require.Contains(t, rec.Body.String(), "No generation submitted")
	require.Contains(t, rec.Body.String(), "test_complete")
}
