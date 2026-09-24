//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type imageBackgroundUpstream struct {
	service.HTTPUpstream
	calls []int64
}

type imageBackgroundAccountRepo struct {
	openAIImagesFailoverAccountRepo
}

func (r imageBackgroundAccountRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]service.Account, error) {
	return r.accounts, nil
}

func (u *imageBackgroundUpstream) Do(_ *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	u.calls = append(u.calls, id)
	// Fail every eligible attempt, so the test covers the complete failover pool.
	return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(`{"error":{"message":"temporarily unavailable"}}`))}, nil
}

func TestImagesTransparentBackgroundRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformGrok} {
		for _, kind := range []string{"generation", "json_edit", "multipart_edit", "opaque", "auto", "omitted", "all_disabled"} {
			t.Run(platform+"/"+kind, func(t *testing.T) {
				accounts := []service.Account{}
				for id := int64(1); id <= 4; id++ {
					accounts = append(accounts, service.Account{ID: id, Name: "candidate", Platform: platform, Type: service.AccountTypeAPIKey,
						Status: service.StatusActive, Schedulable: true, Priority: int(id),
						Credentials: map[string]any{"api_key": "test", "pool_mode": true, "pool_mode_retry_count": 0},
						Extra:       map[string]any{service.ImagesTransparentBackgroundSupportedExtraKey: id%2 == 0 && kind != "all_disabled"}})
				}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				upstream := &imageBackgroundUpstream{}
				repo := imageBackgroundAccountRepo{openAIImagesFailoverAccountRepo{accounts: accounts}}
				gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
				billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				t.Cleanup(billing.Stop)
				h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
				h.maxAccountSwitches = 5
				if kind == "generation" || kind == "json_edit" || kind == "multipart_edit" {
					// Ineligible channels must not consume the one permitted failover.
					h.maxAccountSwitches = 1
				}
				model := "gpt-image-2"
				if platform == service.PlatformGrok {
					model = "grok-imagine-image"
				}
				background := "transparent"
				if kind == "opaque" || kind == "auto" {
					background = kind
				}
				path, contentType := "/v1/images/generations", "application/json"
				body := []byte(`{"model":"` + model + `","prompt":"draw","background":"` + background + `"}`)
				if kind == "omitted" {
					body = []byte(`{"model":"` + model + `","prompt":"transparent sticker","output_format":"png"}`)
				}
				if kind == "json_edit" {
					path = "/v1/images/edits"
					body = []byte(`{"model":"` + model + `","prompt":"draw","background":"transparent","images":[{"image_url":"https://example.test/input.png"}]}`)
				}
				if kind == "multipart_edit" {
					path = "/v1/images/edits"
					var buf bytes.Buffer
					w := multipart.NewWriter(&buf)
					require.NoError(t, w.WriteField("model", model))
					require.NoError(t, w.WriteField("prompt", "draw"))
					require.NoError(t, w.WriteField("background", "transparent"))
					part, err := w.CreateFormFile("image", "input.png")
					require.NoError(t, err)
					_, err = part.Write([]byte("test-image"))
					require.NoError(t, err)
					require.NoError(t, w.Close())
					body, contentType = buf.Bytes(), w.FormDataContentType()
				}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", contentType)
				groupID := int64(3130)
				c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 99, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: platform, AllowImageGeneration: true}, User: &service.User{ID: 100}})
				c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100})
				if platform == service.PlatformGrok {
					h.GrokImages(c)
				} else {
					h.Images(c)
				}
				switch kind {
				case "all_disabled":
					require.Empty(t, upstream.calls, rec.Body.String())
					require.Equal(t, 503, rec.Code, rec.Body.String())
				case "opaque", "auto", "omitted":
					require.Equal(t, []int64{1, 2, 3, 4}, upstream.calls, rec.Body.String())
				default:
					require.Equal(t, []int64{2, 4}, upstream.calls, rec.Body.String())
				}
			})
		}
	}
}
