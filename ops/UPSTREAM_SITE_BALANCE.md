# 上游站点余额与邮件提醒

后台「站点管理」总览统一显示 USD 余额，站点弹窗显示美元余额、换算倍率、最近成功查询时间和邮件发送结果，保留「查询余额」作为即时查询入口。已启用站点每 5 分钟后台查询，无需打开页面或手动点击；后台每分钟检查到期任务，页面每 30 秒读取最新结果。查询与模型/价格同步独立，未开放模型广场或未填写空凡积分美元成本也可查询余额。

## 统一系统邮件管理

站点页不再提供重复的管理员邮箱配置。开关和正数阈值统一放在「系统设置 → 邮件设置 → 站点低余额提醒」。新配置默认开启、阈值 20 USD，已有明确保存的开关和阈值继续保留；底层 `settings.upstream_site_balance_notification` 仅保存这两个选项，不再接受或使用其中旧的 `admin_email`。

收件人复用系统 `account_quota_notify_emails` 中已验证且启用的管理员通知邮箱；尚未配置此列表时使用现有主管理员账号邮箱（首个启用的管理员），不另建账号、不把个人地址写入源码。已有列表全部停用/未验证时报告缺少收件人，不绕过其设置。该列表对应的账号额度开关仅控制账号额度提醒，站点余额使用自己的邮件开关。

邮件使用系统 SMTP 和 `NotificationEmailService`，新增 `upstream_site.balance_low` 事件，可在现有邮件模板编辑器选择「站点余额不足」并编辑模板。站点余额提醒固定选择中文模板，不受收件人或浏览器的英文语言偏好影响，主题、正文、按钮和自动发送页脚均使用中文。模板包含上游站点名称、地址、余额、单位、阈值和查询时间；HTML 自动转义。没有发送到 SMTP 的结果不会记为成功；SMTP 接受不等于收件箱已投递。

首次低余额立即提醒，持续低余额每 24 小时再次提醒，检测到恢复后再次低余额重新提醒。PostgreSQL 站点锁及持久化 10 分钟发送租约防止并发点击/多进程快速重复发送；统一邮件服务按站点、提醒轮次和收件人记录投递，部分失败重试及增加收件人不会重发同轮已成功的邮件。发送失败显示脱敏错误，10 分钟后重试。SMTP 已接受而随后投递记录写入失败仍可能重复发送。

## USD 换算和提醒口径

- 所有站点的余额主显示和提醒阈值统一为 USD，低余额筛选也使用换算后的 `amount_usd`。原始 `amount/currency` 保留用于审计，不再拿原币金额直接比较美元阈值。
- 新增/编辑站点时填写「1 USD = 多少原币或积分」，提供 `1:100`、`1:10`、`1:1` 快捷选项。公式是 `USD 余额 = 原余额 ÷ 所填倍率`：1:100 时 1,900 积分 = 19 USD，2,000 积分 = 20 USD；1:10 时 190 CNY = 19 USD。倍率保存在 `balance_units_per_usd`，必须是有限正数，界面留空（接口传 0）表示使用已有/上游配置。
- 默认口径有明确来源：USD 为 1:1；New API 读取 `/api/status` 中原币显示倍率，先还原原余额，再按相同倍率折回 USD；管理员填写的倍率优先。空凡复用已有的每积分美元成本，换成其倒数展示；填写新倍率时同步更新积分采购价成本，重新同步模型价格，保证余额与采购价口径一致。
- 无法获得倍率时显示「待设置换算倍率」，不假设 1:1、不当作余额为零，也不发送错误提醒。零和负余额正常换算；无效余额查询保留上次结果，不发低余额邮件。空凡余额仍不乘 VIP 折扣，VIP 折扣仅作用于采购价。
- 严格使用 `USD 余额 < USD 阈值`，等于 20 USD 不提醒。修改倍率立即重算已知余额并安排下一轮自动查询；提醒状态也重新评估，通常下一分钟到期扫描即可触发，不需要手动刷新。
- 停用站点不自动查询、不发提醒，但允许手动读取余额。余额超过 10 分钟或查询失败时显示过期，提醒开关不改变余额扫描。普通站点的余额换算不会修改已发布的 USD 模型价格；空凡的积分价格使用上述同一倍率。

## 完整版本与预览

用户于 2026-09-21 明确要求余额自动扫描、自动邮件提醒，并复用系统邮件管理。6556 因此也启动余额扫描和相同的提醒流程，复用生产 PostgreSQL、账号及系统邮件配置。用户随后明确要求通过分组隔离测试、允许预览账号正常调度，预览改为 `UPSTREAM_SITES_PREVIEW=false` 并恢复模型/价格同步；余额任务本身不激活账号、不更改价格策略。用户已要求发送真实余额提醒；新增配置或改变收件人仍遵循系统设置。

余额、空凡和价格调度修改统一测试、提交、推送、打包并更新 6556；不得只发布一部分。生产发布仍需用户审阅并明确确认，详见 `AGENTS.md`、`ops/README.md`。

## 验证

单元测试使用 HTTP 桩和内存仓库，覆盖 1:100/1:10/1:1 的 USD 换算、上游倍率与手工覆盖、未知倍率、空凡成本一致性、固定中文邮件及页脚、20 USD 的严格边界、零/负/无效余额、凭据轮换、与价格调度解耦、邮件失败重试、并发去重、进程重启、低余额恢复和设置校验。前端测试验证过期余额提示、真实零值、查询更新及设置加载/保存。

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
