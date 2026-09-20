package service

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type vividAIReferenceURL struct{ Kind, URL string }

func vividAIVideoRequest(body []byte, upstreamModel string) (vividAIRequest, []vividAIReferenceURL, error) {
	request := vividAIRequest{Model: upstreamModel}
	info, err := ParseSeedanceRequest(body)
	if err != nil {
		return request, nil, vividAIInvalid(err.Error())
	}
	request.Prompt = info.Prompt
	request.Quality = strings.TrimSpace(gjson.GetBytes(body, "resolution").String())
	duration := gjson.GetBytes(body, "duration")
	if duration.Exists() {
		if duration.Type != gjson.Number || duration.Float() != float64(duration.Int()) || duration.Int() <= 0 {
			return request, nil, vividAIInvalid("VividAI duration must be a positive integer")
		}
		request.Duration = duration.Int()
	}
	var unsupported string
	gjson.ParseBytes(body).ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "model", "content", "resolution", "duration", "ratio":
		default:
			unsupported = key.String()
		}
		return unsupported == ""
	})
	if unsupported != "" {
		return request, nil, vividAIInvalid("VividAI does not support video field: " + unsupported)
	}
	var refs []vividAIReferenceURL
	for _, item := range gjson.GetBytes(body, "content").Array() {
		kind := strings.TrimSuffix(item.Get("type").String(), "_url")
		switch kind {
		case "text":
			continue
		case "image", "video", "audio":
		default:
			return request, nil, vividAIInvalid("unsupported VividAI reference type")
		}
		role := item.Get("role").String()
		if role != "" && role != "reference_"+kind {
			return request, nil, vividAIInvalid("VividAI refs do not support first/last frame roles")
		}
		rawURL := strings.TrimSpace(item.Get(kind + "_url.url").String())
		if rawURL == "" {
			return request, nil, vividAIInvalid("reference URL is required")
		}
		refs = append(refs, vividAIReferenceURL{Kind: kind, URL: rawURL})
	}
	return request, refs, validateVividAIRequest(request)
}

// The Ark task binding/ownership and completion billing remain in the existing
// handler. Create returns the first receipt; status resumes that exact job with
// a short wait. No in-memory worker or additional task database is required.
func (s *OpenAIGatewayService) forwardVividAIVideo(ctx context.Context, c *gin.Context, account *Account, endpoint GrokMediaEndpoint, taskID string, body []byte) (*OpenAIForwardResult, error) {
	started := time.Now()
	if endpoint == SeedanceEndpointDelete {
		return s.vividAIForwardError(ctx, c, account, &vividAIError{Status: http.StatusMethodNotAllowed, Code: "unsupported_operation", Message: "VividAI does not provide task deletion or cancellation"}, false)
	}
	if endpoint != SeedanceEndpointCreate && !strings.HasPrefix(taskID, "seedance:vividai:") {
		return s.vividAIForwardError(ctx, c, account, vividAIInvalid("invalid VividAI task ID"), false)
	}
	request := vividAIRequest{JobID: strings.TrimPrefix(taskID, "seedance:vividai:")}
	model, upstreamModel := "", ""
	if endpoint == SeedanceEndpointCreate {
		model = gjson.GetBytes(body, "model").String()
		upstreamModel = account.GetMappedModel(model)
		parsed, refs, err := vividAIVideoRequest(body, upstreamModel)
		if err != nil {
			return s.vividAIForwardError(ctx, c, account, err, false)
		}
		request = parsed
		for _, reference := range refs {
			data, err := s.vividAIReference(ctx, account, reference.URL)
			if err != nil {
				return s.vividAIForwardError(ctx, c, account, err, false)
			}
			key, err := s.vividAIUpload(ctx, account, reference.Kind, data)
			if err != nil {
				return s.vividAIForwardError(ctx, c, account, err, true)
			}
			request.Refs = append(request.Refs, key)
		}
	} else if endpoint != SeedanceEndpointStatus || request.JobID == "" {
		return s.vividAIForwardError(ctx, c, account, vividAIInvalid("invalid VividAI task operation"), false)
	}
	if request.JobID != "" {
		if err := validateUpstreamPathSegment("VividAI jobId", request.JobID); err != nil {
			return s.vividAIForwardError(ctx, c, account, vividAIInvalid(err.Error()), false)
		}
	}
	// Do not cancel an accepted create just because the downstream disconnects.
	upstreamCtx, release := detachUpstreamContext(ctx)
	defer release()
	upstreamCtx, cancel := context.WithTimeout(upstreamCtx, 45*time.Second)
	defer cancel()
	resp, err := s.vividAIPost(upstreamCtx, account, "generate", request, 1)
	if err != nil {
		return s.vividAIForwardError(ctx, c, account, err, endpoint == SeedanceEndpointCreate)
	}
	defer resp.Body.Close()
	frame, err := readVividAIStream(resp.Body, request.JobID, endpoint == SeedanceEndpointCreate)
	if err != nil {
		return s.vividAIForwardError(ctx, c, account, err, false)
	}
	publicID := "vividai:" + frame.JobID
	SetActualOpenAIUpstreamEndpoint(c, "/v1/generate")
	result := &OpenAIForwardResult{ResponseID: SeedanceTaskKey(publicID), Model: model, BillingModel: model, UpstreamModel: upstreamModel, UpstreamEndpoint: "/v1/generate", Duration: time.Since(started)}
	if endpoint == SeedanceEndpointCreate {
		c.JSON(http.StatusOK, gin.H{"id": publicID})
		return result, nil
	}
	payload := gin.H{"id": publicID, "status": frame.Status}
	switch frame.Status {
	case "succeeded":
		if frame.CreditsCharged > math.MaxInt64/1000 {
			return s.vividAIForwardError(ctx, c, account, fmt.Errorf("invalid VividAI credits"), false)
		}
		// Explicit credit-equivalent units; never presented as measured model tokens.
		units := frame.CreditsCharged * 1000
		result.Usage.OutputTokens = int(units)
		payload["content"] = gin.H{"video_url": frame.Result}
		payload["usage"] = gin.H{"completion_tokens": units, "total_tokens": units}
		payload["billing"] = gin.H{"unit": "credit_equivalent", "credits": frame.CreditsCharged, "units_per_credit": 1000, "measured_tokens": false}
	case "failed":
		payload["error"] = gin.H{"code": "generation_failed", "message": frame.Error}
		payload["refunded"] = frame.Refunded
	}
	c.JSON(http.StatusOK, payload)
	return result, nil
}
