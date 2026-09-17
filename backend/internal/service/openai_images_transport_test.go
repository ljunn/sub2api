package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type imagesTransportFailure struct {
	HTTPUpstream
	err error
}

func (u *imagesTransportFailure) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, u.err
}

func TestForwardImagesNativeTransportFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, cause := range []error{errors.New("use of closed network connection"), context.Canceled} {
		t.Run(cause.Error(), func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat"}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: &imagesTransportFailure{err: cause}}
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			account := &Account{ID: 51, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}
			_, err = svc.ForwardImages(context.Background(), c, account, body, parsed, "")
			var failover *UpstreamFailoverError
			if errors.Is(cause, context.Canceled) {
				require.NotErrorAs(t, err, &failover)
			} else {
				require.ErrorAs(t, err, &failover)
				require.False(t, failover.RetryableOnSameAccount)
			}
			require.False(t, c.Writer.Written(), "handler must own the failover response")
		})
	}
}
