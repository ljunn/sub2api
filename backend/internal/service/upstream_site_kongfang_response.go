package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// Kongfang can return HTTP 200 with progress text followed by an engine error
// as candidate text. Inspect only the pre-output prefix so the caller can switch
// accounts before committing an error as a successful, billable image response.
func prepareKongfangGeminiResponse(resp *http.Response) error {
	const maxPrefix = 1 << 20
	original := resp.Body
	reader := bufio.NewReader(original)
	var prefix, event bytes.Buffer
	defer func() {
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(prefix.Bytes()), reader), original}
	}()
	sse := strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	for prefix.Len() < maxPrefix {
		line, err := reader.ReadSlice('\n')
		prefix.Write(line)
		if sse {
			// A long image payload need not be buffered or parsed before forwarding.
			if errors.Is(err, bufio.ErrBufferFull) {
				return nil
			}
			trimmed := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmed, []byte("data:")) {
				if event.Len() > 0 {
					event.WriteByte('\n')
				}
				event.Write(bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:"))))
			}
			if len(trimmed) == 0 || errors.Is(err, io.EOF) {
				payload := bytes.TrimSpace(event.Bytes())
				if len(payload) > 0 && !bytes.Equal(payload, []byte("[DONE]")) {
					if failure := kongfangGeminiFailure(payload); failure != nil {
						return failure
					}
					if !kongfangGeminiProgress(payload) {
						return nil
					}
				}
				event.Reset()
			}
		} else if errors.Is(err, io.EOF) {
			if failure := kongfangGeminiFailure(prefix.Bytes()); failure != nil {
				return failure
			}
			return nil
		}
		if errors.Is(err, io.EOF) {
			return kongfangGeminiUnavailable("Upstream image stream ended before producing a result")
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return err
		}
	}
	return nil
}

func kongfangGeminiUnavailable(message string) *UpstreamFailoverError {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": 503, "status": "UNAVAILABLE", "message": message}})
	return &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, ResponseBody: body}
}

func kongfangGeminiFailure(payload []byte) *UpstreamFailoverError {
	if !gjson.ValidBytes(payload) {
		return nil
	}
	if signal, ok := detectGeminiResponseSignalInBody(payload); ok {
		if signal.Kind == geminiSignalError && (signal.Status == 429 || signal.Status >= 500) {
			return &UpstreamFailoverError{StatusCode: signal.Status, ResponseBody: append([]byte(nil), payload...)}
		}
		return nil // Content refusals and invalid arguments are terminal.
	}
	parts := gjson.GetBytes(payload, "candidates.0.content.parts")
	if !parts.IsArray() || len(parts.Array()) != 1 {
		return nil
	}
	part := parts.Array()[0]
	if len(part.Map()) != 1 || part.Get("text").Type != gjson.String {
		return nil
	}
	message := strings.TrimSpace(part.Get("text").String())
	if message == "GPT Image 引擎暂不可用，请稍后重试" || message == "GPT Image 引擎暂不可用，请稍后重试。" {
		return kongfangGeminiUnavailable(message)
	}
	return nil
}

func kongfangGeminiProgress(payload []byte) bool {
	if !gjson.ValidBytes(payload) {
		return false
	}
	candidates := gjson.GetBytes(payload, "candidates")
	if !candidates.IsArray() || len(candidates.Array()) == 0 {
		return false
	}
	for _, candidate := range candidates.Array() {
		if candidate.Get("finishReason").String() != "" {
			return false
		}
		parts := candidate.Get("content.parts").Array()
		if len(parts) == 0 {
			return false
		}
		for _, part := range parts {
			if len(part.Map()) != 1 || !strings.HasPrefix(part.Get("text").String(), "[keepalive] ") {
				return false
			}
		}
	}
	return true
}
