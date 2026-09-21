# 上游站点余额与邮件提醒

后台「站点管理」自动显示选中站点的余额、单位、最近成功查询时间和邮件发送结果，保留「查询余额」作为即时查询入口。已启用站点每 5 分钟后台查询，无需打开页面或手动点击；后台每分钟检查到期任务，页面每 30 秒读取最新结果。查询与模型/价格同步独立，未开放模型广场或未填写空凡积分美元成本也可查询余额。

## 统一系统邮件管理

站点页不再提供重复的管理员邮箱配置。开关和正数阈值统一放在「系统设置 → 邮件设置 → 站点低余额提醒」。新配置默认开启、阈值 20，已有明确保存的开关和阈值继续保留；底层 `settings.upstream_site_balance_notification` 仅保存这两个选项，不再接受或使用其中旧的 `admin_email`。

收件人复用系统 `account_quota_notify_emails` 中已验证且启用的管理员通知邮箱；尚未配置此列表时使用现有主管理员账号邮箱（首个启用的管理员），不另建账号、不把个人地址写入源码。已有列表全部停用/未验证时报告缺少收件人，不绕过其设置。该列表对应的账号额度开关仅控制账号额度提醒，站点余额使用自己的邮件开关。

邮件使用系统 SMTP 和 `NotificationEmailService`，新增 `upstream_site.balance_low` 事件，可在现有邮件模板编辑器选择「站点余额不足」并编辑中英文模板。模板包含上游站点名称、地址、余额、单位、阈值和查询时间；HTML 自动转义。没有发送到 SMTP 的结果不会记为成功；SMTP 接受不等于收件箱已投递。

首次低余额立即提醒，持续低余额每 24 小时再次提醒，检测到恢复后再次低余额重新提醒。PostgreSQL 站点锁及持久化 10 分钟发送租约防止并发点击/多进程快速重复发送；统一邮件服务按站点、提醒轮次和收件人记录投递，部分失败重试及增加收件人不会重发同轮已成功的邮件。发送失败显示脱敏错误，10 分钟后重试。SMTP 已接受而随后投递记录写入失败仍可能重复发送。

## 金额和错误处理

- Sub2API 从 `/api/v1/auth/me` 读取美元余额。New API 从 `/api/user/self` 读取额度，依据 `/api/status` 的 `quota_display_type`、`quota_per_unit` 和对应倍率还原显示余额，明确标注 USD、CNY、自定义单位或原始额度，不猜测汇率。
- 空凡从 `/api/v1/user/profile` 读取原始 `balance`，单位为「积分」，不乘 VIP 折扣或美元成本；这两项只影响模型采购价。
- 以各站点原始显示金额严格比较 `< 阈值`，等于 20 不提醒；零和负余额都正常参与比较。缺失或无效余额报告错误并保留上次成功结果，失败查询不触发低余额邮件。
- 停用站点不自动查询、不发提醒，但允许手动读取余额。余额查询成功超过 10 分钟或最近查询失败时，页面标记上次结果已过期。邮件开关不改变扫描或渠道调度。

## 完整版本与预览

用户于 2026-09-21 明确要求余额自动扫描、自动邮件提醒，并复用系统邮件管理。6556 因此也启动余额扫描和相同的提醒流程，复用生产 PostgreSQL、账号及系统邮件配置。`UPSTREAM_SITES_PREVIEW=true` 仍禁止自动模型/价格同步和新建托管账号上线；余额任务不激活账号、不更改价格策略。用户已要求发送真实余额提醒；新增配置或改变收件人仍遵循系统设置。

余额、空凡和价格调度修改统一测试、提交、推送、打包并更新 6556；不得只发布一部分。生产发布仍需用户审阅并明确确认，详见 `AGENTS.md`、`ops/README.md`。

## 验证

单元测试使用 HTTP 桩和内存仓库，覆盖币种/单位转换、20 的严格边界、零/负/无效余额、凭据轮换、与价格调度解耦、邮件失败重试、并发去重、进程重启、低余额恢复和设置校验。前端测试验证过期余额提示、真实零值、查询更新及设置加载/保存。

统一版本的相关检查命令：

```bash
cd /opt/sub2api/source/backend
GOMAXPROCS=4 go test -p 2 -tags=unit ./internal/service ./internal/handler/admin ./internal/server/routes \
  -run 'TestKongfang|TestUpstreamSite|TestSiteBalance|TestNewAPISiteBalance|TestNotificationEmail' -count=1
cd ../frontend
npx --yes pnpm@9.15.9 exec vitest run \
  src/components/admin/sites/__tests__/SiteBalance.spec.ts \
  src/views/admin/__tests__/UpstreamSitesView.spec.ts \
  src/views/admin/__tests__/SettingsView.spec.ts \
  src/i18n/__tests__/localeKeyCompleteness.spec.ts
npx --yes pnpm@9.15.9 exec vue-tsc --noEmit
npx --yes pnpm@9.15.9 exec eslint src/components/admin/sites src/views/admin/UpstreamSitesView.vue \
  src/api/admin/upstreamSiteBalance.ts src/api/admin/upstreamSites.ts \
  src/i18n/locales/en/admin/siteBalance.ts src/i18n/locales/zh/admin/siteBalance.ts
cd ..
bash -n ops/build-local.sh
git diff --check HEAD
```

6556 页面验收位置：后台「站点管理」选中站点后查看余额、最近查询及邮件结果；不点「查询余额」也应由后台扫描更新。页面每 30 秒读取最新结果，服务重启或新建站点后按到期任务扫描。邮件配置及「站点余额不足」模板在「系统设置 → 邮件设置」，站点页面仅保留跳转入口。使用本地 HTTP/SMTP 桩验证模板、默认管理员收件人、收件人过滤及多收件人去重；共享生产数据库不写测试夹具。
