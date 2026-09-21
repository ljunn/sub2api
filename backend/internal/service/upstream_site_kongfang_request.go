package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func kongfangSizeTier(size string) string {
	size = strings.ToLower(strings.TrimSpace(size))
	switch size {
	case "1k":
		return "1K"
	case "2k":
		return "2K"
	case "4k":
		return "4K"
	case "", "auto":
		return "2K"
	}
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return "unknown"
	}
	w, e1 := strconv.Atoi(parts[0])
	h, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || w <= 0 || h <= 0 {
		return "unknown"
	}
	if h > w {
		w = h
	}
	if w <= 1600 {
		return "1K"
	}
	if w <= 2800 {
		return "2K"
	}
	return "4K"
}

// Explicit resolution is preferred; conflicting selectors cannot establish a
// trustworthy purchase price. Canonical 1k/2k/4k values are sent upstream.
func kongfangRequestTier(body []byte) string {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return "unknown"
	}
	tier := ""
	for _, path := range []string{"resolution", "image_size", "generationConfig.resolution", "generation_config.resolution", "generationConfig.imageConfig.imageSize", "generation_config.image_config.image_size"} {
		v := root.Get(path)
		if !v.Exists() {
			continue
		}
		value := strings.ToUpper(strings.TrimSpace(v.String()))
		if v.Type != gjson.String || (value != "1K" && value != "2K" && value != "4K") {
			return "unknown"
		}
		if tier != "" && tier != value {
			return "unknown"
		}
		tier = value
	}
	if q := root.Get("quality"); q.Exists() {
		value := ""
		switch strings.ToLower(strings.TrimSpace(q.String())) {
		case "1k", "standard", "low", "medium", "auto":
			value = "1K"
		case "2k", "hd":
			value = "2K"
		case "4k", "ultra":
			value = "4K"
		default:
			return "unknown" // Kongfang's published examples disagree about high.
		}
		if q.Type != gjson.String || (tier != "" && tier != value) {
			return "unknown"
		}
		tier = value
	}
	if tier != "" {
		return tier
	}
	return kongfangSizeTier(root.Get("size").String())
}

func kongfangLocalBillingTier(body []byte) string {
	root := gjson.ParseBytes(body)
	// Native Gemini billing reads imageConfig. Kongfang's flat resolution is
	// translated into that field for the local billing observer below.
	if root.Get("contents").Exists() {
		return kongfangRequestTier(body)
	}
	return NormalizeImageBillingTierOrDefault(root.Get("size").String())
}

func kongfangPriceVeto(p SiteAccountPolicy, request SitePriceRequest) (bool, string) {
	if request.KongfangUnsupportedImageRequest {
		return true, "site_endpoint_unsupported"
	}
	tier := request.KongfangTier
	if tier != "1K" && tier != "2K" && tier != "4K" {
		return true, "site_price_unknown"
	}
	if reason := siteTierReason(p, tier, time.Now()); reason != "" {
		return true, reason
	}
	// The local pixel-based billing tier may differ from the purchased tier.
	// Compare the selected purchase price against both ceilings. This preserves
	// each upstream resolution's switch without underestimating the cost.
	billingTier := request.KongfangBillingTier
	if billingTier == "" {
		billingTier = request.Tier
	}
	if billingTier == "" || billingTier == "unknown" {
		return true, "site_price_unknown"
	}
	if billingTier != tier {
		var billing *SiteTierLimit
		for i := range p.Limits {
			if p.Limits[i].Key == billingTier {
				billing = &p.Limits[i]
				break
			}
		}
		if billing == nil || billing.Reason != "" {
			return true, "site_price_unknown"
		}
		for i := range p.Limits {
			if p.Limits[i].Key == tier {
				// Clone before changing the request-local comparison.
				limits := append([]SiteTierLimit(nil), p.Limits...)
				limits[i].Limits = billing.Limits
				limits[i].Unit = billing.Unit
				p.Limits = limits
				break
			}
		}
		if reason := siteTierReason(p, tier, time.Now()); reason != "" {
			return true, reason
		}
	}
	return false, ""
}

// Runs at the transport boundary on every retry. Only verified image endpoints
// are allowed; a site binding must never imply support for video, Responses,
// text chat or multipart Images Edits.
func prepareKongfangRequest(req *http.Request) error {
	path := req.URL.Path
	native := strings.HasPrefix(path, "/v1beta/models/") && (strings.HasSuffix(path, ":generateContent") || strings.HasSuffix(path, ":streamGenerateContent"))
	if req.Method != http.MethodPost || (path != "/v1/images/generations" && !native) {
		return errors.New("空凡站点当前仅支持 Images Generations 和 Gemini 图片生成接口")
	}
	if req.GetBody == nil {
		return errors.New("空凡图片请求缺少可核验的 JSON")
	}
	r, err := req.GetBody()
	if err != nil {
		return errors.New("无法读取空凡图片请求")
	}
	raw, err := io.ReadAll(io.LimitReader(r, 32*1024*1024+1))
	_ = r.Close()
	if err != nil || len(raw) > 32*1024*1024 || !gjson.ValidBytes(raw) {
		return errors.New("空凡图片请求格式无效或过大")
	}
	tier := kongfangRequestTier(raw)
	if tier == "unknown" {
		return errors.New("空凡图片分辨率参数不明确，请使用一致的 1k、2k 或 4k")
	}
	updated := raw
	if native {
		updated, err = sjson.SetBytes(updated, "generationConfig.resolution", strings.ToLower(tier))
		if ratio := gjson.GetBytes(raw, "generationConfig.imageConfig.aspectRatio"); ratio.Exists() {
			if flat := gjson.GetBytes(raw, "generationConfig.aspectRatio"); flat.Exists() && flat.String() != ratio.String() {
				return errors.New("空凡图片比例参数冲突")
			}
			updated, err = sjson.SetBytes(updated, "generationConfig.aspectRatio", ratio.String())
		}
	} else {
		updated, err = sjson.SetBytes(updated, "quality", strings.ToLower(tier))
	}
	if err != nil {
		return errors.New("无法规范化空凡图片请求")
	}
	// Preserve the original local billing selector before normalization.
	ctx := WithSitePriceRequest(req.Context(), raw)
	if prior, ok := req.Context().Value(siteRequestKey{}).(SitePriceRequest); ok {
		next, _ := ctx.Value(siteRequestKey{}).(SitePriceRequest)
		next.Model = prior.Model // The local selling price belongs to the client alias.
		if prior.KongfangBillingTier != "" {
			next.KongfangBillingTier = prior.KongfangBillingTier
		}
		ctx = context.WithValue(ctx, siteRequestKey{}, next)
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
