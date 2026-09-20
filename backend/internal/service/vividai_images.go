package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func vividAIImageRequest(parsed *OpenAIImagesRequest, model string) (vividAIRequest, error) {
	request := vividAIRequest{Model: model, Prompt: parsed.Prompt, Quality: "1K", Ratio: "1:1"}
	if parsed.N != 1 || parsed.Stream || parsed.HasMask || parsed.OutputFormat != "" || parsed.OutputCompression != nil || parsed.PartialImages != nil || parsed.InputFidelity != "" || parsed.Style != "" || parsed.Moderation != "" || (parsed.Background != "" && parsed.Background != "auto") {
		return request, vividAIInvalid("VividAI supports n=1, non-streaming images without masks, background, output format or other native image options")
	}
	if parsed.ResponseFormat != "" && parsed.ResponseFormat != "url" && parsed.ResponseFormat != "b64_json" {
		return request, vividAIInvalid("response_format must be url or b64_json")
	}
	if parsed.Size != "" && parsed.Size != "auto" {
		tier, ok := ClassifyImageBillingTier(parsed.Size)
		if !ok {
			return request, vividAIInvalid("VividAI size must be 1K, 2K, 4K or WIDTHxHEIGHT")
		}
		request.Quality = tier
		if w, h, ok := parseImageBillingDimensions(parsed.Size); ok {
			a, b := w, h
			for b != 0 {
				a, b = b, a%b
			}
			request.Ratio = fmt.Sprintf("%d:%d", w/a, h/a)
		}
	}
	// OpenAI quality is a rendering effort, not resolution. Preserve the size
	// selected by image2api; explicit VividAI resolution tiers take precedence.
	switch strings.ToLower(parsed.Quality) {
	case "", "auto", "low", "medium", "high", "standard", "hd":
	case "1k", "2k", "4k":
		request.Quality = strings.ToUpper(parsed.Quality)
	default:
		return request, vividAIInvalid("unsupported VividAI image quality")
	}
	return request, validateVividAIRequest(request)
}

func (s *OpenAIGatewayService) forwardVividAIImages(ctx context.Context, c *gin.Context, account *Account, parsed *OpenAIImagesRequest, mappedModel string) (*OpenAIForwardResult, error) {
	started := time.Now()
	model := parsed.Model
	if strings.TrimSpace(mappedModel) != "" {
		model = mappedModel
	}
	upstreamModel := account.GetMappedModel(model)
	request, err := vividAIImageRequest(parsed, upstreamModel)
	if err != nil {
		return s.vividAIForwardError(ctx, c, account, err, false)
	}
	upstreamCtx, release := detachUpstreamContext(ctx)
	defer release()
	upstreamCtx, cancel := context.WithTimeout(upstreamCtx, 2*time.Hour)
	defer cancel()
	for _, upload := range parsed.Uploads {
		key, err := s.vividAIUpload(upstreamCtx, account, "image", upload.Data)
		if err != nil {
			return s.vividAIForwardError(upstreamCtx, c, account, err, true)
		}
		request.Refs = append(request.Refs, key)
	}
	for _, reference := range parsed.InputImageURLs {
		data, err := s.vividAIReference(upstreamCtx, account, reference)
		if err != nil {
			return s.vividAIForwardError(upstreamCtx, c, account, err, false)
		}
		key, err := s.vividAIUpload(upstreamCtx, account, "image", data)
		if err != nil {
			return s.vividAIForwardError(upstreamCtx, c, account, err, true)
		}
		request.Refs = append(request.Refs, key)
	}
	frame, err := s.vividAIWait(upstreamCtx, account, request)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		return s.vividAIForwardError(upstreamCtx, c, account, err, frame.JobID == "")
	}
	if frame.Status != "succeeded" {
		message := frame.Error
		if message == "" {
			message = "VividAI generation failed"
		}
		return s.vividAIForwardError(upstreamCtx, c, account, &vividAIError{Code: "generation_failed", Message: message}, false)
	}
	item := gin.H{}
	if parsed.ResponseFormat == "url" {
		item["url"] = frame.Result
	} else {
		encoded, err := s.fetchOpenAIImageURLBase64(upstreamCtx, account, frame.Result)
		if err != nil {
			return s.vividAIForwardError(upstreamCtx, c, account, err, false)
		}
		item["b64_json"] = encoded
	}
	StopOpenAIImagesJSONKeepaliveCommitted(c)
	c.JSON(http.StatusOK, gin.H{"created": time.Now().Unix(), "data": []any{item}})
	return &OpenAIForwardResult{RequestID: frame.JobID, ResponseID: frame.JobID, Model: model, BillingModel: model, UpstreamModel: upstreamModel, Duration: time.Since(started), ImageCount: 1, ImageSize: request.Quality, ImageInputSize: request.Quality}, nil
}
