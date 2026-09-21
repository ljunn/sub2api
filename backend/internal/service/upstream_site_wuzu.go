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
	"time"

	"github.com/tidwall/gjson"
)

// WUZU (ChatGPT2API) has separate console and inference keys. Public model IDs
// can be duplicated; bindings use the unique config_key throughout provisioning.
type WuzuModelConfig struct {
	ConfigKey         string            `json:"config_key"`
	Model             string            `json:"model"`
	SupportsEdit      bool              `json:"supports_edit"`
	SupportsUpscale   bool              `json:"supports_upscale"`
	SizeMap           map[string]string `json:"size_map,omitempty"`
	MaxInputLongSide  int               `json:"max_input_long_side,omitempty"`
	MaxInputPixels    int               `json:"max_input_pixels,omitempty"`
	MaxOutputLongSide int               `json:"max_output_long_side,omitempty"`
	MaxOutputPixels   int               `json:"max_output_pixels,omitempty"`
}

func (a *siteAdapter) authenticateWuzu(ctx context.Context) error {
	load := func() error {
		result, err := a.request(ctx, http.MethodGet, "/api/auth/me", nil)
		if err != nil {
			return err
		}
		profile := result.Get("identity")
		id := strings.TrimSpace(profile.Get("id").String())
		if !profile.IsObject() || profile.Get("id").Type != gjson.String || id == "" || profile.Get("enabled").Type != gjson.True {
			return errors.New("WUZU 账号资料无效或账号已停用")
		}
		if a.credentials.SubjectID != "" && a.credentials.SubjectID != id && (len(a.site.Bindings) > 0 || len(a.credentials.Keys) > 0) {
			return errors.New("WUZU 登录账号与已有绑定不一致，请新建站点")
		}
		limit := profile.Get("effective_image_concurrency")
		n, ok := siteNumber(limit)
		if !ok || n <= 0 || n > math.MaxInt32 || math.Trunc(n) != n {
			return errors.New("WUZU 未提供有效的账号总并发限制")
		}
		a.credentials.SubjectID = id
		a.site.Concurrency = &SiteConcurrency{Limit: int(n), Source: "effective_image_concurrency", CheckedAt: time.Now().UTC()}
		a.wuzuProfile = profile
		return nil
	}
	if a.credentials.AccessToken != "" {
		err := load()
		if err == nil {
			return nil
		}
		var remote *siteRemoteError
		if !errors.As(err, &remote) || (remote.Status != http.StatusUnauthorized && remote.Status != http.StatusForbidden) {
			return err
		}
	}
	if a.site.AuthMode != "password" || a.credentials.Password == "" {
		return errors.New("WUZU 登录已失效，请更新后台登录令牌（不是模型调用 API Key）")
	}
	a.credentials.AccessToken = ""
	a.credentials.Cookies = nil
	result, err := a.request(ctx, http.MethodPost, "/auth/password-login", map[string]string{"username": a.site.Username, "password": a.credentials.Password})
	if err != nil {
		return err
	}
	key := result.Get("key")
	if result.Get("ok").Type != gjson.True || key.Type != gjson.String || strings.TrimSpace(key.String()) == "" {
		return errors.New("WUZU 登录未返回有效令牌，请检查账号密码或额外验证")
	}
	a.credentials.AccessToken = key.String()
	return load()
}

func (a *siteAdapter) wuzuKeyCapability(ctx context.Context) error {
	result, err := a.request(ctx, http.MethodGet, "/api/me/site-api-keys/capability", nil)
	if err != nil {
		return err
	}
	if result.Get("allowed").Type != gjson.True {
		return errors.New("WUZU 账号未开放自助 API Key，请先在上游开通权限")
	}
	return nil
}

func (a *siteAdapter) wuzuCatalog(ctx context.Context) ([]SiteModel, error) {
	if err := a.wuzuKeyCapability(ctx); err != nil {
		return nil, err
	}
	result, err := a.request(ctx, http.MethodGet, "/api/image-models", nil)
	if err != nil {
		return nil, err
	}
	if !result.Get("items").IsArray() {
		return nil, errors.New("WUZU 未返回有效的模型目录")
	}
	return parseWuzuCatalog(result.Get("items"), a.site.BalanceUnitsPerUSD)
}

func parseWuzuCatalog(items gjson.Result, unitsPerUSD float64) ([]SiteModel, error) {
	out := []SiteModel{}
	seen := map[string]bool{}
	for _, item := range items.Array() {
		if item.Get("enabled").Type != gjson.True || item.Get("disable_api_key").Type != gjson.False || item.Get("mode").String() != "image" {
			continue
		}
		key := strings.TrimSpace(item.Get("config_key").String())
		model := strings.TrimSpace(item.Get("id").String())
		if key == "" || model == "" || len(key) > 200 || strings.ContainsAny(key, "*?\r\n\t") || seen[key] {
			return nil, errors.New("WUZU 模型配置 Key 缺失、重复或格式无效")
		}
		seen[key] = true
		config := &WuzuModelConfig{ConfigKey: key, Model: model}
		if err := json.Unmarshal([]byte(item.Raw), config); err != nil {
			return nil, errors.New("WUZU 模型尺寸配置无效")
		}
		config.Model = model
		m := SiteModel{Wuzu: config, Image: true, GroupID: PlatformOpenAI, GroupName: "OpenAI 图片", Model: key, Platform: PlatformOpenAI, Tiers: []SitePriceTier{}}
		mode := item.Get("quota_cost_mode").String()
		if mode != "tier" && mode != "fixed" {
			m.Reason = "WUZU 此计价规则暂不支持自动核价，请手动填写采购价"
		}
		if allowed := item.Get("allowed_user_types"); allowed.Exists() && (!allowed.IsArray() || len(allowed.Array()) > 0) {
			// The console does not expose a stable user-type discriminator. Never
			// infer permission from the public catalogue or a membership name.
			continue
		}
		if unitsPerUSD <= 0 || math.IsNaN(unitsPerUSD) || math.IsInf(unitsPerUSD, 0) {
			m.Reason = "请配置 WUZU 额度换算倍率（1 USD 等于多少额度）后重新同步"
		}
		for _, tier := range []string{"1K", "2K", "4K"} {
			t := SitePriceTier{Key: tier, Unit: "USD/image", Prices: map[string]float64{}}
			value := item.Get("quota_cost")
			if mode == "tier" {
				value = item.Get("quota_cost_tiers." + strings.ToLower(tier))
			}
			amount, valid := siteNumber(value)
			if !valid {
				t.Reason = "WUZU 未提供此档位的有效额度价格"
			} else {
				t.Note = fmt.Sprintf("%g 额度/张；1 USD = %g 额度", amount, unitsPerUSD)
				if m.Reason == "" {
					cost := amount / unitsPerUSD
					if math.IsNaN(cost) || math.IsInf(cost, 0) {
						t.Reason = "WUZU 价格换算结果无效"
					} else {
						t.Prices["request"] = cost
					}
				}
			}
			m.Tiers = append(m.Tiers, t)
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out, nil
}

func (a *siteAdapter) ensureWuzuKey(ctx context.Context, name string, binding *SiteBinding) (string, error) {
	if err := a.wuzuKeyCapability(ctx); err != nil {
		return "", err
	}
	model := findSiteModel(a.site, binding.GroupID, binding.Model)
	if model == nil || model.Wuzu == nil || model.Wuzu.ConfigKey != binding.Model {
		return "", errors.New("WUZU 模型配置已不可用，请重新同步")
	}
	result, err := a.request(ctx, http.MethodGet, "/api/me/site-api-keys", nil)
	if err != nil {
		return "", err
	}
	if !result.Get("items").IsArray() {
		return "", errors.New("WUZU 未返回有效的 API Key 列表")
	}
	for _, item := range result.Get("items").Array() {
		if item.Get("name").String() == name {
			// WUZU reveals the secret only once. In particular, never retry a
			// possibly successful creation by creating more keys with this name.
			return "", fmt.Errorf("WUZU 已存在绑定密钥 %s，但完整密钥不可再次读取；请在上游删除该专用密钥后重试绑定", name)
		}
	}
	result, err = a.request(ctx, http.MethodPost, "/api/me/site-api-keys", map[string]any{
		"name": name, "allowed_model_config_keys": []string{model.Wuzu.ConfigKey},
		"allowed_model_ids": []string{}, "allowed_providers": []string{}, "image_concurrency": 0,
		"default_image_response_format": "b64_json", "image_response_format_priority": "request",
	})
	if err != nil {
		return "", err
	}
	key := result.Get("key")
	if key.Type != gjson.String || strings.TrimSpace(key.String()) == "" || strings.ContainsAny(key.String(), "*\r\n\t ") {
		return "", errors.New("WUZU 创建响应未返回完整 API Key；请检查上游专用密钥后重试")
	}
	return key.String(), nil
}
