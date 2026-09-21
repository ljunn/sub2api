package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
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
	AmountUSD         *float64   `json:"amount_usd,omitempty"`
	UnitsPerUSD       float64    `json:"units_per_usd,omitempty"`
	NativeUnitsPerUSD float64    `json:"native_units_per_usd,omitempty"`
	ConversionError   string     `json:"conversion_error,omitempty"`
	Amount            *float64   `json:"amount,omitempty"`
	Currency          string     `json:"currency,omitempty"`
	LastAttempt       *time.Time `json:"last_attempt,omitempty"`
	LastSuccess       *time.Time `json:"last_success,omitempty"`
	NextCheck         *time.Time `json:"next_check,omitempty"`
	Error             string     `json:"error,omitempty"`
	LastNotified      *time.Time `json:"last_notified,omitempty"`
	NextNotify        *time.Time `json:"next_notify,omitempty"`
	NotifiedEmail     string     `json:"notified_email,omitempty"`
	NotifyError       string     `json:"notify_error,omitempty"`
	LowSince          *time.Time `json:"low_since,omitempty"`
	NotifyCycle       string     `json:"notify_cycle,omitempty"`
}

type SiteBalanceSettings struct {
	Enabled        bool     `json:"enabled"`
	Threshold      float64  `json:"threshold"`
	SMTPConfigured bool     `json:"smtp_configured"`
	Recipients     []string `json:"recipients"`
}

type siteBalanceMailer interface {
	Send(context.Context, NotificationEmailSendInput) error
}

func (s *UpstreamSiteService) BalanceSettings(ctx context.Context) (*SiteBalanceSettings, error) {
	out := &SiteBalanceSettings{Enabled: true, Threshold: 20, Recipients: []string{}}
	if s.balanceSettings == nil {
		return out, nil
	}
	values, err := s.balanceSettings.GetMultiple(ctx, []string{siteBalanceSettingsKey, SettingKeySMTPHost, SettingKeySMTPFrom, SettingKeyAccountQuotaNotifyEmails})
	if err != nil {
		return nil, errors.New("读取站点余额提醒设置失败")
	}
	if raw := values[siteBalanceSettingsKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			return nil, errors.New("站点余额提醒设置无效，请重新保存")
		}
	}
	out.SMTPConfigured = strings.TrimSpace(values[SettingKeySMTPHost]) != "" && strings.TrimSpace(values[SettingKeySMTPFrom]) != ""
	// Recipient management belongs to the existing system email settings. The
	// old per-site admin_email field is deliberately ignored.
	out.Recipients = filterVerifiedEmails(ParseNotifyEmails(values[SettingKeyAccountQuotaNotifyEmails]))
	if raw := strings.TrimSpace(values[SettingKeyAccountQuotaNotifyEmails]); (raw == "" || raw == "[]") && s.balanceUsers != nil {
		admin, err := s.balanceUsers.GetFirstAdmin(ctx)
		if err != nil {
			return nil, errors.New("读取系统管理员通知邮箱失败")
		}
		if admin != nil && admin.IsActive() && admin.Role == RoleAdmin && strings.TrimSpace(admin.Email) != "" {
			out.Recipients = []string{strings.TrimSpace(admin.Email)}
		}
	}
	if out.Recipients == nil {
		out.Recipients = []string{}
	}
	sort.Strings(out.Recipients)
	return out, nil
}

func (s *UpstreamSiteService) SaveBalanceSettings(ctx context.Context, input SiteBalanceSettings) (*SiteBalanceSettings, error) {
	if math.IsNaN(input.Threshold) || math.IsInf(input.Threshold, 0) || input.Threshold <= 0 || input.Threshold > 1e12 {
		return nil, errors.New("余额提醒阈值必须大于 0 且不超过 1000000000000")
	}
	if s.balanceSettings == nil {
		return nil, errors.New("余额提醒设置不可用")
	}
	// Capabilities and recipients are resolved from system configuration, never
	// persisted from client input.
	raw, err := json.Marshal(struct {
		Enabled   bool    `json:"enabled"`
		Threshold float64 `json:"threshold"`
	}{input.Enabled, input.Threshold})
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
		adapter := newSiteAdapter(site, credentials)
		amount, currency, queryErr = adapter.balance(ctx)
		if queryErr == nil {
			b.Amount, b.Currency, b.LastSuccess, b.Error = &amount, currency, &now, ""
			b.NativeUnitsPerUSD = adapter.balanceNativeRate
		}
	}
	if queryErr != nil {
		b.Error = queryErr.Error()
	}
	convertSiteBalanceUSD(site)
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
	convertSiteBalanceUSD(site)
	settings, err := s.BalanceSettings(ctx)
	if err != nil {
		b.NotifyError = err.Error()
		return s.repo.Save(ctx, site)
	}
	if b.AmountUSD == nil {
		return nil
	}
	if *b.AmountUSD >= settings.Threshold {
		b.LowSince, b.NextNotify, b.NotifyError = nil, nil, ""
		b.NotifyCycle = ""
		return s.repo.Save(ctx, site)
	}
	if !settings.Enabled {
		return nil
	}
	if b.LowSince == nil {
		b.LowSince = &now
	}
	recipients := strings.Join(settings.Recipients, ",")
	if b.NotifiedEmail == recipients && b.NextNotify != nil && b.NextNotify.After(now) {
		return nil
	}
	if b.NotifyCycle == "" || (b.NotifyError == "" && (b.NextNotify == nil || !b.NextNotify.After(now))) {
		b.NotifyCycle = now.Format(time.RFC3339Nano)
	}
	// Persist a short lease before sending. The PostgreSQL site lock serializes
	// preview, production and repeated clicks; a crash cannot cause rapid spam.
	retry := now.Add(10 * time.Minute)
	b.NextNotify, b.NotifiedEmail = &retry, recipients
	b.NotifyError = "邮件正在发送；若进程中断，将在 10 分钟后重试"
	if err = s.repo.Save(ctx, site); err != nil {
		return err
	}
	if !settings.SMTPConfigured || s.balanceMailer == nil || len(settings.Recipients) == 0 {
		b.NotifyError = "邮件未发送，请检查系统邮件设置中的 SMTP 和管理员通知邮箱"
	} else {
		failed := false
		for _, recipient := range settings.Recipients {
			err := s.balanceMailer.Send(ctx, NotificationEmailSendInput{
				Event:          NotificationEmailEventUpstreamSiteBalanceLow,
				Locale:         notificationEmailLocaleChinese,
				RecipientEmail: recipient, RecipientName: emailRecipientName(recipient),
				SourceType: "upstream_site", SourceID: site.ID, ReminderKey: b.NotifyCycle,
				Variables: map[string]string{"upstream_site_name": site.Name, "upstream_site_url": site.BaseURL,
					"current_balance": balanceAmount(*b.AmountUSD), "currency": "USD",
					"threshold": balanceAmount(settings.Threshold), "triggered_at": now.Format(time.RFC3339)},
			})
			failed = failed || err != nil
		}
		if failed {
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

// Keep the original amount for audit, but display and alert only in USD. A
// missing conversion is unknown, not a zero or an assumed 1:1 exchange rate.
func convertSiteBalanceUSD(site *UpstreamSite) {
	b := site.Balance
	if b == nil {
		return
	}
	b.AmountUSD, b.ConversionError = nil, ""
	rate := site.BalanceUnitsPerUSD
	if rate == 0 {
		switch {
		case b.Currency == "USD":
			rate = 1
		case site.Kind == "kongfang" && site.CreditUSD > 0:
			rate = 1 / site.CreditUSD
		default:
			rate = b.NativeUnitsPerUSD
		}
	}
	b.UnitsPerUSD = rate
	if b.Amount == nil {
		return
	}
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		b.ConversionError = "请在站点设置中填写换算倍率：1 USD 等于多少原币或积分"
		return
	}
	usd := *b.Amount / rate
	if math.IsNaN(usd) || math.IsInf(usd, 0) {
		b.ConversionError = "余额换算结果无效，请检查换算倍率"
		return
	}
	b.AmountUSD = &usd
}

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
		a.balanceNativeRate = 1
		return amount, "USD", nil
	}
	status, err := a.request(ctx, http.MethodGet, "/api/status", nil)
	if err != nil {
		return 0, "", err
	}
	amount, currency, err := parseNewAPISiteBalance(profile.Get("data"), status.Get("data"))
	if err == nil {
		switch status.Get("data.quota_display_type").String() {
		case "CNY":
			a.balanceNativeRate = status.Get("data.usd_exchange_rate").Float()
		case "CUSTOM":
			a.balanceNativeRate = status.Get("data.custom_currency_exchange_rate").Float()
		default:
			if currency == "USD" {
				a.balanceNativeRate = 1
			} else if currency == "QUOTA" {
				a.balanceNativeRate = status.Get("data.quota_per_unit").Float()
			}
		}
	}
	return amount, currency, err
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
