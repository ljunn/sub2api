//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestSiteVideoSchedulerFailoverAndTaskOwnership(t *testing.T) {
	for _, tc := range []struct{ platform, format, model, create, poll string }{
		{service.PlatformMiniMax, service.SiteVideoFormatH3, "minimax-h3", "/v1/videos", "/v1/videos/task"},
		{service.PlatformOpenAI, service.SiteVideoFormatWan3, "wan3.0-video-prime", "/v1/videos/generations", "/v1/videos/tasks/task"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, tc.platform, tc.format)
			var forwarded []int64
			upstream.call = func(req *http.Request, id int64) (*http.Response, error) {
				forwarded = append(forwarded, id)
				code, body := 200, `{"id":"task","status":"queued"}`
				if req.Method == http.MethodPost {
					require.Equal(t, tc.create, req.URL.Path)
					if id == 1 {
						code, body = 503, `{"error":{"message":"temporarily unavailable"}}`
					}
				} else {
					require.Equal(t, tc.poll, req.URL.Path)
					body = `{"id":"task","status":"running"}`
				}
				return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			}
			c, w := grokMediaSlotContext(logger.IntoContext(context.Background(), zap.NewExample()), true)
			key, _ := middleware.GetAPIKeyFromContext(c)
			key.Group.Platform = tc.platform
			c.Request.Body = io.NopCloser(strings.NewReader(`{"model":"` + tc.model + `","prompt":"waves","duration":6,"resolution":"720p"}`))
			h.handleGrokMedia(c, service.GrokMediaEndpointVideosGenerations, "")
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, []int64{1, 2}, forwarded)
			require.Equal(t, int64(2), bindings.owner)
			require.Len(t, bindings.pending, 1)
			slots.assertReleased(t)
			c, w = grokMediaSlotContext(context.Background(), false)
			key, _ = middleware.GetAPIKeyFromContext(c)
			key.Group.Platform = tc.platform
			h.handleGrokMedia(c, service.GrokMediaEndpointVideoStatus, "task")
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, []int64{1, 2, 2}, forwarded)
			slots.assertReleased(t)
			c, w = grokMediaSlotContext(context.Background(), false)
			key, _ = middleware.GetAPIKeyFromContext(c)
			key.Group.Platform = tc.platform
			key.ID++
			h.handleGrokMedia(c, service.GrokMediaEndpointVideoStatus, "task")
			require.Equal(t, http.StatusNotFound, w.Code)
			require.Len(t, forwarded, 3)
			slots.assertReleased(t)
		})
	}
}
