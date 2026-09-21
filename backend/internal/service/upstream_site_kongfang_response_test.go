//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const kongfangProgressFixture = `data: {"candidates":[{"content":{"parts":[{"text":"[keepalive] 5% 3s"}]},"index":0}]}` + "\n\n"

func TestKongfangNativeEngineFailureSwitchesBeforeResponseCommit(t *testing.T) {
	engineError := `{"candidates":[{"content":{"parts":[{"text":"GPT Image 引擎暂不可用，请稍后重试"}]},"finishReason":"STOP"}]}`
	for _, tc := range []struct {
		name, body, contentType string
		stream, failover        bool
	}{
		{"text engine error", kongfangProgressFixture + "data: " + engineError + "\n\n", "text/event-stream", true, true},
		{"structured error", kongfangProgressFixture + "data: " + `{"error":{"code":503,"status":"UNAVAILABLE","message":"busy"}}` + "\n\n", "text/event-stream", true, true},
		{"non-stream error", engineError, "application/json", false, true},
		{"progress without result", kongfangProgressFixture, "text/event-stream", true, true},
		{"content refusal", kongfangProgressFixture + "data: " + `{"candidates":[{"finishReason":"IMAGE_SAFETY","finishMessage":"blocked"}]}` + "\n\n", "text/event-stream", true, false},
		{"parameter error", "data: " + `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"bad size"}}` + "\n\n", "text/event-stream", true, false},
		{"image result", kongfangProgressFixture + "data: " + `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]},"finishReason":"STOP"}]}` + "\n\n", "text/event-stream", true, false},
		{"quoted message", "data: " + `{"candidates":[{"content":{"parts":[{"text":"Example: GPT Image 引擎暂不可用，请稍后重试"}]},"finishReason":"STOP"}]}` + "\n\n", "text/event-stream", true, false},
		{"error after real output", "data: " + `{"candidates":[{"content":{"parts":[{"text":"real output"}]}}]}` + "\n\ndata: " + engineError + "\n\n", "text/event-stream", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newGeminiSignalService(tc.contentType, tc.body)
			c, recorder := newGeminiNativeTestContext(t)
			action := "generateContent"
			if tc.stream {
				action = "streamGenerateContent"
			}
			result, err := svc.ForwardNative(context.Background(), c, kongfangForwardAccount(PlatformGemini), "gemini-3.1-flash-image-preview", action, tc.stream, geminiSignalTestRequest())
			if tc.failover {
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.False(t, failover.RetryableOnSameAccount)
				require.Equal(t, 503, failover.StatusCode)
				require.Nil(t, result, "failed image must not reach usage billing")
				require.Empty(t, recorder.Body.String())
				require.False(t, c.Writer.Written())
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, tc.body, recorder.Body.String())
			}
		})
	}
}

type kongfangResponseCloseTracker struct {
	io.Reader
	closed bool
}

func (r *kongfangResponseCloseTracker) Close() error { r.closed = true; return nil }

func TestKongfangResponsePrefixPreservesLargeImageAndBodyClose(t *testing.T) {
	body := kongfangProgressFixture + fmt.Sprintf("data: {\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"data\":%q}}]}}]}\n\n", strings.Repeat("a", 2<<20))
	source := &kongfangResponseCloseTracker{Reader: strings.NewReader(body)}
	resp := &http.Response{Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: source}
	require.NoError(t, prepareKongfangGeminiResponse(resp))
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(got))
	require.NoError(t, resp.Body.Close())
	require.True(t, source.closed)
}

func TestKongfangResponseReadErrorIsNotHidden(t *testing.T) {
	source := &errReadCloser{err: errors.New("interrupted")}
	resp := &http.Response{Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: source}
	require.ErrorContains(t, prepareKongfangGeminiResponse(resp), "interrupted")
}
