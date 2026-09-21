package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// WithKongfangImageRequest replaces the JSON-only price selectors after the
// Images parser has accepted a multipart request. Keep uploaded bytes out of
// the pricing context and preserve explicit resolution/quality conflicts.
func WithKongfangImageRequest(ctx context.Context, body []byte, parsed *OpenAIImagesRequest) context.Context {
	if parsed == nil {
		return ctx
	}
	request, _ := ctx.Value(siteRequestKey{}).(SitePriceRequest)
	request.Model = parsed.Model
	request.KongfangUnsupportedImageRequest = parsed.HasMask || parsed.MaskUpload != nil || parsed.MaskImageURL != ""
	fields, err := kongfangImagesFields(body, parsed)
	if err != nil {
		request.KongfangTier = "unknown"
	} else {
		request.KongfangTier = kongfangRequestTier(fields)
		request.KongfangBillingTier = kongfangLocalBillingTier(fields)
		if tier := gjson.GetBytes(fields, "service_tier").String(); tier != "" && tier != "default" && tier != "auto" {
			request.UnpricedServiceTier = true
		}
		if gjson.GetBytes(fields, "speed").String() == "fast" {
			request.UnpricedServiceTier = true
		}
	}
	return context.WithValue(ctx, siteRequestKey{}, request)
}

// kongfangImagesFields retains text fields verbatim and converts only typed
// OpenAI form fields. File parts are already represented by parsed.Uploads.
func kongfangImagesFields(body []byte, parsed *OpenAIImagesRequest) ([]byte, error) {
	if !parsed.Multipart {
		return body, nil
	}
	_, params, err := mime.ParseMediaType(parsed.ContentType)
	if err != nil || params["boundary"] == "" {
		return nil, fmt.Errorf("invalid multipart images content type")
	}
	fields := map[string]any{}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(part.FormName())
		if part.FileName() != "" || name == "" {
			_ = part.Close()
			continue
		}
		if _, exists := fields[name]; exists {
			_ = part.Close()
			return nil, fmt.Errorf("duplicate images field %s", name)
		}
		value, err := io.ReadAll(io.LimitReader(part, openAIImageMaxUploadPartSize+1))
		_ = part.Close()
		if err != nil || len(value) > openAIImageMaxUploadPartSize {
			return nil, fmt.Errorf("invalid images field %s", name)
		}
		switch name {
		case "n", "output_compression", "partial_images":
			n, err := strconv.Atoi(strings.TrimSpace(string(value)))
			if err != nil {
				return nil, fmt.Errorf("invalid images field %s", name)
			}
			fields[name] = n
		case "stream":
			stream, err := strconv.ParseBool(strings.TrimSpace(string(value)))
			if err != nil {
				return nil, fmt.Errorf("invalid images field %s", name)
			}
			fields[name] = stream
		default:
			fields[name] = string(value)
		}
	}
	return json.Marshal(fields)
}

// Kongfang accepts reference images through Generations image_urls (including
// data URLs), not multipart Edits. Adapt the wire request without changing the
// client endpoint, original billing size, model alias, or failover request.
func kongfangImagesForwardBody(body []byte, parsed *OpenAIImagesRequest, model string) ([]byte, error) {
	if parsed.HasMask || parsed.MaskUpload != nil || parsed.MaskImageURL != "" {
		return nil, fmt.Errorf("Kongfang reference images do not support masks")
	}
	fields, err := kongfangImagesFields(body, parsed)
	if err != nil {
		return nil, err
	}
	fields, err = sjson.SetBytes(fields, "model", model)
	if err != nil {
		return nil, err
	}
	if !parsed.IsEdits() {
		return fields, nil
	}
	images := append([]string(nil), parsed.InputImageURLs...)
	for _, upload := range parsed.Uploads {
		contentType, _, _ := mime.ParseMediaType(upload.ContentType)
		if contentType == "" || contentType == "application/octet-stream" {
			contentType = http.DetectContentType(upload.Data)
		}
		if len(upload.Data) == 0 || !strings.HasPrefix(contentType, "image/") {
			return nil, fmt.Errorf("invalid Kongfang reference image")
		}
		images = append(images, "data:"+contentType+";base64,"+base64.StdEncoding.EncodeToString(upload.Data))
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("Kongfang reference image is required")
	}
	fields, err = sjson.DeleteBytes(fields, "images")
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(fields, "image_urls", images)
}
