# 上游站点余额与邮件提醒

后台「站点管理」显示选中站点的余额、单位、最近成功查询时间和邮件发送结果，并提供「查询余额」。提醒设置独立保存于 `settings.upstream_site_balance_notification`，包含开关、正数阈值及管理员收件邮箱。默认阈值 20，邮件默认关闭，部署时通过管理员接口明确配置；不在源码中硬编码个人邮箱。

- Sub2API 从 `/api/v1/auth/me` 读取用户美元余额；New API 从 `/api/user/self` 读取额度，依据 `/api/status` 公开的 `quota_display_type`、`quota_per_unit` 及相应显示倍率还原站点余额。USD、CNY、自定义单位和原始额度分别标明，不能统一猜测汇率。New API 字段对应 [上游 GetStatus 源码](https://github.com/QuantumNous/new-api/blob/main/controller/misc.go)。
- 空凡从 `/api/v1/user/profile` 读取个人 `balance`，显示单位为「积分」。直接展示站点原始积分余额，不再乘 VIP 折扣或积分美元成本；后两者只用于模型采购价。余额查询独立于模型同步，即使未配置积分成本也能查看余额。
- 比较的是各站点显示金额，严格低于阈值才提醒，等于 20 不提醒。合法的零余额和负余额均参与比较。缺失、无效余额或不完整显示参数会显示查询错误，保留上次成功结果；失败查询不触发低余额邮件。
- 已启用站点每 5 分钟查询一次，与价格同步的成功状态和下次价格同步时间独立。无需开放模型广场也可查询余额。停用站点不自动查询、不发送提醒，但仍可手动读取余额。
- 邮件复用系统 SMTP，包含站点名称、地址、余额、单位、阈值和查询时间。首次低余额立即提醒，持续低余额每 24 小时再次提醒；检测到恢复后再次低余额会重新提醒。
- PostgreSQL 站点锁串行化查询、凭据轮换和邮件发送。通知时间与去重状态保存于站点文档，不依赖 Redis，重启和预览共用数据库时仍有效。发送前持久化 10 分钟发送租约，发送失败显示脱敏错误并在租约到期后重试。SMTP 已接受而随后数据库写入失败时，无法做到严格恰好一次，可能在租约到期后重复发送。
- 提醒开关仅控制邮件，不改变渠道调度。余额查询成功时间超过 10 分钟或最近一次查询失败，页面将上次余额标记为待查询/过期。

## 统一版本与预览

余额功能与空凡适配、价格调度、关联前后端改动统一提交和打包。不得将余额功能留在未提交工作区而只构建空凡。完整版本规则见 `AGENTS.md` 和 `ops/README.md`。

仍遵守 `ops/README.md` 的 6556 预览流程与生产 PostgreSQL 连接校验。`UPSTREAM_SITES_PREVIEW=true` 禁用自动轮询；手动查询读取真实站点，启用邮件提醒时真实低余额会发信，并使用共享 PostgreSQL 去重。保存提醒设置会直接修改共享生产设置。不要向共享数据库写测试夹具。

## 验证

单元测试使用 HTTP 桩和内存仓库，覆盖币种/单位转换、20 的严格边界、零/负/无效余额、凭据轮换、与价格调度解耦、邮件失败重试、并发去重、进程重启、低余额恢复和设置校验。前端测试验证过期余额提示、真实零值、查询更新及设置加载/保存。

统一版本的相关检查命令：

```bash
cd /opt/sub2api/source/backend
GOMAXPROCS=4 go test -p 2 -tags=unit ./internal/service ./internal/handler/admin ./internal/server/routes \
  -run 'TestKongfang|TestUpstreamSite|TestSiteBalance|TestNewAPISiteBalance' -count=1
cd ../frontend
npx --yes pnpm@9.15.9 exec vitest run \
  src/components/admin/sites/__tests__/SiteBalance.spec.ts \
  src/views/admin/__tests__/UpstreamSitesView.spec.ts \
  src/i18n/__tests__/localeKeyCompleteness.spec.ts
npx --yes pnpm@9.15.9 exec vue-tsc --noEmit
npx --yes pnpm@9.15.9 exec eslint src/components/admin/sites src/views/admin/UpstreamSitesView.vue \
  src/api/admin/upstreamSiteBalance.ts src/api/admin/upstreamSites.ts \
  src/i18n/locales/en/admin/siteBalance.ts src/i18n/locales/zh/admin/siteBalance.ts
cd ..
bash -n ops/build-local.sh
git diff --check HEAD
```

6556 页面验收位置：后台「站点管理」顶部的「站点低余额提醒」，以及选中站点后的余额卡片和「查询余额」按钮；新增站点的类型列表同时保留「空凡」。真实手动查询前先检查提醒开关，未经明确授权不要通过验收触发真实邮件。
