package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

// The plaza is an optional presentation feature, not an API connectivity
// requirement. Use authenticated user endpoints and provision missing group keys.
func (a *siteAdapter) authenticatedPricingCatalog(ctx context.Context) ([]SiteModel, error) {
	available, err := a.request(ctx, http.MethodGet, "/api/v1/groups/available", nil)
	if err != nil {
		return nil, fmt.Errorf("读取可用分组失败：%w", err)
	}
	rates, err := a.request(ctx, http.MethodGet, "/api/v1/groups/rates", nil)
	if err != nil {
		return nil, fmt.Errorf("读取个人费率失败：%w", err)
	}
	groups, personal := available.Get("data"), rates.Get("data")
	if !groups.IsArray() || !personal.IsObject() {
		return nil, errors.New("上游可用分组或个人费率数据不完整")
	}
	channels, err := a.request(ctx, http.MethodGet, "/api/v1/channels/available", nil)
	if err != nil {
		var remote *siteRemoteError
		if !errors.As(err, &remote) || remote.Status != http.StatusNotFound {
			return nil, fmt.Errorf("读取可用渠道价格失败：%w", err)
		}
	} else if !channels.Get("data").IsArray() {
		return nil, errors.New("上游可用渠道数据不完整")
	}
	a.warnings = append(a.warnings, "已自动同步可用模型，缺少的采购价可手动填写。")
	out := []SiteModel{}
	for _, group := range groups.Array() {
		id := group.Get("id").String()
		if id == "" || (group.Get("status").Exists() && group.Get("status").String() != StatusActive) {
			continue
		}
		models := map[string]SiteModel{}
		for _, channel := range channels.Get("data").Array() {
			for _, section := range channel.Get("platforms").Array() {
				allowed := false
				for _, member := range section.Get("groups").Array() {
					allowed = allowed || member.Get("id").String() == id
				}
				if !allowed {
					continue
				}
				for _, model := range section.Get("supported_models").Array() {
					m, err := siteAuthenticatedChannelModel(group, model, personal)
					if err != nil {
						return nil, err
					}
					if m.Platform != group.Get("platform").String() && group.Get("platform").String() != PlatformComposite {
						continue
					}
					if previous, exists := models[m.Model]; exists {
						m = siteMergeChannelPrice(previous, m)
					}
					models[m.Model] = m
				}
			}
		}
		catalog, keyErr := a.groupKeyModelCatalog(ctx, id)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if keyErr != nil {
			if len(models) == 0 {
				a.warnings = append(a.warnings, fmt.Sprintf("分组「%s」暂未取得模型列表：%s", group.Get("name").String(), keyErr))
			}
		} else {
			for _, item := range catalog.Array() {
				name := strings.TrimSpace(item.Get("id").String())
				if name == "" {
					return nil, errors.New("上游模型目录缺少模型名称")
				}
				if _, exists := models[name]; !exists {
					models[name] = siteAuthenticatedUnpricedModel(group, name, personal)
				}
			}
		}
		for _, model := range models {
			out = append(out, model)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GroupID != out[j].GroupID {
			return out[i].GroupID < out[j].GroupID
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}

func siteAuthenticatedChannelModel(group, model, rates gjson.Result) (SiteModel, error) {
	name := strings.TrimSpace(model.Get("name").String())
	if name == "" {
		return SiteModel{}, errors.New("上游渠道模型缺少名称")
	}
	var g, m map[string]any
	if err := json.Unmarshal([]byte(group.Raw), &g); err != nil {
		return SiteModel{}, errors.New("上游分组格式无效")
	}
	if err := json.Unmarshal([]byte(model.Raw), &m); err != nil {
		return SiteModel{}, errors.New("上游渠道模型格式无效")
	}
	m["name"] = name
	if model.Get("platform").String() == "" {
		m["platform"] = group.Get("platform").String()
	}
	mode := model.Get("pricing.billing_mode").String()
	if mode == "image" || mode == "video" {
		// Channel prices are base prices. Group media overrides take precedence;
		// the shared parser then applies personal/independent/peak multipliers.
		p := m["pricing"].(map[string]any)
		intervals := []map[string]any{}
		keys, prefix := []string{"1K", "2K", "4K"}, "image_price_"
		if mode == "video" {
			keys, prefix = []string{"480p", "720p", "1080p"}, "video_price_"
		}
		for _, tier := range keys {
			price := group.Get(prefix + strings.ToLower(tier))
			if mode == "video" {
				family := group.Get("video_model_prices").Map()[CanonicalGrokImagineVideoPriceFamily(name)]
				if value := family.Get(tier); value.Exists() && value.Type != gjson.Null {
					price = value
				}
			}
			if !price.Exists() || price.Type == gjson.Null {
				price = model.Get("pricing.per_request_price")
				for _, interval := range model.Get("pricing.intervals").Array() {
					if strings.EqualFold(interval.Get("tier_label").String(), tier) {
						price = interval.Get("per_request_price")
						break
					}
				}
			}
			intervals = append(intervals, map[string]any{"tier_label": tier, "per_request_price": price.Value()})
		}
		p["intervals"] = intervals
	}
	g["models"] = []any{m}
	raw, err := json.Marshal([]any{g})
	if err != nil {
		return SiteModel{}, err
	}
	parsed, err := parseSub2APISiteCatalog(gjson.ParseBytes(raw), rates)
	if err != nil {
		return SiteModel{}, err
	}
	out := parsed[0]
	if out.Reason == "" && siteAuthenticatedMultiplierRedacted(group, rates, mode) {
		out.Reason = "上游倍率为零，无法确认是免费还是隐藏价格"
	}
	for i := range out.Tiers {
		out.Tiers[i].Note = "登录后可用渠道价格，已计入分组与个人倍率"
	}
	return out, nil
}

func siteAuthenticatedMultiplierRedacted(group, rates gjson.Result, mode string) bool {
	rate := group.Get("rate_multiplier")
	if mode == "image" && group.Get("image_rate_independent").Bool() {
		rate = group.Get("image_rate_multiplier")
	} else if mode == "video" && group.Get("video_rate_independent").Bool() {
		rate = group.Get("video_rate_multiplier")
	} else if personal := rates.Map()[group.Get("id").String()]; personal.Exists() {
		return false // An explicit personal override is not a redacted public rate.
	}
	v, ok := siteNumber(rate)
	return ok && v == 0
}

func siteAuthenticatedUnpricedModel(group gjson.Result, name string, rates gjson.Result) SiteModel {
	m := SiteModel{GroupID: group.Get("id").String(), GroupName: group.Get("name").String(), Model: name, Platform: group.Get("platform").String(), Tiers: []SitePriceTier{}, Reason: "未获取到采购价，请手动填写。"}
	// A model name can establish image capability, but cannot establish whether
	// this upstream bills it by image, token, or a model-specific override.
	// Show useful group reference prices without admitting them as verified cost.
	m.Image = isOpenAIImageGenerationModel(name) || isImageGenerationModel(name)
	if !m.Image || !group.Get("allow_image_generation").Bool() {
		return m
	}
	rate, known := siteNumber(group.Get("rate_multiplier"))
	if personal := rates.Map()[m.GroupID]; personal.Exists() {
		rate, known = siteNumber(personal)
	}
	if group.Get("peak_rate_enabled").Bool() {
		peak, ok := siteNumber(group.Get("peak_rate_multiplier"))
		known = known && ok
		rate *= math.Max(1, peak)
	}
	if group.Get("image_rate_independent").Bool() {
		rate, known = siteNumber(group.Get("image_rate_multiplier"))
	}
	if !known || siteAuthenticatedMultiplierRedacted(group, rates, "image") {
		return m
	}
	m.Tiers = sitePublicImageTiers(group)
	for i := range m.Tiers {
		if value, ok := m.Tiers[i].Prices["request"]; ok {
			value *= rate
			if math.IsInf(value, 0) || math.IsNaN(value) {
				delete(m.Tiers[i].Prices, "request")
			} else {
				m.Tiers[i].Prices["request"] = value
			}
		}
		m.Tiers[i].Note = "上游分组参考价，可在填写采购价时参考"
		m.Tiers[i].Reason = m.Reason
	}
	return m
}

// Multiple visible channels can sell the same model. Never choose the cheapest
// or silently discard an unpriced route that may actually receive the request.
func siteMergeChannelPrice(a, b SiteModel) SiteModel {
	if a.Reason != "" {
		return a
	}
	if b.Reason != "" {
		return b
	}
	if a.Platform != b.Platform || a.Image != b.Image || len(a.Tiers) != len(b.Tiers) {
		a.Reason = "上游多个渠道的计费方式不一致，暂无法确认成本上限"
		return a
	}
	for i := range a.Tiers {
		x, y := &a.Tiers[i], b.Tiers[i]
		if x.Key != y.Key || x.Unit != y.Unit || x.Reason != "" || y.Reason != "" {
			a.Reason = "上游多个渠道的档位价格不完整，暂无法确认成本上限"
			return a
		}
		for component, price := range y.Prices {
			x.Prices[component] = math.Max(x.Prices[component], price)
		}
	}
	return a
}
