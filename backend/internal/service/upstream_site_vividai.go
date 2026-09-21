package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Capabilities belong to the synced model even when a manual price is used.
// VividAI has no upstream billing groups: these two groups describe protocols.
type SiteVividAIModel struct {
	Kind            string   `json:"kind"`
	Qualities       []string `json:"qualities"`
	DurationMode    string   `json:"duration_mode,omitempty"`
	DurationMin     int64    `json:"duration_min,omitempty"`
	DurationMax     int64    `json:"duration_max,omitempty"`
	DurationOptions []int64  `json:"duration_options,omitempty"`
}

func (a *siteAdapter) vividAIBalance(ctx context.Context) (float64, error) {
	if a.credentials.AccessToken == "" {
		return 0, errors.New("请填写 VividAI 现有 API Key")
	}
	result, err := a.request(ctx, http.MethodGet, "/v1/balance", nil)
	if err != nil {
		return 0, err
	}
	amount, valid := siteNumber(result.Get("balance"))
	if result.Get("object").String() != "user.balance" || !valid {
		return 0, errors.New("VividAI 余额响应无效")
	}
	limit, err := siteConcurrencyFromProfile("vividai", result)
	if err != nil {
		return 0, err
	}
	// This protocol explicitly publishes its limit; absence must not become 1000.
	if limit.Source == "default" {
		return 0, errors.New("VividAI 未提供有效并发上限")
	}
	a.site.Concurrency = limit
	return amount, nil
}

func (a *siteAdapter) vividAICatalog(ctx context.Context) ([]SiteModel, error) {
	available, err := a.request(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	catalog, err := a.requestWithHeaders(ctx, http.MethodPost, "/api/cell", map[string]any{
		"serviceName": "modelSvc", "methodName": "publicList", "data": map[string]any{"clientVersion": nil}, "state": map[string]any{},
	}, map[string]string{"Authorization": ""})
	if err != nil {
		return nil, err
	}
	if available.Get("object").String() != "list" || !available.Get("data").IsArray() || catalog.Get("status").Type != gjson.Number || catalog.Get("status").Int() != 0 || !catalog.Get("data.data").IsArray() {
		return nil, errors.New("VividAI 模型或价格目录格式无效")
	}
	return parseVividAICatalog(available.Get("data"), catalog.Get("data.data"), a.site.CreditUSD), nil
}

func vividAIDurationMax(m *SiteVividAIModel) int64 {
	if m == nil {
		return 0
	}
	switch m.DurationMode {
	case "options":
		if len(m.DurationOptions) == 0 {
			return 0
		}
		var maximum int64
		for _, d := range m.DurationOptions {
			if d < 1 || d > 3600 {
				return 0
			}
			if d > maximum {
				maximum = d
			}
		}
		return maximum
	case "range":
		if m.DurationMin > 0 && m.DurationMax >= m.DurationMin && m.DurationMax <= 3600 {
			return m.DurationMax
		}
	}
	return 0
}

func parseVividAICatalog(available, catalogue gjson.Result, usdPerCredit float64) []SiteModel {
	byID := map[string]gjson.Result{}
	for _, entry := range catalogue.Array() {
		id := entry.Get("code").String()
		if _, duplicate := byID[id]; duplicate {
			byID[id] = gjson.Result{}
		} else {
			byID[id] = entry
		}
	}
	models := []SiteModel{}
	seen := map[string]bool{}
	for _, item := range available.Array() {
		id := strings.TrimSpace(item.Get("id").String())
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		entry := byID[id]
		kind := entry.Get("kind").String()
		// A website-only text model must never become an OpenAI chat account.
		if kind == "text" || entry.Get("enabled").Type == gjson.False {
			continue
		}
		m := SiteModel{Model: id, Platform: PlatformOpenAI, GroupID: "image", GroupName: "图片", Image: kind == "image", Tiers: []SitePriceTier{}}
		metadata := &SiteVividAIModel{Kind: kind, DurationMode: entry.Get("durationMode").String(), DurationMin: entry.Get("durationMin").Int(), DurationMax: entry.Get("durationMax").Int()}
		if kind == "video" && metadata.DurationMode == "range" {
			for _, field := range []string{"durationMin", "durationMax"} {
				v := entry.Get(field)
				if v.Type != gjson.Number || v.Float() != float64(v.Int()) {
					metadata.DurationMode = "invalid"
				}
			}
		}
		m.VividAI = metadata
		if kind == "video" {
			m.GroupID, m.GroupName = "video", "视频"
		}
		if !entry.IsObject() || entry.Get("enabled").Type != gjson.True || (kind != "image" && kind != "video") {
			m.Reason = "VividAI 未提供完整模型能力与价格"
			models = append(models, m)
			continue
		}
		for _, q := range entry.Get("qualities").Array() {
			if q.Type == gjson.String {
				metadata.Qualities = append(metadata.Qualities, q.String())
			}
		}
		for _, d := range entry.Get("durationOptions").Array() {
			if d.Type != gjson.Number || d.Float() != float64(d.Int()) {
				metadata.DurationOptions = append(metadata.DurationOptions, 0)
			} else {
				metadata.DurationOptions = append(metadata.DurationOptions, d.Int())
			}
		}
		if usdPerCredit <= 0 || math.IsNaN(usdPerCredit) || math.IsInf(usdPerCredit, 0) {
			m.Reason = "请配置积分美元换算倍率后重新同步"
		}
		qualities := metadata.Qualities
		if kind == "image" {
			qualities = []string{"1K", "2K", "4K"}
		}
		for _, quality := range qualities {
			tier := SitePriceTier{Key: quality, Unit: "USD/image", Prices: map[string]float64{}}
			supported := false
			for _, q := range metadata.Qualities {
				supported = supported || q == quality
			}
			if !supported {
				tier.Reason = "上游不支持此分辨率"
				m.Tiers = append(m.Tiers, tier)
				continue
			}
			amount, known, count := 0.0, false, 0
			for _, p := range entry.Get("prices").Array() {
				if p.Get("quality").String() != quality {
					continue
				}
				count++
				amount, known = siteNumber(p.Get("price"))
				// The public tariff may also publish an agent price. Without an account
				// role endpoint, use the maximum published charge instead of assuming a discount.
				if agent := p.Get("agentPrice"); agent.Exists() && agent.Type != gjson.Null {
					v, ok := siteNumber(agent)
					known = known && ok
					amount = math.Max(amount, v)
				}
			}
			known = known && count == 1
			component := "request"
			if kind == "video" {
				tier.Unit = "USD/second"
				component = "second"
				duration := vividAIDurationMax(metadata)
				tier.MaxDurationSeconds = duration
				known = known && duration > 0
				tier.Note = fmt.Sprintf("%g 积分/秒；每积分 %g USD；最长 %d 秒", amount, usdPerCredit, duration)
				if units := amount * 1000; !math.IsNaN(units) && !math.IsInf(units, 0) {
					tier.CreditUnits = units
				} else {
					known = false
				}
			} else {
				tier.Note = fmt.Sprintf("%g 积分/张；每积分 %g USD", amount, usdPerCredit)
			}
			price := amount * usdPerCredit
			if !known || m.Reason != "" || math.IsNaN(price) || math.IsInf(price, 0) {
				tier.Reason = "缺少有效价格、时长或积分换算"
			} else {
				tier.Prices[component] = price
			}
			m.Tiers = append(m.Tiers, tier)
		}
		if len(m.Tiers) == 0 {
			m.Reason = "VividAI 未提供有效规格"
		}
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].GroupID != models[j].GroupID {
			return models[i].GroupID < models[j].GroupID
		}
		return models[i].Model < models[j].Model
	})
	return models
}

// Upgrade cached per-task quotes on read; no catalogue refresh or DB rewrite is
// required before the price preview and admission use the same per-second unit.
func vividAIPerSecondTiers(metadata *SiteVividAIModel, tiers []SitePriceTier) []SitePriceTier {
	duration := vividAIDurationMax(metadata)
	if metadata == nil || metadata.Kind != "video" || duration <= 0 {
		return tiers
	}
	out := append([]SitePriceTier(nil), tiers...)
	for i, tier := range out {
		if tier.Unit != "USD/request" {
			continue
		}
		out[i].Unit = "USD/second"
		out[i].MaxDurationSeconds = duration
		out[i].CreditUnits = tier.CreditUnits / float64(duration)
		if out[i].CreditUnits > 0 {
			out[i].Note = fmt.Sprintf("%g 积分/秒；最长 %d 秒", out[i].CreditUnits/1000, duration)
		}
		out[i].Prices = map[string]float64{}
		if price, ok := tier.Prices["request"]; ok {
			out[i].Prices["second"] = price / float64(duration)
		}
	}
	return out
}

func vividAISitePriceVeto(p SiteAccountPolicy, request SitePriceRequest) (bool, string) {
	m := p.VividAI
	if m == nil {
		return true, "site_price_unknown"
	}
	tier := request.VividAITier
	if tier == "" {
		tier = request.Tier
	}
	if tier == "" && m.Kind == "image" {
		tier = "1K"
	}
	supported := false
	for _, q := range m.Qualities {
		supported = supported || q == tier
	}
	if !supported {
		return true, "site_price_unknown"
	}
	if m.Kind == "video" {
		d := request.VividAIDuration
		valid := false
		if m.DurationMode == "range" {
			valid = d >= m.DurationMin && d <= m.DurationMax && vividAIDurationMax(m) > 0
		}
		if m.DurationMode == "options" {
			// A single fixed duration is the upstream default when omitted.
			if d == 0 && len(m.DurationOptions) == 1 {
				d = m.DurationOptions[0]
			}
			for _, option := range m.DurationOptions {
				valid = valid || (d == option && d > 0)
			}
		}
		if !valid {
			return true, "site_price_unknown"
		}
	} else if m.Kind != "image" {
		return true, "site_price_unknown"
	}
	// A manual per-request image tariff still has to honor real model capabilities.
	for _, t := range p.Tiers {
		if t.Key == "default" {
			tier = "default"
			break
		}
	}
	reason := siteTierReason(p, tier, time.Now())
	return reason != "", reason
}
