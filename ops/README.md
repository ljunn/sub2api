# 本机源码部署

固定源码目录：**`/opt/sub2api/source`**。这是独立 Git 仓库，`.git` 和对象库都在此目录，不能改为指向 `/tmp` 的 worktree。

- 本机维护仓库：<https://github.com/ljunn/sub2api>，分支 `host-production`。
- 上游：`upstream` → <https://github.com/Wei-Shaw/sub2api>。
- 初始源码基线：`5de5e2bed035d43591a2e10e51f420ef6a84eb98`，对应迁移前二进制记录的提交。
- `backend/cmd/server/VERSION` 与选定的上游 release 对齐；当前源码基线已合入 `v0.2.8` 及版本号同步提交 `a3eb7ef30`，本机源码版本为 `0.2.8`。上游 release 工作流会在打包时更新此文件，tag 内可能仍是旧值，合并时须按 release 修正。
- 生产仍由 `sub2api.service` 管理，工作目录 `/opt/sub2api`，端口 **7654**。
- 配置 `/opt/sub2api/config.yaml`、安装标记、现有 PostgreSQL/Redis 和业务数据沿用现有部署，不进入 Git。
- `/opt/image2api` 是独立项目，不要为了修 Sub2API 修改它或切换其 6555 预览。
- 后续各个 custom 渠道的账号池、模型路由、失败切换、重试、熔断、限流和健康调度统一放在 Sub2API；image2api 的 custom 适配层主要负责连接 Sub2API、转发请求和处理统一响应。

## 完整版本统一提交与发布

用户于 **2026-09-21** 明确要求：**所有待发布改动一起提交、推送、打包，不能只发布其中一部分。** 本约定同时适用于 6556 审阅预览和生产发布。

1. 在 `/opt/sub2api/source` 检查 `git status --short`、已暂存和未暂存差异，以及未跟踪源码；把全部待发布功能及关联前端、后端、配置、测试、文档纳入同一个完整版本。保留已有工作，不能只提交最近新增的功能；凭据、私有运行配置和临时产物不提交。
2. 对完整工作区完成测试，提交并推送 `host-production`，确认工作区干净。可以保留已有提交历史，但最后的 `HEAD` 必须包含所有待发布功能。
3. 运行 `./ops/build-local.sh`。脚本已移除 `--committed-head` 部分构建入口；任何未提交/未跟踪源码、未推送 HEAD，或构建过程中出现新改动，都阻止形成可发布产物。不要使用独立索引、部分暂存、临时 checkout 或手动 `git archive` 绕过这一要求。
4. 用完整产物更新 6556，按功能清单逐项审阅，并向用户说明预览包含的内容。本次统一版本包含：站点模型与价格调度、空凡登录/图片协议/VIP 积分价格、Sub2API/New API/空凡余额自动扫描、复用系统邮件管理的低余额提醒、每个绑定一行的紧凑页面、账号实时调度状态，以及采用对方站点分组的 `【站点】模型（分组）` 账号名称。只更新站点绑定所管理的账号，不修改历史独立账号。
5. 用户确认此完整预览并明确同意上线后，才执行生产发布。若审阅后新增改动，重新整合、测试、提交、推送、构建并更新预览，不能上线未经审阅的不同版本。

余额功能细节见 [UPSTREAM_SITE_BALANCE.md](UPSTREAM_SITE_BALANCE.md)，空凡接入见 [KONGFANG.md](KONGFANG.md)。此前仅含空凡适配的预览不能作为本次完整功能的发布依据。

2026-09-21 后续整合新增：多站点总览直接展示余额和新增模型数，支持异常筛选与搜索；上游新增模型按管理员保存已读状态，实际查看后清除未读提示，绑定仍由管理员决定。后续审阅修复同时包含站点整行切换与异步结果不抢选择、列表主页面与站点详情弹窗，以及飞羽公开价格与登录专属分组兼容、无模型广场时通过公开价格及登录接口继续同步（含 supergpt，分组参考价与完整模型采购价分别展示），以及余额统一折算 USD、可填写 1:100/1:10/1:1 倍率、按 USD 阈值发送全中文余额提醒。这些功能与上述余额、空凡、价格调度、紧凑布局及命名修改合入同一个完整 HEAD，详见 [UPSTREAM_SITES.md](UPSTREAM_SITES.md)。

## 日常修改与构建

```bash
cd /opt/sub2api/source
git status
# 检查并整合全部待发布改动，运行完整发布范围相关的测试
cd backend
go test -p 2 -tags=unit ./internal/service ./internal/handler \
  -run 'TestForwardImagesRetryLater400|TestOpenAIGatewayHandlerImages_(RetryLater400SwitchesAccounts|ServerErrorFailsOverAndReturnsClearErrorWhenExhausted)'
cd ..
git add <全部已审阅的待发布文件>
git commit -m '说明本次修改'
git push origin host-production
./ops/build-local.sh
```

`build-local.sh` 从已提交的 `HEAD` 导出源码，在本机用锁定的 pnpm 9.15.9 和 `pnpm-lock.yaml` 构建前端，再以 `-tags embed` 将前端嵌入 Go 二进制。Go 版本由 `backend/go.mod` 指定。构建标记为 `source`，包含完整 commit 和构建时间。

产物固定放在 `/opt/sub2api/releases/<版本>-<提交前12位>/`：`sub2api`、`manifest.json`、`SHA256SUMS`。构建不切换线上进程。已构建的同一提交复用其经过校验的产物。

## 6556 审阅预览：共用 Sub2API 生产数据库

用户于 **2026-09-20** 明确要求：Sub2API 也必须构建并运行独立的预览环境，固定审阅入口为 **`0.0.0.0:6556`**。每次生产发布前，先完成测试、提交、推送和本机源码构建，将待发布版本在此端口运行，供用户审阅；用户确认预览并明确同意上线后，才能执行生产发布。

- 预览直接连接 **Sub2API 自身的生产 PostgreSQL 数据库**，使用原有生产账号和业务数据。启动或更新前，核对数据库连接与 `/opt/sub2api/config.yaml` 及 `sub2api.service` 的实际环境一致；不能误用 image2api 的 `vivid_ai` 数据库。
- 未经用户明确要求，不另建 review/test 数据库，不创建替代预览账号。自动化集成测试使用专用测试库，不向共享生产库写入测试夹具。
- 使用独立预览进程和本机私有配置；生产服务继续运行在 `7654`。启动预览不得覆盖 `/opt/sub2api/config.yaml`、切换 `current` 或重启 `sub2api.service`。预览配置中的凭据不提交 Git，也不打印到终端。
- 检查预览健康状态、页面、原有账号登录及生产数据是否可读；预览中保存账号、模型、价格和设置会直接修改生产数据。
- image2api 的 `6555` 及既有预览拓扑保持不变。Sub2API 使用 `6556`，不得借用或替换 `6555`。
- 预览与发布均不得重建、恢复或删除 PostgreSQL、Redis、业务存储或其数据卷。

### 本机预览进程

- 独立 systemd 临时服务：`sub2api-preview.service`，监听 `0.0.0.0:6556`。
- 私有工作目录：`/opt/sub2api/preview`，其中 `config.yaml` 和 `.installed` 仅服务用户可读；配置不进入 Git。PostgreSQL 和 JWT 配置复用生产，启动前同时核对生产进程的环境变量覆盖。Redis 复用服务器及认证，但使用独立 **DB 14**（首次启动前已确认为空）：Sub2API 启动会清理同库其他进程的并发槽位，预览不能与生产共享 Redis DB。此隔离不改变 PostgreSQL、原有账号和业务数据，也不涉及删除或重建 Redis。预览禁用自动 token 刷新、用量清理、仪表盘聚合、Ops 定时后台和批量图片队列。Ops 使用 `enabled: true`、`disable_background_tasks: true`，保留请求错误记录和错误列表/详情查询；不得再通过 `ops.enabled: false` 整体关闭，否则使用记录中的错误请求接口会返回 `404 OPS_DISABLED`。独立开关强制停用指标采集、预聚合、清理、告警评估和定时报表，优先于共享数据库中的定时任务设置。
- ExecStart 直接指向已提交并推送版本的 `/opt/sub2api/releases/<版本>-<提交>/sub2api`，不修改生产 `current`。新预览替换时只停止/启动 `sub2api-preview`。

完成配置核对、测试、提交和推送后：

```bash
./ops/build-local.sh
release_dir="/opt/sub2api/releases/$(tr -d '\r\n' < backend/cmd/server/VERSION)-$(git rev-parse --short=12 HEAD)"
# 如果旧预览仍在运行，只停止旧预览；生产 sub2api.service 不受影响。
systemctl stop sub2api-preview.service 2>/dev/null || true
systemd-run --unit=sub2api-preview --collect \
  --property=User=sub2api --property=Group=sub2api \
  --property=WorkingDirectory=/opt/sub2api/preview \
  --property=Restart=on-failure \
  --setenv=DATA_DIR=/opt/sub2api/preview \
  --setenv=CONFIG_FILE=/opt/sub2api/preview/config.yaml \
  --setenv=UPSTREAM_SITES_PREVIEW=false \
  --setenv=GATEWAY_SCHEDULING_DISABLE_STICKY_SESSIONS=true \
  "$release_dir/sub2api"
curl -fsS http://127.0.0.1:6556/health
systemctl status sub2api-preview --no-pager
```

2026-09-21 按用户要求，6556 使用 `gateway.scheduling.disable_sticky_sessions: true`（或上述环境变量）全局关闭自动会话粘性，覆盖 Gemini 原生/兼容、Claude/Antigravity 和 OpenAI 兼容调度。已有 Redis 绑定、预取绑定及内容摘要回退均不再决定账号，不写入或续期绑定；同一个调用账号/会话每次重新按当前优先级与可用性调度，单次请求故障仍正常换渠道，下一次请求重新排序，无须清 Redis。并发、会话数量限制与已生成响应/媒体任务的归属校验继续生效。此配置是实例级覆盖，默认关闭覆盖以兼容其他部署；后续生产上线须在用户确认完整预览后同步设置该项，不能只更新二进制而遗漏配置。

站点管理、价格口径及预览托管账号的行为见 [UPSTREAM_SITES.md](UPSTREAM_SITES.md)。用户于 2026-09-21 明确要求通过新分组隔离测试、允许符合条件的账号参与调度，因此预览设置 `UPSTREAM_SITES_PREVIEW=false`，恢复每 5 分钟的模型/价格自动同步；新建托管账号正常启用，原先带待上线标记的账号在同步时解除统一停用。仍执行分组、模型、档位价格、有效期及手动调度开关检查，不得再仅因运行于 6556 而统一停用账号。已核对测试分组 10、11 独立于历史分组，启用时没有 API Key、调用记录或路由引用；这不改变共用生产 PostgreSQL 的约定，也不代表允许发布或重启生产。余额每 5 分钟自动扫描与系统邮件提醒继续运行；它们与价格同步独立，配置和收件人统一复用系统邮件管理，见 [UPSTREAM_SITE_BALANCE.md](UPSTREAM_SITE_BALANCE.md)。

两种新增媒体协议的配置和计费说明分别见 [VIVIDAI.md](VIVIDAI.md) 和 [LONGXIA.md](LONGXIA.md)。验证使用模拟上游，没有实际客户 Key 时不把模拟测试视作真实上游生成验证。

## 2026-09-15：上游 400 换渠道修复

`400` 响应中的泛化错误 `Upstream request failed. Please retry later.` 会触发现有渠道切换流程。只读取响应的 `error.message` 等明确错误字段，接受空类型或 `api_error`、`upstream_error`、`server_error`；明确的内容拒绝、参数错误码或 `param` 字段优先，仍然停止请求。

修复复用现有渠道排除和最大切换次数，不在同一请求中无限重试。HTTP 入口回归测试使用本地桩渠道：修复前仅调用渠道 1，修复后调用渠道 1、2；内容拒绝仍只调用渠道 1。所有测试不访问生产数据库、不向真实上游发起生成。

本次只修改 Sub2API。image2api 的临时修复已经撤回，其原 6555 预览已恢复。

## 2026-09-21：池模式默认冷却修复

生产日志确认：池模式 API Key 的 503 不在默认同账号重试列表内，仍进入账号＋模型瞬时故障计数，连续失败后冷却 10 秒、45 秒；相关分组出现 `model_not_supported=1 model_rate_limited=1 runtime_blocked=2`，新请求直接返回 `503 No available compatible accounts`。其中模型级限流是上游 404 自动产生的 30 分钟停调，日志里的 `model_rate_limited` 不一定代表真实 429。

池模式现在对普通上游瞬时失败只使用单次请求内的重试、换渠道与次数上限，不再生成或执行默认的账号＋模型瞬时冷却。默认模型 404、图片 429、图片能力错误保留失败识别，但不再写跨请求模型冷却；旧版本持久化的这三种默认原因也不再拦截池模式账号，无需修改共享数据库。显式自定义错误码、管理员临时规则、手动调度开关、分组/模型/价格资格、并发和用量限制继续生效。本机相关池账号没有配置显式临时规则，健康熔断和流超时停调均未启用。

回归测试使用模拟上游连续发起四次请求，验证每次仍逐个尝试两个渠道，失败不会传播到下一次请求；同时覆盖旧模型冷却、普通账号冷却、显式规则和内容拒绝。测试不连接生产数据库、不提交真实生成。6556 包含此前完整版本及本次冷却修复，生产须用户审阅确认后发布。上游全部返回错误时仍会在有限次数内返回失败，本修复不保证上游生成成功。

## 2026-09-23：10k 图片模型不支持时换渠道

参分渠道把 `gpt-image-2` 映射为 `gpt-image-medium`，上游返回 HTTP 400、`type=invalid_request_error`、`code=ERR-<10 位十六进制编号>` 和 `unsupported 10k image model: gpt-image-medium`。此前编号被当成明确错误码，且消息匹配缺少此句式，请求在该渠道结束。

现在将这类编号作为错误流水号，继续检查明确的模型不可用错误。该响应进入既有渠道切换流程：本次请求排除当前账号，直接选择下一个符合模型/分组/价格条件的账号，不在原账号重复尝试；沿用既有最大切换次数。明确参数错误、非 model 的 param、内容拒绝和回显 prompt 不触发此例外。未修改渠道模型映射或启停状态，也未增加跨请求冷却。

回归使用本地模拟上游，覆盖渠道 1 返回生产同款 400 后渠道 2 成功、后续渠道仍失败、没有兼容备用渠道，以及参数/内容错误仍终止。未对共享生产数据库写入测试数据，也未调用真实上游生成。6556 沿用完整已提交版本及原生产账号和数据；生产仍需用户审阅确认后发布。

## 生产发布

### v0.2.7 与已有 fork 迁移的兼容

本机数据库在 2026-09-17 已执行 fork 版本的 `238_opencode_go_platform.sql`
及 `239_fork_platform_constraints_superset.sql`。前者记录的 checksum 为
`d310f134e119bd0b01c36e048841d04e1adc04a117c5c516ccdbbc8800742414`，
而 v0.2.7 上游文件为 `6f987e251519bd3759e60da44620a5d777494cceb333b6ce394aa0ea536ef5a2`。
四个实际 CHECK 约束已逐一核对，均包含上游的 OpenCode/MiniMax 平台集合，并保留 Kiro/Adobe 扩展。
迁移执行器沿用现有的精确 checksum 兼容机制，仅接受这两个已核对版本；不重写迁移账本，
不重新执行旧迁移，也不收窄已有平台约束。其余 checksum 仍严格校验。

内嵌前端对 Seedance 的 `/api/v3`、`/v3`、`/v1` 及无版本前缀任务路由全部放行，
避免 `/v3` 和 `/contents/generations/tasks` 被 SPA 回退页面截获。

### 发布命令

先完成本地验证，将待发布版本运行在 **6556** 供用户审阅并提供改动和测试结果，**用户确认预览并明确同意上线后**再执行：

```bash
cd /opt/sub2api/source
./ops/release-local.sh
```

脚本要求 `host-production` 分支、干净工作区，并验证当前提交已存在于 GitHub。它先完成本机源码构建，再切换 `current` 链接、重启 **仅** `sub2api.service`。既有 systemd `ExecStart=/opt/sub2api/sub2api` 不变，该路径在首次切换时成为指向 `current/sub2api` 的链接。

首次切换会保留原二进制到 `releases/legacy-<SHA前12位>/`。健康检查同时核对实际运行的二进制路径和 `http://127.0.0.1:7654/health`；失败自动恢复旧版本。

不要使用后台的一键下载更新、`install.sh` 或下载 release 二进制覆盖本机源码版本。不得重新创建、恢复或删除数据库、Redis 和业务存储。

## 查版本与回滚

```bash
readlink -f /opt/sub2api/current
cat /opt/sub2api/current/manifest.json
/opt/sub2api/sub2api -version
systemctl status sub2api
journalctl -u sub2api -n 80 --no-pager
```

首次源码版本尚未上线时，`current` 尚不存在，`/opt/sub2api/sub2api` 仍是原二进制。发布历史在 `/opt/sub2api/release-history.log`，`previous` 指向上一个可回滚版本。

确认需要回滚后：

```bash
cd /opt/sub2api/source
./ops/rollback-local.sh
```

## 2026-09-22：流量扶持审阅

完整版本新增站点绑定「调度设置 → 流量扶持」：可配置比例和截止时间、按健康状态逐步恢复、共享 5% 恢复试调、Redis 原子首发计数与 24 小时性能历史、账号列表状态及设置跳转。规则与验证见 [UPSTREAM_SITES.md](UPSTREAM_SITES.md)。本次用户指定空凡 `gpt-image-2` 的目标为 20%，其他绑定默认关闭；预览保存使用独立设置项，旧版生产同步不覆盖，生产发布仍需审阅确认。


## 2026-09-24：合入 v0.2.8 与 Grok 上游格式

统一预览包含上游 `v0.2.8`（及 `a3eb7ef30` 版本号同步）、此前站点/余额/调度修改、Grok 分组绑定、视频秒价、Grok 类型使用 OpenAI 媒体端点及对应模型测试。合并冲突保留本机站点核价/请求观察、托管模型别名、400 换渠道和弹窗滚动锁，同时采用上游 Gemini 传输错误统一换号、复合分组图片路由及新增服务，Wire 重新生成。

启动前只读核对共享库：4 个分组仍含旧 `max_reasoning_effort_multiplier`。本机首次执行的 `239_channel_reasoning_effort_multipliers.sql` 保留此旧 JSON 字段，再添加通用倍率映射；不移除线上旧二进制需要的字段。分组定价 JSON 兼容读取旧字段，新保存时向旧字段镜像 `max`，显式清空映射不复活旧值。此迁移是本机维护版本，后续合并不得用上游删除旧字段的版本覆盖已登记 checksum。原已执行迁移和平台扩展兼容不变。上游站点通用推理倍率按最高值保守核价。

验证使用单元测试和浏览器模拟数据，不向共享库写测试夹具，不提交真实媒体生成任务。只更新 `sub2api-preview` 的 6556，生产 `sub2api.service` 和 image2api 的 6555 不变。


本次验证：前端 339 个测试文件、2,544 项测试全部通过，Vue 类型检查通过；后端 service 全量回归及 handler、admin、dto、quotaview、routes、repository、migrations 与 Wire 单元测试通过，新增 Grok 内容下载路径补充回归通过。新迁移及定价读写在 Testcontainers 创建的 `sub2api_test` 独立库验证通过。补充修复用量查询页卸载后遗留的动画计时器，避免异步回调访问已卸载页面。

同日补充修复：站点托管 Grok 账号手动选择上游格式曾被通用凭据保护拦截并返回 500，现允许单独修改格式和原样回传未变更分组/凭据，保留受管密钥与价格策略；真正修改受管字段返回 400 及明确说明。旧版生产同步导致自动识别格式丢失时，使用绑定/同步保存的账号格式缓存，避免测试重新调用 `/v1/videos/generations`。细节见 [UPSTREAM_SITES.md](UPSTREAM_SITES.md)。此次只修复格式持久化和保存流程，不修改采购价或售价；沿用完整 v0.2.8 预览。协议回归使用模拟上游，真实上游结果以预览账号验证记录为准。

同日新增渠道透明背景开关：OpenAI/Grok 账号可声明不支持透明背景，调度时跳过明确要求透明背景的图片请求，覆盖重试、换渠道、粘性账号和调度缓存；已有账号保持原行为，站点同步保留设置。配置与边界见 [IMAGE_BACKGROUND.md](IMAGE_BACKGROUND.md)。统一 6556 预览同时保留上述全部功能，生产尚需审阅确认。
