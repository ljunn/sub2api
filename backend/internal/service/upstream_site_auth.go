package service

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

// Only expose known authentication codes, never an upstream body that might
// contain credentials or internal diagnostics.
func siteAuthErrorDetail(code string) string {
	switch code {
	case "AUTH_SESSION_LIMIT":
		return "上游账号的登录会话数量已达上限，请复用已有登录令牌，或在上游会话管理中清理不再使用的会话"
	case "AUTH_ORIGIN_FORBIDDEN":
		return "上游拒绝刷新会话的请求来源（Origin），请检查站点地址和上游受信任来源配置；不会因此重新登录"
	case "AUTH_SESSION_MISMATCH":
		return "上游登录会话与刷新令牌不匹配，请使用同一会话的登录令牌"
	case "AUTH_REFRESH_RACE":
		return "上游刷新令牌已被其他请求更新，请稍后同步或更新登录令牌"
	case "AUTH_SESSION_ISSUANCE_LIMIT":
		return "上游限制短时间内新建登录会话，请稍后再试并复用已有登录令牌"
	default:
		return ""
	}
}

func (a *siteAdapter) refreshSiteAuth(ctx context.Context) (gjson.Result, error) {
	if a.site.Kind != "newapi" {
		return a.request(ctx, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": a.credentials.RefreshToken})
	}
	const path = "/api/user/auth/refresh"
	payload := map[string]string{}
	result, err := a.request(ctx, http.MethodPost, path, payload)
	var remote *siteRemoteError
	if !errors.As(err, &remote) || remote.Status != http.StatusForbidden || remote.Code != "AUTH_ORIGIN_FORBIDDEN" {
		return result, err
	}
	// An IP/alternate endpoint may be usable for API traffic while cookie
	// refresh only accepts the canonical dashboard origin advertised by New API.
	// Keep every request on the configured endpoint; only change the Origin.
	status, statusErr := a.request(ctx, http.MethodGet, "/api/status", nil)
	if statusErr != nil {
		return gjson.Result{}, err
	}
	origin, ok := siteCanonicalAuthOrigin(status.Get("data.server_address").String())
	if !ok {
		return gjson.Result{}, err
	}
	configured, _ := url.Parse(a.site.BaseURL)
	if origin == configured.Scheme+"://"+configured.Host {
		return gjson.Result{}, err
	}
	return a.requestWithHeaders(ctx, http.MethodPost, path, payload, map[string]string{"Origin": origin})
}

func siteCanonicalAuthOrigin(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}
