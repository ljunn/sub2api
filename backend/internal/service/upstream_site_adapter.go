package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type siteRemoteError struct {
	Status int
	Path   string
}

func (e *siteRemoteError) Error() string {
	return fmt.Sprintf("上游接口 %s 返回 HTTP %d，请检查登录凭据、权限或站点验证要求", e.Path, e.Status)
}

type siteAdapter struct {
	site              *UpstreamSite
	credentials       *SiteCredentials
	client            *http.Client
	balanceNativeRate float64
	warnings          []string
}

func newSiteAdapter(site *UpstreamSite, credentials *SiteCredentials) *siteAdapter {
	return &siteAdapter{site: site, credentials: credentials, client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (a *siteAdapter) request(ctx context.Context, method, path string, input any) (gjson.Result, error) {
	return a.requestWithHeaders(ctx, method, path, input, nil)
}
func (a *siteAdapter) requestWithHeaders(ctx context.Context, method, path string, input any, headers map[string]string) (gjson.Result, error) {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return gjson.Result{}, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.site.BaseURL+path, body)
	if err != nil {
		return gjson.Result{}, errors.New("上游地址无效")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Sub2API-SiteManager/1")
	origin, _ := url.Parse(a.site.BaseURL)
	req.Header.Set("Origin", origin.Scheme+"://"+origin.Host)
	if a.credentials.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.credentials.AccessToken)
	}
	if a.site.Kind == "newapi" && a.site.UserID > 0 {
		req.Header.Set("New-Api-User", strconv.FormatInt(a.site.UserID, 10))
	}
	for name, value := range a.credentials.Cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	if a.site.Kind == "newapi" && a.credentials.RefreshToken != "" {
		req.AddCookie(&http.Cookie{Name: "new_api_refresh", Value: a.credentials.RefreshToken})
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return gjson.Result{}, errors.New("上游连接失败或超时")
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "new_api_refresh" {
			a.credentials.RefreshToken = c.Value
			continue
		}
		if c.Name == "session" {
			if a.credentials.Cookies == nil {
				a.credentials.Cookies = map[string]string{}
			}
			a.credentials.Cookies[c.Name] = c.Value
		}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil || len(raw) > 8*1024*1024 {
		return gjson.Result{}, errors.New("上游响应过大或读取失败")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return gjson.Result{}, &siteRemoteError{Status: resp.StatusCode, Path: path}
	}
	if !gjson.ValidBytes(raw) {
		return gjson.Result{}, errors.New("上游没有返回 JSON，请检查站点地址或人机验证")
	}
	result := gjson.ParseBytes(raw)
	if success := result.Get("success"); success.Exists() && !success.Bool() {
		return gjson.Result{}, errors.New("上游拒绝操作，请检查凭据、权限、额度或额外验证要求")
	}
	if code := result.Get("code"); code.Type == gjson.Number && code.Int() != 0 && code.Int() != 200 {
		return gjson.Result{}, errors.New("上游接口报告失败")
	}
	return result, nil
}
func (a *siteAdapter) acceptAuth(result gjson.Result) error {
	data := result.Get("data")
	if token := data.Get("access_token").String(); token != "" {
		a.credentials.AccessToken = token
	}
	if token := data.Get("refresh_token").String(); token != "" {
		a.credentials.RefreshToken = token
	}
	if a.site.Kind == "kongfang" {
		a.credentials.AccessToken = data.Get("token").String()
	}
	id := data.Get("user.id").Int()
	if id == 0 {
		id = data.Get("id").Int()
	}
	if id > 0 {
		if a.site.UserID > 0 && a.site.UserID != id && len(a.site.Bindings) > 0 {
			return errors.New("登录账号与已有绑定不一致，请新建站点")
		}
		a.site.UserID = id
	}
	if a.credentials.AccessToken == "" && len(a.credentials.Cookies) == 0 {
		return errors.New("登录需要额外验证，请在上游完成验证后使用 Access Token / Refresh Token")
	}
	return nil
}
func (a *siteAdapter) authenticate(ctx context.Context) error {
	selfPath := "/api/v1/auth/me"
	if a.site.Kind == "kongfang" {
		selfPath = "/api/v1/user/profile"
	}
	if a.site.Kind == "newapi" {
		selfPath = "/api/user/self"
	}
	if a.credentials.AccessToken != "" || len(a.credentials.Cookies) > 0 {
		result, err := a.request(ctx, http.MethodGet, selfPath, nil)
		if err == nil {
			return a.acceptProfile(result.Get("data"))
		}
		var remote *siteRemoteError
		if !errors.As(err, &remote) || (remote.Status != 401 && remote.Status != 403) {
			return err
		}
	}
	if a.credentials.RefreshToken != "" && a.site.Kind != "kongfang" {
		path := "/api/v1/auth/refresh"
		var payload any = map[string]string{"refresh_token": a.credentials.RefreshToken}
		if a.site.Kind == "newapi" {
			path = "/api/user/auth/refresh"
			payload = map[string]string{}
		}
		result, err := a.request(ctx, http.MethodPost, path, payload)
		if err == nil {
			if err := a.acceptAuth(result); err != nil {
				return err
			}
			return a.loadProfile(ctx, selfPath)
		}
		var remote *siteRemoteError
		if !errors.As(err, &remote) || (remote.Status != 401 && remote.Status != 403 && remote.Status != 404) {
			return err
		}
	}
	if a.site.AuthMode != "password" || a.credentials.Password == "" {
		return errors.New("登录已失效，请更新 Access Token 或 Refresh Token")
	}
	payload := map[string]string{"email": a.site.Username, "password": a.credentials.Password}
	path := "/api/v1/auth/login"
	if a.site.Kind == "kongfang" {
		path = "/api/v1/admin/auth/login"
		payload = map[string]string{"username": a.site.Username, "password": a.credentials.Password}
	}
	if a.site.Kind == "newapi" {
		path = "/api/user/login"
		payload = map[string]string{"username": a.site.Username, "password": a.credentials.Password}
		key, err := a.request(ctx, http.MethodGet, "/api/user/login/encryption-key", nil)
		if err != nil {
			var remote *siteRemoteError
			if !errors.As(err, &remote) || remote.Status != 404 {
				return err
			}
		}
		if key.Get("data.enabled").Bool() {
			block, _ := pem.Decode([]byte(key.Get("data.public_key").String()))
			if block == nil {
				return errors.New("上游密码加密公钥无效")
			}
			parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err != nil {
				return errors.New("上游密码加密公钥无效")
			}
			pub, ok := parsed.(*rsa.PublicKey)
			if !ok || pub.N.BitLen() < 2048 {
				return errors.New("上游密码加密公钥无效")
			}
			ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, []byte(a.credentials.Password), nil)
			if err != nil {
				return errors.New("密码加密失败")
			}
			delete(payload, "password")
			payload["password_encrypted"] = base64.StdEncoding.EncodeToString(ciphertext)
			payload["encryption_key_id"] = key.Get("data.kid").String()
		}
	}
	// Discard expired credentials so a challenge cannot masquerade as a login.
	a.credentials.AccessToken = ""
	a.credentials.Cookies = nil
	result, err := a.request(ctx, http.MethodPost, path, payload)
	if err != nil {
		return err
	}
	if err := a.acceptAuth(result); err != nil {
		return err
	}
	return a.loadProfile(ctx, selfPath)
}
func (a *siteAdapter) catalog(ctx context.Context) ([]SiteModel, error) {
	if err := a.authenticate(ctx); err != nil {
		return nil, err
	}
	if a.site.Kind == "kongfang" {
		return a.kongfangCatalog(ctx)
	}
	if a.site.Kind == "newapi" {
		result, err := a.request(ctx, http.MethodGet, "/api/pricing", nil)
		if err != nil {
			return nil, err
		}
		return parseNewAPISiteCatalog(result)
	}
	result, err := a.request(ctx, http.MethodGet, "/api/v1/model-plaza", nil)
	if err != nil {
		var remote *siteRemoteError
		if errors.As(err, &remote) && remote.Status == http.StatusNotFound {
			return a.publicPricingCatalog(ctx)
		}
		return nil, fmt.Errorf("读取模型价格失败：%w", err)
	}
	// Fetch personal rates separately: model-plaza deliberately falls back to
	// public rates when its personal-rate lookup fails, which is unsafe here.
	rates, err := a.request(ctx, http.MethodGet, "/api/v1/groups/rates", nil)
	if err != nil {
		return nil, err
	}
	return parseSub2APISiteCatalog(result.Get("data.groups"), rates.Get("data"))
}
func siteNumber(v gjson.Result) (float64, bool) {
	n := v.Float()
	return n, v.Type == gjson.Number && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0
}
func siteTokenPrices(p gjson.Result, rate float64) map[string]float64 {
	out := map[string]float64{}
	for _, field := range []string{"input_price", "output_price", "cache_read_price", "cache_write_price", "cache_write_1h_price", "image_input_price", "image_output_price"} {
		if v, ok := siteNumber(p.Get(field)); ok {
			out[field] = v * 1e6 * rate
		}
	}
	return out
}

func siteIntervalTokenPrices(base, interval gjson.Result, rate float64) (map[string]float64, bool) {
	prices := siteTokenPrices(base, rate)
	for _, field := range []string{"input_price", "output_price", "cache_read_price", "cache_write_price", "cache_write_1h_price", "image_input_price", "image_output_price"} {
		absolute := interval.Get(field)
		if field == "cache_write_1h_price" && (!absolute.Exists() || absolute.Type == gjson.Null) {
			absolute = interval.Get("cache_write_price")
		}
		if absolute.Exists() && absolute.Type != gjson.Null {
			value, ok := siteNumber(absolute)
			if !ok {
				return nil, false
			}
			prices[field] = value * 1e6 * rate
			continue
		}
		multiplierKey := strings.TrimSuffix(field, "_price") + "_multiplier"
		if field == "cache_write_1h_price" {
			multiplierKey = "cache_write_multiplier"
		}
		if multiplier := interval.Get(multiplierKey); multiplier.Exists() && multiplier.Type != gjson.Null {
			value, ok := siteNumber(multiplier)
			if !ok {
				return nil, false
			}
			if price, exists := prices[field]; exists {
				prices[field] = price * value
			}
		}
	}
	return prices, true
}
func parseSub2APISiteCatalog(groups, rates gjson.Result) ([]SiteModel, error) {
	if !groups.IsArray() || !rates.IsObject() {
		return nil, errors.New("上游分组或个人费率数据不完整")
	}
	out := []SiteModel{}
	for _, group := range groups.Array() {
		groupID := group.Get("id").String()
		rate, known := siteNumber(group.Get("rate_multiplier"))
		if personal := rates.Map()[groupID]; personal.Exists() {
			rate, known = siteNumber(personal)
		}
		// Use the highest advertised peak factor for the entire refresh interval,
		// so crossing a time boundary cannot underprice a queued request.
		if group.Get("peak_rate_enabled").Bool() {
			peak, ok := siteNumber(group.Get("peak_rate_multiplier"))
			known = known && ok
			rate *= math.Max(1, peak)
		}
		for _, model := range group.Get("models").Array() {
			m := SiteModel{GroupID: groupID, GroupName: group.Get("name").String(), Model: model.Get("name").String(), Platform: model.Get("platform").String(), Tiers: []SitePriceTier{}}
			p := model.Get("pricing")
			mode := p.Get("billing_mode").String()
			m.Image = mode == "image"
			multiplier := rate
			modelKnown := known
			if mode == "image" && group.Get("image_rate_independent").Bool() {
				var ok bool
				multiplier, ok = siteNumber(group.Get("image_rate_multiplier"))
				modelKnown = ok
			}
			timeFactor := 1.0
			if tp := model.Get("time_pricing"); tp.IsObject() {
				for _, period := range tp.Get("periods").Array() {
					v, ok := siteNumber(period.Get("multiplier"))
					modelKnown = modelKnown && ok
					timeFactor = math.Max(timeFactor, v)
				}
			}
			multiplier *= timeFactor
			if !modelKnown || !p.IsObject() {
				m.Reason = "上游未提供完整的实际价格"
				out = append(out, m)
				continue
			}
			switch mode {
			case "image":
				for _, tier := range []string{"1K", "2K", "4K"} {
					price, ok := siteNumber(p.Get("per_request_price"))
					for _, iv := range p.Get("intervals").Array() {
						if strings.EqualFold(iv.Get("tier_label").String(), tier) {
							price, ok = siteNumber(iv.Get("per_request_price"))
							break
						}
					}
					t := SitePriceTier{Key: tier, Unit: "USD/image", Prices: map[string]float64{}}
					if ok {
						t.Prices["request"] = price * multiplier
					} else {
						t.Reason = "上游未提供此分辨率价格"
					}
					m.Tiers = append(m.Tiers, t)
				}
			case "per_request":
				price, ok := siteNumber(p.Get("per_request_price"))
				for _, iv := range p.Get("intervals").Array() {
					v, valid := siteNumber(iv.Get("per_request_price"))
					ok = ok && valid
					price = math.Max(price, v)
				}
				if ok {
					m.Tiers = append(m.Tiers, SitePriceTier{Key: "default", Unit: "USD/request", Prices: map[string]float64{"request": price * multiplier}})
				}
			case "token", "":
				prices := siteTokenPrices(p, multiplier)
				// Conservatively bound every context tier instead of guessing output
				// length. All components must have an explicit operator limit.
				for _, iv := range p.Get("intervals").Array() {
					intervalPrices, valid := siteIntervalTokenPrices(p, iv, multiplier)
					if !valid {
						m.Reason = "阶梯倍率无效，无法确认价格"
					}
					for k, v := range intervalPrices {
						prices[k] = math.Max(prices[k], v)
					}
				}
				if _, ok := prices["input_price"]; !ok {
					m.Reason = "缺少输入单价"
				}
				if _, ok := prices["output_price"]; !ok {
					m.Reason = "缺少输出单价"
				}
				if max, ok := siteNumber(p.Get("max_reasoning_effort_multiplier")); ok && max > 1 {
					for k := range prices {
						prices[k] *= max
					}
				}
				tier := SitePriceTier{Key: "default", Unit: "USD/1M tokens", Prices: prices}
				if len(p.Get("intervals").Array()) > 0 {
					tier.Note = "保守价格上限：采用所有上下文阶梯中的最高单价"
				}
				m.Tiers = append(m.Tiers, tier)
			default:
				m.Reason = "暂不能确认此计费方式的成本"
			}
			if len(m.Tiers) == 0 && m.Reason == "" {
				m.Reason = "上游未提供可核对的价格"
			}
			out = append(out, m)
		}
	}
	return out, nil
}
func parseNewAPISiteCatalog(result gjson.Result) ([]SiteModel, error) {
	if !result.Get("data").IsArray() || !result.Get("group_ratio").IsObject() || !result.Get("usable_group").IsObject() {
		return nil, errors.New("上游价格或可用分组数据不完整")
	}
	out := []SiteModel{}
	ratios := result.Get("group_ratio").Map()
	usable := result.Get("usable_group").Map()
	for _, p := range result.Get("data").Array() {
		for groupID, group := range usable {
			if groupID == "auto" {
				continue
			}
			enabled := false
			for _, g := range p.Get("enable_groups").Array() {
				if g.String() == groupID || g.String() == "all" {
					enabled = true
				}
			}
			if !enabled {
				continue
			}
			m := SiteModel{GroupID: groupID, GroupName: group.String(), Model: p.Get("model_name").String(), Platform: "openai", Tiers: []SitePriceTier{}}
			if m.GroupName == "" {
				m.GroupName = groupID
			}
			rate, ok := siteNumber(ratios[groupID])
			if !ok {
				m.Reason = "缺少分组实际倍率"
				out = append(out, m)
				continue
			}
			for _, endpoint := range p.Get("supported_endpoint_types").Array() {
				if endpoint.String() == "image-generation" {
					m.Image = true
				}
			}
			if schema := p.Get("billing_usage_schema"); schema.IsObject() && len(schema.Map()) > 0 {
				m.Reason = "任务插件计费暂无法核价，已阻止调度"
				out = append(out, m)
				continue
			}
			if expression := p.Get("billing_expr").String(); expression != "" {
				tier, err := siteExpressionPrices(expression, rate)
				if err != nil {
					m.Reason = "动态计费规则暂无法核价，已阻止调度"
				} else {
					m.Tiers = append(m.Tiers, tier)
				}
				out = append(out, m)
				continue
			}
			if mode := p.Get("billing_mode").String(); mode != "" && mode != "ratio" {
				m.Reason = "上游计费规则不完整，已阻止调度"
				out = append(out, m)
				continue
			}
			if p.Get("quota_type").Int() == 1 {
				price, ok := siteNumber(p.Get("model_price"))
				if ok {
					tier := SitePriceTier{Key: "default", Unit: "USD/request", Prices: map[string]float64{"request": price * rate}}
					if m.Image {
						tier.Unit = "USD/image"
					}
					if strings.HasPrefix(m.Model, "dall-e") {
						factor := 2.0
						if m.Model == "dall-e-3" {
							factor = 3
						}
						tier.Prices["request"] *= factor
						tier.Note = "保守价格上限：包含 DALL-E 尺寸与高清倍率"
					}
					m.Tiers = append(m.Tiers, tier)
				}
			} else {
				ratio, ok := siteNumber(p.Get("model_ratio"))
				completion, outputOK := siteNumber(p.Get("completion_ratio"))
				if ok && outputOK {
					input := ratio * 2 * rate
					prices := map[string]float64{"input_price": input, "output_price": input * completion}
					for field, target := range map[string]string{"cache_ratio": "cache_read_price", "create_cache_ratio": "cache_write_price", "image_ratio": "image_input_price", "audio_ratio": "audio_input_price", "audio_completion_ratio": "audio_output_price"} {
						if v, valid := siteNumber(p.Get(field)); valid {
							prices[target] = input * v
						}
					}
					if cache, exists := prices["cache_write_price"]; exists {
						// New API derives its 1h cache-write ratio from 5m at 6/3.75.
						prices["cache_write_1h_price"] = cache * (6.0 / 3.75)
					}
					m.Tiers = append(m.Tiers, SitePriceTier{Key: "default", Unit: "USD/1M tokens", Prices: prices})
				}
			}
			if len(m.Tiers) == 0 {
				m.Reason = "缺少上游计费单价"
			}
			out = append(out, m)
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

// Each binding owns a deterministic key name. Always search before creating,
// including after an ambiguous timeout or a process restart.
func (a *siteAdapter) ensureKey(ctx context.Context, b *SiteBinding) (string, error) {
	if key := a.credentials.Keys[b.ID]; key != "" {
		return key, nil
	}
	if err := a.authenticate(ctx); err != nil {
		return "", err
	}
	name := "s2site-" + b.ID
	var key string
	if a.site.Kind == "kongfang" {
		var err error
		key, err = a.ensureKongfangKey(ctx, name)
		if err != nil {
			return "", err
		}
	} else if a.site.Kind == "sub2api" {
		path := "/api/v1/keys?search=" + url.QueryEscape(name) + "&page_size=100"
		result, err := a.request(ctx, http.MethodGet, path, nil)
		if err != nil {
			return "", err
		}
		for _, item := range result.Get("data.items").Array() {
			if item.Get("name").String() == name && item.Get("group_id").String() == b.GroupID {
				key = item.Get("key").String()
				if key == "" {
					return "", errors.New("已找到上游 API Key，但上游未允许读取完整密钥")
				}
				break
			}
		}
		if key == "" {
			gid, err := strconv.ParseInt(b.GroupID, 10, 64)
			if err != nil {
				return "", errors.New("上游分组 ID 无效")
			}
			result, err = a.request(ctx, http.MethodPost, "/api/v1/keys", map[string]any{"name": name, "group_id": gid})
			if err != nil {
				return "", err
			}
			key = result.Get("data.key").String()
		}
	} else {
		path := "/api/token/search?keyword=" + url.QueryEscape(name) + "&page_size=100"
		lookup := func() (int64, error) {
			result, err := a.request(ctx, http.MethodGet, path, nil)
			if err != nil {
				return 0, err
			}
			items := result.Get("data.items")
			if !items.IsArray() {
				items = result.Get("data")
			}
			for _, item := range items.Array() {
				if item.Get("name").String() == name && item.Get("group").String() == b.GroupID {
					return item.Get("id").Int(), nil
				}
			}
			return 0, nil
		}
		id, err := lookup()
		if err != nil {
			return "", err
		}
		if id == 0 {
			_, err = a.request(ctx, http.MethodPost, "/api/token/", map[string]any{"name": name, "group": b.GroupID, "expired_time": -1, "unlimited_quota": true, "model_limits_enabled": true, "model_limits": b.Model, "cross_group_retry": false})
			if err != nil {
				return "", err
			}
			id, err = lookup()
			if err != nil {
				return "", err
			}
		}
		if id == 0 {
			return "", errors.New("上游令牌创建结果尚未可见，请重试绑定")
		}
		result, err := a.request(ctx, http.MethodPost, fmt.Sprintf("/api/token/%d/key", id), map[string]any{})
		if err != nil {
			return "", err
		}
		key = result.Get("data.key").String()
		if key != "" && !strings.HasPrefix(key, "sk-") {
			key = "sk-" + key
		}
	}
	if key == "" || strings.Contains(key, "*") {
		return "", errors.New("上游未返回可用的完整 API Key")
	}
	if a.credentials.Keys == nil {
		a.credentials.Keys = map[string]string{}
	}
	a.credentials.Keys[b.ID] = key
	return key, nil
}
