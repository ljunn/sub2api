package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

// Kongfang's console uses a JWT distinct from its inference API keys. Never
// send the console token to model discovery or expose key_raw in our responses.
func (a *siteAdapter) kongfangKeys(ctx context.Context) ([]gjson.Result, error) {
	result, err := a.request(ctx, http.MethodGet, "/api/v1/user/api-keys", nil)
	if err != nil {
		return nil, err
	}
	if !result.Get("data").IsArray() {
		return nil, errors.New("空凡没有返回有效的密钥列表")
	}
	return result.Get("data").Array(), nil
}

func kongfangKey(item gjson.Result) string {
	key := item.Get("key_raw").String()
	if item.Get("is_active").Type != gjson.True || strings.TrimSpace(key) == "" || strings.Contains(key, "*") {
		return ""
	}
	return key
}

func (a *siteAdapter) ensureKongfangKey(ctx context.Context, name string) (string, error) {
	keys, err := a.kongfangKeys(ctx)
	if err != nil {
		return "", err
	}
	for _, item := range keys {
		if item.Get("name").String() == name {
			if key := kongfangKey(item); key != "" {
				return key, nil
			}
			return "", errors.New("绑定的空凡密钥已停用或不可读取，请在上游检查后重试")
		}
	}
	result, err := a.request(ctx, http.MethodPost, "/api/v1/user/api-keys", map[string]string{"name": name})
	if err != nil {
		return "", err
	}
	return result.Get("data.key").String(), nil
}

func (a *siteAdapter) kongfangCatalog(ctx context.Context) ([]SiteModel, error) {
	profile, err := a.request(ctx, http.MethodGet, "/api/v1/user/profile", nil)
	if err != nil {
		return nil, err
	}
	user := profile.Get("data")
	if user.Get("id").Int() <= 0 || (a.site.UserID > 0 && user.Get("id").Int() != a.site.UserID) || user.Get("status").String() != "active" {
		return nil, errors.New("空凡账号身份不匹配或账号未启用")
	}
	a.site.UserID = user.Get("id").Int()
	discount := 1.0
	if user.Get("is_vip").Type == gjson.True {
		var ok bool
		discount, ok = siteNumber(user.Get("vip_discount"))
		if !ok || discount <= 0 || discount > 1 {
			return nil, errors.New("空凡未提供有效 VIP 折扣，无法核价")
		}
	} else if user.Get("is_vip").Type != gjson.False {
		return nil, errors.New("空凡未提供账号折扣状态，无法核价")
	}
	pricing, err := a.request(ctx, http.MethodGet, "/api/v1/user/pricing", nil)
	if err != nil {
		return nil, err
	}
	available, err := a.request(ctx, http.MethodGet, "/api/v1/user/models", nil)
	if err != nil {
		return nil, err
	}
	if !available.Get("data.image").IsArray() || !pricing.Get("data").IsObject() {
		return nil, errors.New("空凡模型或价格目录格式无效")
	}
	keys, err := a.kongfangKeys(ctx)
	if err != nil {
		return nil, err
	}
	var key string
	for _, item := range keys {
		if key = kongfangKey(item); key != "" {
			break
		}
	}
	if key == "" {
		return nil, errors.New("空凡没有可用 API Key，请先在上游 KEY 管理中创建一个密钥")
	}
	// Discover the actual public protocol aliases, not the console's internal
	// lovart-* names. An empty video/chat list never becomes a usable channel.
	openai, err := a.requestWithHeaders(ctx, http.MethodGet, "/v1/models", nil, map[string]string{"Authorization": "Bearer " + key})
	if err != nil {
		return nil, err
	}
	gemini, err := a.requestWithHeaders(ctx, http.MethodGet, "/v1beta/models", nil, map[string]string{"Authorization": "", "x-goog-api-key": key})
	if err != nil {
		return nil, err
	}
	if !openai.Get("data").IsArray() || !gemini.Get("models").IsArray() {
		return nil, errors.New("空凡协议模型目录格式无效")
	}
	return parseKongfangCatalog(available.Get("data.image"), openai.Get("data"), gemini.Get("models"), pricing.Get("data"), discount, a.site.CreditUSD), nil
}

// Only verified image families have a price rule. New models stay visible but
// unpriced, and therefore cannot pass the existing scheduling price gate.
func kongfangPriceFamily(model string) string {
	switch model {
	case "gpt-image-2", "lovart-gpt-image-2":
		return "image_gpt"
	case "gpt-image-2.5-flare", "lovart-gpt-image-2.5-flare":
		return "image_gpt_flare"
	case "gpt-image-2.5-sunburst", "lovart-gpt-image-2.5-sunburst":
		return "image_gpt_sunburst"
	case "nano-banana-pro", "lovart-nano-banana-pro", "gemini-3-pro-image", "gemini-3.0-pro-image", "gemini-3-pro-image-preview", "gemini-2.0-flash-preview-image-generation":
		return "image_pro"
	case "nano-banana", "nano-banana-v2", "lovart-nano-banana", "lovart-nano-banana-2", "gemini-2.5-flash-image", "gemini-2.5-flash-image-preview", "gemini-3.1-flash-image", "gemini-3.1-flash-image-preview":
		return "image_std"
	default:
		return ""
	}
}
func parseKongfangCatalog(available, openai, gemini, prices gjson.Result, discount, usdPerCredit float64) []SiteModel {
	enabled := map[string]bool{}
	disabled := map[string]bool{}
	families := map[string]bool{}
	for _, model := range available.Array() {
		if model.Get("enabled").Type != gjson.True {
			disabled[model.Get("id").String()] = true
		}
		if model.Get("enabled").Type == gjson.True && model.Get("kind").String() == "image" {
			id := model.Get("id").String()
			enabled[id] = true
			if family := kongfangPriceFamily(id); family != "" {
				families[family] = true
			}
		}
	}
	out := []SiteModel{}
	seen := map[string]bool{}
	add := func(id, platform string) {
		if id == "" || disabled[id] || !IsSafeGeminiModelPathSegment(id) || seen[platform+":"+id] {
			return
		}
		family := kongfangPriceFamily(id)
		if !enabled[id] && (family == "" || !families[family]) {
			return
		}
		seen[platform+":"+id] = true
		groupName := "OpenAI 图片"
		if platform == PlatformGemini {
			groupName = "Gemini 图片"
		}
		m := SiteModel{Image: true, GroupID: platform, GroupName: groupName, Model: id, Platform: platform, Tiers: []SitePriceTier{}}
		if family == "" {
			m.Reason = "空凡模型尚无已核验的价格规则"
		}
		if usdPerCredit <= 0 || math.IsNaN(usdPerCredit) || math.IsInf(usdPerCredit, 0) {
			m.Reason = "请配置每积分美元成本后重新同步"
		}
		for _, tier := range []string{"1K", "2K", "4K"} {
			t := SitePriceTier{Key: tier, Unit: "USD/image", Prices: map[string]float64{}}
			amount, ok := siteNumber(prices.Get(family + ".quality_" + strings.ToLower(tier)))
			if family == "" || !ok {
				t.Reason = "空凡未提供此档位的有效价格"
			} else {
				t.Note = fmt.Sprintf("标价 %g 积分 × 账号折扣 %g = %g 积分；每积分 %g USD", amount, discount, amount*discount, usdPerCredit)
				value := amount * discount * usdPerCredit
				if m.Reason == "" && !math.IsNaN(value) && !math.IsInf(value, 0) {
					t.Prices["request"] = value
				} else {
					t.Reason = "积分美元成本或模型价格规则不可用"
				}
			}
			m.Tiers = append(m.Tiers, t)
		}
		out = append(out, m)
	}
	for _, model := range openai.Array() {
		if model.Get("type").String() == "image" {
			add(model.Get("id").String(), PlatformOpenAI)
		}
	}
	for _, model := range gemini.Array() {
		add(strings.TrimPrefix(model.Get("name").String(), "models/"), PlatformGemini)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GroupID != out[j].GroupID {
			return out[i].GroupID < out[j].GroupID
		}
		return out[i].Model < out[j].Model
	})
	return out
}
