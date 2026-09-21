package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const siteBalanceSettingsKey = "upstream_site_balance_notification"
const siteBalanceInterval = 5 * time.Minute
const siteBalanceReminderInterval = 24 * time.Hour

// Balance freshness and mail delivery are independent of catalogue sync. Missing
// or malformed upstream data must never turn into a synthetic zero balance.
type SiteBalance struct {
	Amount        *float64   `json:"amount,omitempty"`
	Currency      string     `json:"currency,omitempty"`
	LastAttempt   *time.Time `json:"last_attempt,omitempty"`
	LastSuccess   *time.Time `json:"last_success,omitempty"`
	NextCheck     *time.Time `json:"next_check,omitempty"`
	Error         string     `json:"error,omitempty"`
	LastNotified  *time.Time `json:"last_notified,omitempty"`
	NextNotify    *time.Time `json:"next_notify,omitempty"`
	NotifiedEmail string     `json:"notified_email,omitempty"`
	NotifyError   string     `json:"notify_error,omitempty"`
	LowSince      *time.Time `json:"low_since,omitempty"`
}

type SiteBalanceSettings struct {
	Enabled        bool    `json:"enabled"`
	Threshold      float64 `json:"threshold"`
	AdminEmail     string  `json:"admin_email"`
	SMTPConfigured bool    `json:"smtp_configured"`
}

type siteBalanceMailer interface {
	SendEmail(context.Context, string, string, string) error
}

func (s *UpstreamSiteService) BalanceSettings(ctx context.Context) (*SiteBalanceSettings, error) {
	out := &SiteBalanceSettings{Threshold: 20}
	if s.balanceSettings == nil {
		return out, nil
	}
	values, err := s.balanceSettings.GetMultiple(ctx, []string{siteBalanceSettingsKey, SettingKeySMTPHost, SettingKeySMTPFrom})
	if err != nil {
		return nil, errors.New("读取站点余额提醒设置失败")
	}
	if raw := values[siteBalanceSettingsKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			return nil, errors.New("站点余额提醒设置无效，请重新保存")
		}
	}
	out.SMTPConfigured = strings.TrimSpace(values[SettingKeySMTPHost]) != "" && strings.TrimSpace(values[SettingKeySMTPFrom]) != ""
	return out, nil
}

func (s *UpstreamSiteService) SaveBalanceSettings(ctx context.Context, input SiteBalanceSettings) (*SiteBalanceSettings, error) {
	input.AdminEmail = strings.TrimSpace(input.AdminEmail)
	if math.IsNaN(input.Threshold) || math.IsInf(input.Threshold, 0) || input.Threshold <= 0 || input.Threshold > 1e12 {
		return nil, errors.New("余额提醒阈值必须大于 0 且不超过 1000000000000")
	}
	address, err := mail.ParseAddress(input.AdminEmail)
	if (input.Enabled || input.AdminEmail != "") && (err != nil || address.Address != input.AdminEmail || strings.ContainsAny(input.AdminEmail, "\r\n")) {
		return nil, errors.New("请输入有效的管理员收件邮箱")
	}
	if s.balanceSettings == nil {
		return nil, errors.New("余额提醒设置不可用")
	}
	input.SMTPConfigured = false // This is a read-only capability, never user input.
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	if err = s.balanceSettings.Set(ctx, siteBalanceSettingsKey, string(raw)); err != nil {
		return nil, errors.New("保存站点余额提醒设置失败")
	}
	return s.BalanceSettings(ctx)
}

func (s *UpstreamSiteService) RefreshBalance(ctx context.Context, id string) (*UpstreamSite, error) {
	return s.refreshBalance(ctx, id, false)
}

func (s *UpstreamSiteService) refreshBalance(ctx context.Context, id string, dueOnly bool) (*UpstreamSite, error) {
	unlock, err := s.repo.Lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	site, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if dueOnly && (!site.Enabled || (site.Balance != nil && site.Balance.NextCheck != nil && site.Balance.NextCheck.After(now))) {
		return site, nil
	}
	if site.Balance == nil {
		site.Balance = &SiteBalance{}
	}
	b := site.Balance
	b.LastAttempt = &now
	next := now.Add(siteBalanceInterval)
	b.NextCheck = &next
	credentials, queryErr := s.credentials(site)
	if queryErr == nil {
		var amount float64
		var currency string
		amount, currency, queryErr = newSiteAdapter(site, credentials).balance(ctx)
		if queryErr == nil {
			b.Amount, b.Currency, b.LastSuccess, b.Error = &amount, currency, &now, ""
		}
	}
	if queryErr != nil {
		b.Error = queryErr.Error()
	}
	// Even a disconnected caller must not lose rotated upstream tokens or the
	// notification lease. SMTP uses its own bounded connection deadlines.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if credentials != nil {
		err = s.saveSecret(persistCtx, site, credentials)
	} else {
		err = s.repo.Save(persistCtx, site)
	}
	if err != nil {
		return nil, err
	}
	if queryErr == nil && site.Enabled {
		if err = s.notifySiteBalance(persistCtx, site, now); err != nil {
			return nil, err
		}
	}
	return site, nil
}

func (s *UpstreamSiteService) notifySiteBalance(ctx context.Context, site *UpstreamSite, now time.Time) error {
	b := site.Balance
	settings, err := s.BalanceSettings(ctx)
	if err != nil {
		b.NotifyError = err.Error()
		return s.repo.Save(ctx, site)
	}
	if b.Amount == nil || !settings.Enabled {
		return nil
	}
	if *b.Amount >= settings.Threshold {
		b.LowSince, b.NextNotify, b.NotifyError = nil, nil, ""
		return s.repo.Save(ctx, site)
	}
	if b.LowSince == nil {
		b.LowSince = &now
	}
	if b.NotifiedEmail == settings.AdminEmail && b.NextNotify != nil && b.NextNotify.After(now) {
		return nil
	}
	// Persist a short lease before sending. The PostgreSQL site lock serializes
	// preview, production and repeated clicks; a crash cannot cause rapid spam.
	retry := now.Add(10 * time.Minute)
	b.NextNotify, b.NotifiedEmail = &retry, settings.AdminEmail
	b.NotifyError = "邮件正在发送；若进程中断，将在 10 分钟后重试"
	if err = s.repo.Save(ctx, site); err != nil {
		return err
	}
	if !settings.SMTPConfigured || s.balanceMailer == nil || settings.AdminEmail == "" {
		b.NotifyError = "邮件未发送，请检查 SMTP 配置和管理员收件邮箱"
	} else {
		subject := "[Sub2API] 站点余额不足提醒"
		body := fmt.Sprintf("<h2>站点余额不足</h2><p>站点：%s</p><p>地址：%s</p><p>当前余额：<strong>%s %s</strong></p><p>提醒阈值：%s %s（严格低于时提醒）</p><p>查询时间：%s</p><p>请及时前往上游站点充值。金额按上游站点显示单位计算，未折算汇率。持续低余额每 24 小时提醒一次，余额恢复后再次降低会重新提醒。</p>",
			html.EscapeString(site.Name), html.EscapeString(site.BaseURL), balanceAmount(*b.Amount), html.EscapeString(b.Currency), balanceAmount(settings.Threshold), html.EscapeString(b.Currency), now.Format(time.RFC3339))
		if err = s.balanceMailer.SendEmail(ctx, settings.AdminEmail, subject, body); err != nil {
			// SMTP errors can contain server responses or credentials.
			b.NotifyError = "邮件发送失败，请检查 SMTP 配置；10 分钟后重试"
		} else {
			next := now.Add(siteBalanceReminderInterval)
			b.LastNotified, b.NextNotify, b.NotifyError = &now, &next, ""
		}
	}
	return s.repo.Save(ctx, site)
}

func balanceAmount(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }

func (a *siteAdapter) balance(ctx context.Context) (float64, string, error) {
	if a.site.Kind != "sub2api" && a.site.Kind != "newapi" && a.site.Kind != "kongfang" {
		return 0, "", errors.New("此站点类型暂不支持余额查询")
	}
	if err := a.authenticate(ctx); err != nil {
		return 0, "", err
	}
	path := "/api/v1/auth/me"
	if a.site.Kind == "kongfang" {
		path = "/api/v1/user/profile"
	}
	if a.site.Kind == "newapi" {
		path = "/api/user/self"
	}
	profile, err := a.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, "", err
	}
	if a.site.Kind == "sub2api" || a.site.Kind == "kongfang" {
		if a.site.Kind == "kongfang" && (profile.Get("data.id").Int() != a.site.UserID || profile.Get("data.status").String() != "active") {
			return 0, "", errors.New("空凡账号身份不匹配或账号未启用")
		}
		amount, ok := siteBalanceNumber(profile.Get("data.balance"))
		if !ok {
			return 0, "", errors.New("上游未提供有效余额")
		}
		if a.site.Kind == "kongfang" {
			return amount, "积分", nil
		}
		return amount, "USD", nil
	}
	status, err := a.request(ctx, http.MethodGet, "/api/status", nil)
	if err != nil {
		return 0, "", err
	}
	return parseNewAPISiteBalance(profile.Get("data"), status.Get("data"))
}

func siteBalanceNumber(value gjson.Result) (float64, bool) {
	if value.Type != gjson.Number && value.Type != gjson.String {
		return 0, false
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(value.String()), 64)
	return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
}

func parseNewAPISiteBalance(profile, status gjson.Result) (float64, string, error) {
	invalid := errors.New("上游余额或显示单位配置不完整，无法查询余额")
	quota, ok := siteBalanceNumber(profile.Get("quota"))
	if !ok {
		return 0, "", invalid
	}
	currency := status.Get("quota_display_type").String()
	if currency == "" {
		// Legacy New API only exposes this boolean: true is USD, false is quota.
		switch status.Get("display_in_currency").Type {
		case gjson.True:
			currency = "USD"
		case gjson.False:
			currency = "TOKENS"
		default:
			return 0, "", invalid
		}
	}
	if currency == "TOKENS" {
		return quota, "QUOTA", nil
	}
	unit, ok := siteBalanceNumber(status.Get("quota_per_unit"))
	if !ok || unit <= 0 {
		return 0, "", invalid
	}
	amount := quota / unit
	switch currency {
	case "USD":
	case "CNY", "CUSTOM":
		key := "usd_exchange_rate"
		if currency == "CUSTOM" {
			key = "custom_currency_exchange_rate"
			currency = strings.TrimSpace(status.Get("custom_currency_symbol").String())
			if currency == "" || len(currency) > 24 || strings.ContainsAny(currency, "\r\n") {
				return 0, "", invalid
			}
		}
		rate, ok := siteBalanceNumber(status.Get(key))
		if !ok || rate <= 0 {
			return 0, "", invalid
		}
		amount *= rate
	default:
		return 0, "", invalid
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0, "", invalid
	}
	return amount, currency, nil
}
