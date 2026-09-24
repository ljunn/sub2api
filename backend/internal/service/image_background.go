package service

import (
	"context"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/tidwall/gjson"
)

const ImagesTransparentBackgroundSupportedExtraKey = "images_transparent_background_supported"

type imageBackgroundContextKey struct{}

// Missing settings preserve existing routing. Only an explicit false opts an
// account out; this is an operator declaration, not an upstream capability probe.
func (a *Account) SupportsTransparentBackground() bool {
	if a == nil {
		return false
	}
	supported, configured := a.Extra[ImagesTransparentBackgroundSupportedExtraKey].(bool)
	return !configured || supported
}

func WithImageBackground(ctx context.Context, background string) context.Context {
	return context.WithValue(ctx, imageBackgroundContextKey{}, strings.EqualFold(strings.TrimSpace(background), "transparent"))
}

// Responses declares image options on the hosted tool. Do not infer transparency
// from prompts, PNG/WebP output, input alpha channels, or arbitrary tool schemas.
func WithResponsesImageBackground(ctx context.Context, body []byte) context.Context {
	background := ""
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get("type").String() == "image_generation" && strings.EqualFold(strings.TrimSpace(tool.Get("background").String()), "transparent") {
			background = "transparent"
			break
		}
	}
	return WithImageBackground(ctx, background)
}

func ImageBackgroundRequestAllowed(ctx context.Context, account *Account) bool {
	if ctx == nil {
		return true
	}
	required, _ := ctx.Value(imageBackgroundContextKey{}).(bool)
	return !required || account.SupportsTransparentBackground()
}

// A final guard also covers internal callers that bypass scheduling. Preserve
// the requested background; never silently turn a transparent request opaque.
func checkImageBackgroundBeforeSend(ctx context.Context, account *Account) error {
	if ImageBackgroundRequestAllowed(ctx, account) {
		return nil
	}
	return &UpstreamFailoverError{StatusCode: 503, ResponseBody: []byte(`{"error":{"type":"api_error","message":"Selected account does not support transparent image backgrounds"}}`)}
}

func validateImageBackgroundExtra(extra map[string]any) error {
	if value, exists := extra[ImagesTransparentBackgroundSupportedExtraKey]; exists {
		if _, ok := value.(bool); !ok {
			return infraerrors.BadRequest("INVALID_IMAGE_BACKGROUND_SUPPORT", "透明背景支持必须为 true 或 false")
		}
	}
	return nil
}
