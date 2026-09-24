package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

// Available groups are authoritative for this login. Exclusive groups can be
// intentionally absent from the public price page even though the user can use
// them. Reuse existing keys, or provision one discovery key per available group.
func (a *siteAdapter) privatePricingCatalog(ctx context.Context, public, available, rates gjson.Result) ([]SiteModel, error) {
	publicGroups := map[string]bool{}
	platforms := map[string]string{}
	imageModels := map[string]bool{}
	for _, group := range public.Get("groups").Array() {
		publicGroups[group.Get("id").String()] = true
		platforms[group.Get("id").String()] = group.Get("platform").String()
	}
	for _, model := range public.Get("models").Array() {
		if model.Get("billing_mode").String() == "image" {
			platform := model.Get("platform").String()
			if platform == "" {
				platform = platforms[model.Get("group_id").String()]
			}
			imageModels[platform+"\x00"+model.Get("name").String()] = true
		}
	}
	models := []SiteModel{}
	for _, group := range available.Array() {
		id := group.Get("id").String()
		if id == "" || publicGroups[id] || (group.Get("status").Exists() && group.Get("status").String() != StatusActive) {
			continue
		}
		catalog, err := a.groupKeyModelCatalog(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			a.warnings = append(a.warnings, fmt.Sprintf("分组「%s」同步失败：%s", group.Get("name").String(), err))
			a.markGroupCatalogFailed(id, err)
			continue
		}
		seen := map[string]bool{}
		for _, item := range catalog.Array() {
			name := strings.TrimSpace(item.Get("id").String())
			if name == "" {
				return nil, errors.New("专属分组模型目录缺少模型名称")
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			m := SiteModel{GroupID: id, GroupName: group.Get("name").String(), Model: name, Platform: group.Get("platform").String(), Tiers: []SitePriceTier{}}
			m.Image = imageModels[m.Platform+"\x00"+name]
			switch {
			case rates.Map()[id].Exists():
				m.Reason = "分组价格未包含个人专属费率，暂无法确认实际成本"
			case group.Get("peak_rate_enabled").Bool():
				m.Reason = "专属分组未提供完整的高峰计费上限"
			case !m.Image || !group.Get("allow_image_generation").Bool():
				m.Reason = "专属分组未提供此模型的完整计费单价"
			default:
				// The authenticated group API exposes final image prices, with
				// multipliers redacted. Never copy prices from another group or
				// parse the free-form description (which may contain old prices).
				m.Tiers = sitePublicImageTiers(group)
			}
			models = append(models, m)
		}
	}
	return models, nil
}

var errNoSiteGroupKey = errors.New("该分组没有可用 API Key")

func (a *siteAdapter) existingKeyModelCatalog(ctx context.Context, groupID string) (gjson.Result, error) {
	var lastErr error
	// Bound pagination and try other existing keys if the first one has expired.
	// Always check the returned group ID, even if the server ignores our filter.
	for page := 1; page <= 20; page++ {
		path := fmt.Sprintf("/api/v1/keys?group_id=%s&page=%d&page_size=100", url.QueryEscape(groupID), page)
		keys, err := a.request(ctx, http.MethodGet, path, nil)
		if err != nil {
			return gjson.Result{}, err
		}
		items := keys.Get("data.items")
		if !items.IsArray() {
			return gjson.Result{}, errors.New("上游未返回可用的 API Key 列表")
		}
		for _, item := range items.Array() {
			if item.Get("group_id").String() != groupID || item.Get("status").String() != StatusActive {
				continue
			}
			key := item.Get("key").String()
			if key == "" || strings.Contains(key, "*") {
				continue
			}
			catalog, err := a.requestWithHeaders(ctx, http.MethodGet, "/v1/models", nil, map[string]string{"Authorization": "Bearer " + key})
			if err != nil {
				lastErr = err
				continue
			}
			if !catalog.Get("data").IsArray() {
				lastErr = errors.New("上游未返回完整的模型目录")
				continue
			}
			return catalog.Get("data"), nil
		}
		if len(items.Array()) < 100 || (keys.Get("data.total").Exists() && int64(page*100) >= keys.Get("data.total").Int()) {
			break
		}
		if page == 20 {
			return gjson.Result{}, errors.New("上游 Key 数量过多，未完成查找，请缩小分组 Key 列表后重试")
		}
	}
	if lastErr != nil {
		return gjson.Result{}, lastErr
	}
	return gjson.Result{}, errNoSiteGroupKey
}

// A stable identity plus the site lock makes retries recover the same key,
// including after a successful upstream create followed by a local save failure.
func (a *siteAdapter) groupKeyModelCatalog(ctx context.Context, groupID string) (gjson.Result, error) {
	id := "catalog-" + a.site.ID + "-" + groupID
	read := func(key string) (gjson.Result, error) {
		result, err := a.requestWithHeaders(ctx, http.MethodGet, "/v1/models", nil, map[string]string{"Authorization": "Bearer " + key})
		if err != nil {
			return gjson.Result{}, err
		}
		if !result.Get("data").IsArray() {
			return gjson.Result{}, errors.New("上游未返回完整的模型目录")
		}
		return result.Get("data"), nil
	}
	if key := a.credentials.Keys[id]; key != "" {
		result, err := read(key)
		if err == nil {
			return result, nil
		}
		var remote *siteRemoteError
		if !errors.As(err, &remote) || (remote.Status != 401 && remote.Status != 403) {
			return gjson.Result{}, err
		}
		delete(a.credentials.Keys, id)
	}
	result, err := a.existingKeyModelCatalog(ctx, groupID)
	if err == nil {
		return result, nil
	}
	var remote *siteRemoteError
	if !errors.Is(err, errNoSiteGroupKey) && (!errors.As(err, &remote) || (remote.Status != 401 && remote.Status != 403)) {
		return gjson.Result{}, err
	}
	key, err := a.ensureKey(ctx, &SiteBinding{ID: id, GroupID: groupID})
	if err != nil {
		return gjson.Result{}, fmt.Errorf("自动创建或复用 API Key 失败：%w", err)
	}
	return read(key)
}
