package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var wuzuPriceFields = map[string]bool{
	"size": true, "width": true, "height": true, "resolution": true, "aspect_ratio": true,
	"output_width": true, "output_height": true, "__output_width": true, "__output_height": true,
	"model_config_key": true, "async": true, "n": true, "mask": true,
}

// Retain only price selectors, never another copy of uploaded image data.
func WithWuzuImageRequest(ctx context.Context, body []byte, contentType string) context.Context {
	request, _ := ctx.Value(siteRequestKey{}).(SitePriceRequest)
	request.WuzuFields = wuzuRequestFields(body, contentType)
	return context.WithValue(ctx, siteRequestKey{}, request)
}

func wuzuRequestFields(body []byte, contentType string) map[string]string {
	out := map[string]string{}
	invalid := map[string]string{"_invalid": "true"}
	media, params, _ := mime.ParseMediaType(contentType)
	if media == "multipart/form-data" {
		if params["boundary"] == "" {
			return invalid
		}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return invalid
			}
			name := part.FormName()
			if wuzuPriceFields[name] {
				if _, duplicate := out[name]; duplicate {
					_ = part.Close()
					return invalid
				}
				if name == "mask" {
					out[name] = "true"
				} else {
					value, err := io.ReadAll(io.LimitReader(part, 4097))
					if err != nil || len(value) > 4096 || part.FileName() != "" {
						_ = part.Close()
						return invalid
					}
					out[name] = string(value)
				}
			}
			_ = part.Close()
		}
		return out
	}
	root := gjson.ParseBytes(body)
	if !gjson.ValidBytes(body) || !root.IsObject() {
		return invalid
	}
	root.ForEach(func(key, value gjson.Result) bool {
		if wuzuPriceFields[key.String()] {
			if _, duplicate := out[key.String()]; duplicate {
				out["_invalid"] = "true"
			}
			if value.Type != gjson.String && value.Type != gjson.Number && value.Type != gjson.False && value.Type != gjson.True {
				out["_invalid"] = "true"
			}
			out[key.String()] = value.String()
		}
		return true
	})
	return out
}

func wuzuLongSideTier(side int) string {
	if side <= 1536 {
		return "1K"
	}
	if side < 3000 {
		return "2K"
	}
	return "4K"
}

func wuzuResolution(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1k", "1024":
		return 1024
	case "2k", "2048":
		return 2048
	case "4k", "4096":
		return 4096
	}
	n, _ := strconv.Atoi(value)
	if n <= 0 || n > 65536 {
		return 0
	}
	return n
}

func wuzuRequestValid(config *WuzuModelConfig, fields map[string]string) bool {
	if config == nil || fields["_invalid"] != "" || fields["mask"] != "" {
		return false
	}
	if key := fields["model_config_key"]; key != "" && key != config.ConfigKey {
		return false
	}
	if async := strings.ToLower(strings.TrimSpace(fields["async"])); async != "" && async != "false" && async != "0" {
		return false
	}
	if n, exists := fields["n"]; exists {
		count, err := strconv.Atoi(n)
		if err != nil || count < 1 || count > 9 {
			return false
		}
	}
	return true
}

// WUZU prices by final dimensions, with thresholds different from Sub2API's.
// Unknown dimensions retain every possible price; clamping can select a lower,
// more expensive tier, so never assume prices are monotonic.
func wuzuRequestTiers(config *WuzuModelConfig, fields map[string]string) ([]string, []string) {
	all := []string{"1K", "2K", "4K"}
	// Partial or malformed explicit dimensions may be completed or clamped by
	// the provider. Do not fall back to a cheaper size in that case.
	for _, names := range [][2]string{{"width", "height"}, {"output_width", "output_height"}, {"__output_width", "__output_height"}} {
		if names[0] != "width" && !config.SupportsUpscale {
			continue
		}
		w, wok := fields[names[0]]
		h, hok := fields[names[1]]
		if !wok && !hok {
			continue
		}
		x, ex := strconv.Atoi(w)
		y, ey := strconv.Atoi(h)
		if !wok || !hok || ex != nil || ey != nil || x <= 0 || y <= 0 || x > 65536 || y > 65536 {
			return all, all
		}
	}
	pair := func(w, h string) (int, int) {
		x, _ := strconv.Atoi(fields[w])
		y, _ := strconv.Atoi(fields[h])
		if x <= 0 || y <= 0 || x > 65536 || y > 65536 {
			return 0, 0
		}
		return x, y
	}
	ratioDimensions := func(ratio string) (int, int) {
		parts := strings.Split(ratio, ":")
		if len(parts) != 2 {
			return 0, 0
		}
		x, e1 := strconv.ParseFloat(parts[0], 64)
		y, e2 := strconv.ParseFloat(parts[1], 64)
		side := wuzuResolution(fields["resolution"])
		if e1 != nil || e2 != nil || x <= 0 || y <= 0 || x > 65536 || y > 65536 || math.IsNaN(x) || math.IsNaN(y) || side == 0 {
			return 0, 0
		}
		round := func(n float64) int { return max(1, int(math.Round(n/16))*16) }
		if x >= y {
			return side, round(float64(side) * y / x)
		}
		return round(float64(side) * x / y), side
	}
	w, h := 0, 0
	if config.SupportsUpscale {
		w, h = pair("__output_width", "__output_height")
		if w == 0 {
			w, h = pair("output_width", "output_height")
		}
	}
	if w == 0 {
		w, h = pair("width", "height")
	}
	if w == 0 && config.SupportsUpscale {
		ratio := fields["aspect_ratio"]
		if ratio == "" {
			ratio = fields["size"]
		}
		w, h = ratioDimensions(ratio)
	}
	if w == 0 {
		size := fields["size"]
		if mapped := config.SizeMap[size]; mapped != "" {
			size = mapped
		}
		w, h, _ = parseImageBillingDimensions(size)
		if w == 0 && (strings.EqualFold(size, "1k") || strings.EqualFold(size, "2k") || strings.EqualFold(size, "4k")) {
			w = wuzuResolution(size)
			h = w
		}
	}
	if w == 0 {
		w, h = ratioDimensions(fields["aspect_ratio"])
	}
	if w <= 0 || h <= 0 || w > 65536 || h > 65536 {
		// Automatic sizes may produce any resolution.
		return all, all
	}
	long := max(w, h)
	tier := wuzuLongSideTier(long)
	billing := NormalizeImageBillingTierOrDefault(strconv.Itoa(w) + "x" + strconv.Itoa(h))
	clamped := (config.MaxOutputLongSide > 0 && long > config.MaxOutputLongSide) || (config.MaxOutputPixels > 0 && int64(w)*int64(h) > int64(config.MaxOutputPixels))
	if !config.SupportsUpscale {
		clamped = clamped || (config.MaxInputLongSide > 0 && long > config.MaxInputLongSide) || (config.MaxInputPixels > 0 && int64(w)*int64(h) > int64(config.MaxInputPixels))
	}
	if clamped {
		return all[:imageTierRank(tier)], all[:imageTierRank(billing)]
	}
	return []string{tier}, []string{billing}
}

func wuzuPriceVeto(p SiteAccountPolicy, request SitePriceRequest) (bool, string) {
	if !wuzuRequestValid(p.Wuzu, request.WuzuFields) {
		return true, "site_price_unknown"
	}
	purchase, billing := []string{request.Tier}, []string{request.Tier}
	if request.WuzuFields != nil {
		purchase, billing = wuzuRequestTiers(p.Wuzu, request.WuzuFields)
		// Billing may fall back to the input size when output metadata is absent.
		fallback := request.Tier
		if fallback == "" || fallback == "unknown" {
			fallback = "2K"
		}
		billing = append(billing, fallback)
	}
	for _, key := range purchase {
		if reason := siteTierReason(p, key, time.Now()); reason != "" {
			return true, reason
		}
		for _, billed := range billing {
			var ceiling *SiteTierLimit
			for i := range p.Limits {
				if p.Limits[i].Key == billed {
					ceiling = &p.Limits[i]
					break
				}
			}
			if ceiling == nil || ceiling.Reason != "" {
				return true, "site_price_unknown"
			}
			copy := p
			copy.Limits = append([]SiteTierLimit(nil), p.Limits...)
			for i := range copy.Limits {
				if copy.Limits[i].Key == key {
					copy.Limits[i].Limits = ceiling.Limits
					copy.Limits[i].Unit = ceiling.Unit
				}
			}
			if reason := siteTierReason(copy, key, time.Now()); reason != "" {
				return true, reason
			}
		}
	}
	return false, ""
}

func prepareWuzuRequest(req *http.Request, config *WuzuModelConfig) error {
	if config == nil || req.Method != http.MethodPost || (req.URL.Path != "/v1/images/generations" && req.URL.Path != "/v1/images/edits") || req.GetBody == nil {
		return errors.New("WUZU 站点当前仅支持图片生成和文件上传图片编辑")
	}
	media, params, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if strings.HasSuffix(req.URL.Path, "/edits") && (!config.SupportsEdit || media != "multipart/form-data") {
		return errors.New("WUZU 图片编辑需要支持编辑的模型及 multipart 文件上传")
	}
	r, err := req.GetBody()
	if err != nil {
		return errors.New("无法读取 WUZU 请求")
	}
	raw, err := io.ReadAll(io.LimitReader(r, 64*1024*1024+1))
	_ = r.Close()
	if err != nil || len(raw) > 64*1024*1024 || !wuzuRequestValid(config, wuzuRequestFields(raw, req.Header.Get("Content-Type"))) {
		return errors.New("WUZU 请求无效：请检查模型配置、图片数量、mask 或 async 参数")
	}
	ctx := WithWuzuImageRequest(req.Context(), raw, req.Header.Get("Content-Type"))
	var updated []byte
	if media == "multipart/form-data" {
		reader := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		for {
			part, e := reader.NextPart()
			if e == io.EOF {
				break
			}
			if e != nil {
				return errors.New("WUZU multipart 请求无效")
			}
			if part.FormName() != "model" && part.FormName() != "model_config_key" {
				target, e := writer.CreatePart(part.Header)
				if e != nil {
					return e
				}
				if _, e = io.Copy(target, part); e != nil {
					return e
				}
			}
			_ = part.Close()
		}
		_ = writer.WriteField("model", config.Model)
		_ = writer.WriteField("model_config_key", config.ConfigKey)
		_ = writer.Close()
		updated = buf.Bytes()
		req.Header.Set("Content-Type", writer.FormDataContentType())
	} else {
		updated, err = sjson.SetBytes(raw, "model", config.Model)
		if err == nil {
			updated, err = sjson.SetBytes(updated, "model_config_key", config.ConfigKey)
		}
		if err != nil {
			return errors.New("无法设置 WUZU 模型配置")
		}
	}
	if req.Body != nil {
		_ = req.Body.Close()
	}
	req.Body = io.NopCloser(bytes.NewReader(updated))
	req.ContentLength = int64(len(updated))
	req.Header.Del("Content-Length")
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(updated)), nil }
	*req = *req.WithContext(ctx)
	return nil
}
