package service

const NotificationEmailEventUpstreamSiteBalanceLow = "upstream_site.balance_low"

var notificationEmailUpstreamSiteBalanceInfo = NotificationEmailEventInfo{
	Event:       NotificationEmailEventUpstreamSiteBalanceLow,
	Label:       "Upstream site low balance",
	Description: "Automatically sent to system admin notification emails, or the primary administrator when none are configured, when an enabled upstream site's balance is below its threshold.",
	Category:    "admin",
	Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
		"upstream_site_name", "upstream_site_url", "current_balance", "currency", "threshold", "triggered_at"),
}

var notificationEmailUpstreamSiteBalanceTemplates = map[string]notificationEmailOfficialTemplate{
	notificationEmailDefaultLocale: {
		Subject: "[{{site_name}}] Upstream site low balance - {{upstream_site_name}}",
		HTML: notificationEmailCard("#d97706", "Upstream site low balance", `
<p>The balance of <strong>{{upstream_site_name}}</strong> is below its alert threshold.</p>
<p>Current balance: <strong>{{current_balance}} {{currency}}</strong></p>
<p>Threshold: {{threshold}} {{currency}}</p>
<p>Checked at: {{triggered_at}}</p>
<p><a class="button" href="{{upstream_site_url}}">Open upstream site</a></p>
<p class="muted">Amounts use the upstream site's own display units. Balances are checked every 5 minutes. A sustained low balance triggers a reminder every 24 hours; a new drop after recovery triggers another alert. Manage alerts in system email settings.</p>`),
	},
	notificationEmailLocaleChinese: {
		Subject: "[{{site_name}}] 站点余额不足 - {{upstream_site_name}}",
		HTML: notificationEmailCard("#d97706", "站点余额不足", `
<p>上游站点 <strong>{{upstream_site_name}}</strong> 的余额已低于提醒阈值。</p>
<p>当前余额：<strong>{{current_balance}} {{currency}}</strong></p>
<p>提醒阈值：{{threshold}} {{currency}}</p>
<p>查询时间：{{triggered_at}}</p>
<p><a class="button" href="{{upstream_site_url}}">前往上游站点</a></p>
<p class="muted">金额按上游站点显示单位计算。每 5 分钟查询余额，持续低余额每 24 小时提醒一次，恢复后再次降低会重新提醒。提醒开关和阈值统一在系统邮件设置中管理。</p>`),
	},
}
