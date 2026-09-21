package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// VividAI is a media-only protocol on an OpenAI API key account. It uses the
// existing scheduler; this adapter never selects accounts itself.
const AccountExtraVividAI = "vividai_enabled"

func (a *Account) IsVividAI() bool {
	return a != nil && a.Platform == PlatformOpenAI && a.Type == AccountTypeAPIKey && a.getExtraBool(AccountExtraVividAI)
}

type vividAIRequest struct {
	Model    string   `json:"model,omitempty"`
	Prompt   string   `json:"prompt,omitempty"`
	Quality  string   `json:"quality,omitempty"`
	Ratio    string   `json:"ratio,omitempty"`
	Duration int64    `json:"duration,omitempty"`
	Refs     []string `json:"refs,omitempty"`
	JobID    string   `json:"jobId,omitempty"`
}

type vividAIFrame struct {
	JobID          string `json:"jobId"`
	Status         string `json:"status"`
	Result         string `json:"result"`
	CreditsCharged int64  `json:"creditsCharged"`
	Error          string `json:"error"`
	Refunded       bool   `json:"refunded"`
}

type vividAIError struct {
	Status  int
	Code    string
	Message string
	// Only explicit rejections before a task is accepted may switch accounts.
	Rejected bool
}

func (e *vividAIError) Error() string { return "vividai: " + e.Message }

func vividAIInvalid(message string) error {
	return &vividAIError{Status: http.StatusBadRequest, Code: "invalid_request_error", Message: message}
}

func validateVividAIRequest(request vividAIRequest) error {
	if strings.TrimSpace(request.Model) == "" || strings.TrimSpace(request.Prompt) == "" || utf8.RuneCountInString(request.Prompt) > 8000 {
		return vividAIInvalid("VividAI requires a model and a prompt of 1–8000 characters")
	}
	if strings.TrimSpace(request.Quality) == "" {
		return vividAIInvalid("VividAI requires a quality/resolution supported by the upstream model")
	}
	return nil
}

func (s *OpenAIGatewayService) vividAIPost(ctx context.Context, account *Account, endpoint string, payload any, waitSeconds int) (*http.Response, error) {
	base, err := s.validateUpstreamBaseURL(account.GetCredential("base_url"))
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(account.GetCredential("api_key"))
	if key == "" {
		return nil, vividAIInvalid("VividAI account requires an API key")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, buildOpenAIEndpointURL(base, "/v1/"+endpoint), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	account.ApplyHeaderOverrides(req.Header)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson, application/json")
	if waitSeconds > 0 {
		req.Header.Set("X-Wait-Seconds", fmt.Sprint(waitSeconds))
	}
	managedCreate := false
	if p, ok := payload.(vividAIRequest); ok && p.JobID == "" && endpoint == "generate" && account.IsSiteManaged() {
		if err := CheckSitePriceBeforeSend(ctx, account, s.accountRepo); err != nil {
			return nil, err
		}
		release, err := s.acquireVividAICreate(ctx, account)
		if err != nil {
			return nil, err
		}
		defer release()
		if err := CheckSitePriceBeforeSend(ctx, account, s.accountRepo); err != nil {
			return nil, err
		}
		managedCreate = true
	}
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	if managedCreate {
		markSiteForwardStarted(ctx)
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		return nil, fmt.Errorf("vividai request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var envelope struct {
			JobID string `json:"jobId"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		body := s.readUpstreamErrorBody(resp)
		decodeErr := json.Unmarshal(body, &envelope)
		message := sanitizeUpstreamErrorMessage(envelope.Error.Message)
		if message == "" {
			message = fmt.Sprintf("upstream HTTP %d", resp.StatusCode)
		}
		return nil, &vividAIError{Status: resp.StatusCode, Code: envelope.Error.Code, Message: message, Rejected: decodeErr == nil && envelope.JobID == "" && resp.StatusCode < 500}
	}
	if managedCreate {
		if err := vividAIReadReceipt(resp); err != nil {
			resp.Body.Close()
			return nil, err
		}
	}
	return resp, nil
}

// readVividAIStream preserves the receipt even when a later read fails. Empty
// heartbeat lines and a final running snapshot are not completion.
func readVividAIStream(body io.Reader, jobID string, receiptOnly bool) (vividAIFrame, error) {
	latest := vividAIFrame{JobID: jobID}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var frame vividAIFrame
		if err := json.Unmarshal(line, &frame); err != nil {
			return latest, fmt.Errorf("invalid VividAI stream frame: %w", err)
		}
		if frame.JobID == "" || (latest.JobID != "" && frame.JobID != latest.JobID) {
			return latest, errors.New("VividAI stream jobId missing or changed")
		}
		if err := validateUpstreamPathSegment("VividAI jobId", frame.JobID); err != nil {
			return latest, err
		}
		switch frame.Status {
		case "running":
		case "succeeded":
			resultURL, parseErr := url.Parse(frame.Result)
			if parseErr != nil || resultURL.Scheme != "https" || resultURL.Hostname() == "" || resultURL.User != nil || rejectPrivateImageHost(frame.Result) != nil || frame.CreditsCharged < 0 {
				return latest, errors.New("invalid VividAI result")
			}
		case "failed":
		default:
			return latest, errors.New("unknown VividAI task status")
		}
		latest = frame
		if receiptOnly || frame.Status != "running" {
			return latest, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return latest, err
	}
	if latest.Status == "" {
		return latest, io.ErrUnexpectedEOF
	}
	return latest, nil
}

// Once a receipt exists all subsequent POSTs contain only jobId. An ambiguous
// initial transport failure is deliberately terminal: resubmitting could bill twice.
func (s *OpenAIGatewayService) vividAIWait(ctx context.Context, account *Account, request vividAIRequest) (vividAIFrame, error) {
	jobID := request.JobID
	failures := 0
	for {
		resp, err := s.vividAIPost(ctx, account, "generate", request, 120)
		frame := vividAIFrame{JobID: jobID}
		if err == nil {
			frame, err = readVividAIStream(resp.Body, jobID, false)
			_ = resp.Body.Close()
			if frame.JobID != "" {
				jobID = frame.JobID
			}
			if frame.Status == "succeeded" || frame.Status == "failed" {
				return frame, nil
			}
		}
		var upstreamErr *vividAIError
		if jobID == "" || (errors.As(err, &upstreamErr) && upstreamErr.Status < 500) {
			return frame, err
		}
		if err != nil {
			failures++
			if failures >= 3 {
				return frame, fmt.Errorf("VividAI job %s could not be resumed: %w", jobID, err)
			}
		} else {
			failures = 0
		}
		request = vividAIRequest{JobID: jobID}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return frame, fmt.Errorf("VividAI job %s: %w", jobID, ctx.Err())
		case <-timer.C:
		}
	}
}

func (s *OpenAIGatewayService) vividAIForwardError(ctx context.Context, c *gin.Context, account *Account, err error, allowFailover bool) (*OpenAIForwardResult, error) {
	var priceFailure *UpstreamFailoverError
	if allowFailover && errors.As(err, &priceFailure) {
		return nil, priceFailure
	}
	status, code, message := http.StatusBadGateway, "upstream_error", "VividAI request failed; an accepted task must not be recreated"
	var upstreamErr *vividAIError
	if errors.As(err, &upstreamErr) {
		code, message = upstreamErr.Code, upstreamErr.Message
		if code == "" {
			code = "upstream_error"
		}
		switch code {
		case "4001", "invalid_request_error":
			status = http.StatusBadRequest
		case "4011":
			status = http.StatusUnauthorized
		case "4002":
			status = http.StatusPaymentRequired
		case "4293":
			status = http.StatusTooManyRequests
		case "unsupported_operation":
			status = http.StatusMethodNotAllowed
		}
		// VividAI also uses its business-rejection code for exhausted channel
		// inventory. Report that explicit refusal as availability, not bad input.
		if code == "4001" && upstreamErr.Status == http.StatusBadRequest && upstreamErr.Rejected &&
			strings.Contains(message, "该渠道暂无可用账号") {
			status, code = http.StatusServiceUnavailable, "upstream_capacity_unavailable"
		}
		// These codes explicitly guarantee no task was accepted. Generic 5001 or
		// HTTP errors do not, and may not trigger a second billable create.
		if allowFailover && upstreamErr.Rejected && (code == "4011" || code == "4002" || code == "4293") {
			body, _ := json.Marshal(gin.H{"error": gin.H{"code": code, "message": message, "type": "upstream_error"}})
			resp := &http.Response{StatusCode: status, Header: make(http.Header)}
			s.handleFailoverSideEffects(ctx, resp, account, body)
			return nil, &UpstreamFailoverError{StatusCode: status, ResponseBody: body}
		}
	}
	StopOpenAIImagesJSONKeepaliveCommitted(c)
	MarkResponseCommitted(c)
	c.JSON(status, gin.H{"error": gin.H{"code": code, "type": "upstream_error", "message": message}})
	return nil, err
}

// All reference and signed-object traffic is credential-free, including
// account header overrides. Reuse the gateway's public-host/DNS/redirect policy.
func (s *OpenAIGatewayService) vividAIObjectRequest(ctx context.Context, account *Account, method, rawURL, mime string, data []byte) (*http.Response, error) {
	target, err := s.validateOutboundURL(rawURL)
	if err != nil {
		return nil, err
	}
	if err := rejectPrivateImageHost(target); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(WithHTTPUpstreamPublicHostsOnly(ctx), method, target, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if mime != "" {
		req.Header.Set("Content-Type", mime)
	}
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	return s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
}

const vividAIMaxReferenceBytes = 100 << 20

func (s *OpenAIGatewayService) vividAIReference(ctx context.Context, account *Account, rawURL string) ([]byte, error) {
	if strings.HasPrefix(rawURL, "data:") {
		header, payload, ok := strings.Cut(rawURL, ",")
		if !ok || !strings.HasSuffix(header, ";base64") || len(payload) > base64.StdEncoding.EncodedLen(vividAIMaxReferenceBytes) {
			return nil, vividAIInvalid("invalid or oversized reference data URL")
		}
		data, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil, vividAIInvalid("invalid reference base64")
		}
		return data, nil
	}
	downloadCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := s.vividAIObjectRequest(downloadCtx, account, http.MethodGet, rawURL, "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("reference download HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, vividAIMaxReferenceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > vividAIMaxReferenceBytes {
		return nil, vividAIInvalid("reference exceeds 100 MiB")
	}
	return data, nil
}

func vividAIMime(kind string, data []byte) (string, error) {
	detected := http.DetectContentType(data)
	switch kind {
	case "image":
		if detected == "image/png" || detected == "image/jpeg" || detected == "image/webp" {
			return detected, nil
		}
	case "video":
		if detected == "video/mp4" || detected == "video/webm" {
			return detected, nil
		}
		if len(data) >= 12 && string(data[4:8]) == "ftyp" && string(data[8:12]) == "qt  " {
			return "video/quicktime", nil
		}
	case "audio":
		switch detected {
		case "audio/mpeg":
			return "audio/mpeg", nil
		case "audio/wave", "audio/x-wav":
			return "audio/wav", nil
		case "application/ogg":
			return "audio/ogg", nil
		}
		if len(data) >= 12 && string(data[4:8]) == "ftyp" && strings.HasPrefix(string(data[8:12]), "M4A") {
			return "audio/mp4", nil
		}
	}
	return "", vividAIInvalid("unsupported reference format or kind mismatch: " + kind)
}

func (s *OpenAIGatewayService) vividAIUpload(ctx context.Context, account *Account, kind string, data []byte) (string, error) {
	if len(data) == 0 || len(data) > vividAIMaxReferenceBytes {
		return "", vividAIInvalid("empty or oversized reference")
	}
	contentType, err := vividAIMime(kind, data)
	if err != nil {
		return "", err
	}
	uploadCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := s.vividAIPost(uploadCtx, account, "uploads", map[string]string{"kind": kind, "mime": contentType}, 0)
	if err != nil {
		return "", err
	}
	var session struct {
		Key       string `json:"key"`
		URL       string `json:"url"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&session)
	_ = resp.Body.Close()
	if err != nil || session.Key == "" || session.URL == "" || session.ExpiresAt <= time.Now().Unix() {
		return "", errors.New("invalid or expired VividAI upload session")
	}
	uploaded, err := s.vividAIObjectRequest(uploadCtx, account, http.MethodPut, session.URL, contentType, data)
	if err != nil {
		return "", err
	}
	defer uploaded.Body.Close()
	if uploaded.StatusCode < 200 || uploaded.StatusCode >= 300 {
		return "", fmt.Errorf("VividAI reference upload HTTP %d", uploaded.StatusCode)
	}
	return session.Key, nil
}
