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

type geminiVideoSchedulerFunc func(context.Context, *int64, string, string, map[int64]struct{}, string, int64) (*service.AccountSelectionResult, error)

func (f geminiVideoSchedulerFunc) SelectAccountWithLoadAwareness(ctx context.Context, group *int64, session, model string, excluded map[int64]struct{}, metadata string, user int64) (*service.AccountSelectionResult, error) {
	return f(ctx, group, session, model, excluded, metadata, user)
}

func TestGeminiVideoRelayFailoverAndOwnerPolling(t *testing.T) {
	h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformGemini)
	var selected, forwarded []int64
	scheduler := geminiVideoSchedulerFunc(func(_ context.Context, group *int64, session, model string, excluded map[int64]struct{}, _ string, _ int64) (*service.AccountSelectionResult, error) {
		require.Equal(t, int64(24), *group)
		require.Equal(t, "veo-3.1", model)
		id := int64(1)
		if _, failed := excluded[id]; failed {
			id = 2
		}
		selected = append(selected, id)
		a := &service.Account{ID: id, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 50,
			GroupIDs: []int64{24}, Credentials: map[string]any{"base_url": "https://relay.example", "api_key": "test-key", service.GrokMediaAPIFormatCredentialKey: "openai"}}
		return &service.AccountSelectionResult{Account: a, WaitPlan: &service.AccountWaitPlan{AccountID: id, MaxConcurrency: 50}}, nil
	})
	upstream.call = func(req *http.Request, id int64) (*http.Response, error) {
		forwarded = append(forwarded, id)
		status, body := 200, `{"id":"task","status":"queued"}`
		if req.Method == http.MethodPost && id == 1 {
			status, body = 503, `{"error":{"message":"temporarily unavailable"}}`
		} else if req.Method == http.MethodGet {
			require.Equal(t, "/v1/videos/task", req.URL.Path)
			body = `{"id":"task","status":"pending"}`
		} else {
			require.Equal(t, "/v1/videos", req.URL.Path)
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	c, w := grokMediaSlotContext(logger.IntoContext(context.Background(), zap.NewExample()), true)
	key, _ := middleware.GetAPIKeyFromContext(c)
	key.Group.Platform = service.PlatformGemini
	c.Request.Body = io.NopCloser(strings.NewReader(`{"model":"veo-3.1","prompt":"waves","seconds":"8","resolution":"720p"}`))
	h.handleGrokMedia(c, service.GrokMediaEndpointVideosGenerations, "", scheduler)
	require.Equal(t, 200, w.Code, "response=%s selected=%v forwarded=%v", w.Body.String(), selected, forwarded)
	require.Equal(t, []int64{1, 2}, selected)
	require.Equal(t, int64(2), bindings.owner)
	require.Len(t, bindings.pending, 1)
	slots.assertReleased(t)

	c, w = grokMediaSlotContext(context.Background(), false)
	key, _ = middleware.GetAPIKeyFromContext(c)
	key.Group.Platform = service.PlatformGemini
	h.handleGrokMedia(c, service.GrokMediaEndpointVideoStatus, "task", scheduler)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, []int64{1, 2}, selected, "poll must not schedule a new account")
	require.Equal(t, []int64{1, 2, 2}, forwarded)
	slots.assertReleased(t)

	c, w = grokMediaSlotContext(context.Background(), false)
	key, _ = middleware.GetAPIKeyFromContext(c)
	key.ID++
	h.handleGrokMedia(c, service.GrokMediaEndpointVideoStatus, "task", scheduler)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Len(t, forwarded, 3, "another key cannot poll the task")
	slots.assertReleased(t)
}
