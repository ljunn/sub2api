package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// Some Sub2API forks (including 飞羽) replace model-plaza with a public
// catalogue of final USD prices. Its group multipliers may be redacted to zero;
// applying them again would turn a paid model into an apparently free model.
func (a *siteAdapter) publicPricingCatalog(ctx context.Context) ([]SiteModel, error) {
	result, err := a.request(ctx, http.MethodGet, "/api/v1/public/model-pricing", nil)
	if err != nil {
		var remote *siteRemoteError
		if errors.As(err, &remote) && remote.Status == http.StatusNotFound {
			return a.authenticatedPricingCatalog(ctx)
		}
		return nil, fmt.Errorf("读取公开模型价格失败：%w", err)
	}
	available, err := a.request(ctx, http.MethodGet, "/api/v1/groups/available", nil)
	if err != nil {
		return nil, fmt.Errorf("读取可用分组失败：%w", err)
	}
	rates, err := a.request(ctx, http.MethodGet, "/api/v1/groups/rates", nil)
	if err != nil {
		return nil, fmt.Errorf("读取个人费率失败：%w", err)
	}
	models, err := parsePublicPricingSiteCatalog(result.Get("data"), available.Get("data"), rates.Get("data"))
	if err != nil {
		return nil, err
	}
	private, err := a.privatePricingCatalog(ctx, result.Get("data"), available.Get("data"), rates.Get("data"))
	if err != nil {
		return nil, err
	}
	return append(models, private...), nil
}

func parsePublicPricingSiteCatalog(data, available, rates gjson.Result) ([]SiteModel, error) {
	if !data.Get("groups").IsArray() || !data.Get("models").IsArray() || !available.IsArray() || !rates.IsObject() {
		return nil, errors.New("上游公开模型价格或可用分组、个人费率数据不完整")
	}
	usable := map[string]gjson.Result{}
	for _, group := range available.Array() {
		id := group.Get("id").String()
		if id != "" && (!group.Get("status").Exists() || group.Get("status").String() == StatusActive) {
			usable[id] = group
		}
	}
	groups := map[string]gjson.Result{}
	for _, group := range data.Get("groups").Array() {
		groups[group.Get("id").String()] = group
	}
	out := []SiteModel{}
	seen := map[string]bool{}
	for _, p := range data.Get("models").Array() {
		id, name := p.Get("group_id").String(), strings.TrimSpace(p.Get("name").String())
		group, exists := groups[id]
		access, allowed := usable[id]
		if !allowed {
			continue // Public prices include groups this login cannot bind.
		}
		if !exists || name == "" || seen[id+"\x00"+name] {
			return nil, errors.New("上游公开模型目录存在无效或重复项目")
		}
		seen[id+"\x00"+name] = true
		m := SiteModel{GroupID: id, GroupName: group.Get("name").String(), Model: name, Platform: p.Get("platform").String(), Tiers: []SitePriceTier{}}
		if m.Platform == "" {
			m.Platform = group.Get("platform").String()
		}
		mode := p.Get("billing_mode").String()
		m.Image = siteImagePriceModel(m.Model, mode)
		switch {
		case !p.Get("price_available").Bool():
			m.Reason = "上游尚未公布此模型价格"
		case rates.Map()[id].Exists():
			// Public final prices do not expose the undiscounted base price needed
			// to apply a personal override. Never guess from redacted multipliers.
			m.Reason = "公开价格未包含个人专属费率，暂无法确认实际成本"
		case access.Get("peak_rate_enabled").Bool() || p.Get("time_pricing").IsObject():
			m.Reason = "公开价格未提供完整的高峰或分时计费上限"
		default:
			switch mode {
			case "video":
				m.Tiers = siteVideoPriceTiers(p, 1)
			case "image":
				m.Tiers = sitePublicImageTiers(p)
			case "per_request":
				if price, ok := siteNumber(p.Get("per_request_price")); ok {
					m.Tiers = append(m.Tiers, SitePriceTier{Key: "default", Unit: "USD/request", Prices: map[string]float64{"request": price}})
				}
			case "token":
				prices := siteTokenPrices(p, 1)
				tier := SitePriceTier{Key: "default", Unit: "USD/1M tokens", Prices: prices}
				for _, interval := range p.Get("tiers").Array() {
					// These are final prices too, in USD/token. Take the highest
					// context price across every advertised tier.
					if _, ok := siteNumber(interval.Get("input_price")); !ok {
						m.Reason = "缺少上下文阶梯输入单价"
					}
					if _, ok := siteNumber(interval.Get("output_price")); !ok {
						m.Reason = "缺少上下文阶梯输出单价"
					}
					for key, price := range siteTokenPrices(interval, 1) {
						prices[key] = math.Max(prices[key], price)
					}
					tier.Note = "保守价格上限：采用所有上下文阶梯中的最高单价"
				}
				if _, ok := siteNumber(p.Get("input_price")); !ok {
					m.Reason = "缺少输入单价"
				}
				if _, ok := siteNumber(p.Get("output_price")); !ok {
					m.Reason = "缺少输出单价"
				}
				m.Tiers = append(m.Tiers, tier)
			default:
				m.Reason = "暂不能确认此计费方式的成本"
			}
			if len(m.Tiers) == 0 && m.Reason == "" {
				m.Reason = "上游未提供可核对的价格"
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func sitePublicImageTiers(pricing gjson.Result) []SitePriceTier {
	tiers := make([]SitePriceTier, 0, 3)
	for _, key := range []string{"1K", "2K", "4K"} {
		tier := SitePriceTier{Key: key, Unit: "USD/image", Prices: map[string]float64{}}
		if price, ok := siteNumber(pricing.Get("image_price_" + strings.ToLower(key))); ok {
			tier.Prices["request"] = price
		} else {
			tier.Reason = "上游未提供此分辨率价格"
		}
		tiers = append(tiers, tier)
	}
	return tiers
}
