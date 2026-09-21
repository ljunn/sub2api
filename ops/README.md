# 本机源码部署

固定源码目录：**`/opt/sub2api/source`**。这是独立 Git 仓库，`.git` 和对象库都在此目录，不能改为指向 `/tmp` 的 worktree。

- 本机维护仓库：<https://github.com/ljunn/sub2api>，分支 `host-production`。
- 上游：`upstream` → <https://github.com/Wei-Shaw/sub2api>。
- 初始源码基线：`5de5e2bed035d43591a2e10e51f420ef6a84eb98`，对应迁移前二进制记录的提交。
- `backend/cmd/server/VERSION` 与选定的上游 release 对齐；当前源码基线已合入 `v0.2.7`，本机源码版本为 `0.2.7`。上游 release 工作流会在打包时更新此文件，tag 内可能仍是旧值，合并时须按 release 修正。
- 生产仍由 `sub2api.service` 管理，工作目录 `/opt/sub2api`，端口 **7654**。
- 配置 `/opt/sub2api/config.yaml`、安装标记、现有 PostgreSQL/Redis 和业务数据沿用现有部署，不进入 Git。
- `/opt/image2api` 是独立项目，不要为了修 Sub2API 修改它或切换其 6555 预览。
- 后续各个 custom 渠道的账号池、模型路由、失败切换、重试、熔断、限流和健康调度统一放在 Sub2API；image2api 的 custom 适配层主要负责连接 Sub2API、转发请求和处理统一响应。

## 完整版本统一提交与发布

用户于 **2026-09-21** 明确要求：**所有待发布改动一起提交、推送、打包，不能只发布其中一部分。** 本约定同时适用于 6556 审阅预览和生产发布。

1. 在 `/opt/sub2api/source` 检查 `git status --short`、已暂存和未暂存差异，以及未跟踪源码；把全部待发布功能及关联前端、后端、配置、测试、文档纳入同一个完整版本。保留已有工作，不能只提交最近新增的功能；凭据、私有运行配置和临时产物不提交。
2. 对完整工作区完成测试，提交并推送 `host-production`，确认工作区干净。可以保留已有提交历史，但最后的 `HEAD` 必须包含所有待发布功能。
3. 运行 `./ops/build-local.sh`。脚本已移除 `--committed-head` 部分构建入口；任何未提交/未跟踪源码、未推送 HEAD，或构建过程中出现新改动，都阻止形成可发布产物。不要使用独立索引、部分暂存、临时 checkout 或手动 `git archive` 绕过这一要求。
4. 用完整产物更新 6556，按功能清单逐项审阅，并向用户说明预览包含的内容。本次统一版本包含：站点模型与价格调度、空凡登录/图片协议/VIP 积分价格、Sub2API/New API/空凡余额查询、低余额提醒设置和邮件通知机制。
5. 用户确认此完整预览并明确同意上线后，才执行生产发布。若审阅后新增改动，重新整合、测试、提交、推送、构建并更新预览，不能上线未经审阅的不同版本。

余额功能细节见 [UPSTREAM_SITE_BALANCE.md](UPSTREAM_SITE_BALANCE.md)，空凡接入见 [KONGFANG.md](KONGFANG.md)。此前仅含空凡适配的预览不能作为本次完整功能的发布依据。

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
- 私有工作目录：`/opt/sub2api/preview`，其中 `config.yaml` 和 `.installed` 仅服务用户可读；配置不进入 Git。PostgreSQL 和 JWT 配置复用生产，启动前同时核对生产进程的环境变量覆盖。Redis 复用服务器及认证，但使用独立 **DB 14**（首次启动前已确认为空）：Sub2API 启动会清理同库其他进程的并发槽位，预览不能与生产共享 Redis DB。此隔离不改变 PostgreSQL、原有账号和业务数据，也不涉及删除或重建 Redis。预览禁用自动 token 刷新、用量清理、仪表盘聚合、Ops 后台和批量图片队列。
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
  --setenv=UPSTREAM_SITES_PREVIEW=true \
  "$release_dir/sub2api"
curl -fsS http://127.0.0.1:6556/health
systemctl status sub2api-preview --no-pager
```

站点管理、价格口径及预览托管账号的行为见 [UPSTREAM_SITES.md](UPSTREAM_SITES.md)。预览必须设置 `UPSTREAM_SITES_PREVIEW=true`，禁用重复的价格同步任务，并暂停新建托管账号直到确认发布。

两种新增媒体协议的配置和计费说明分别见 [VIVIDAI.md](VIVIDAI.md) 和 [LONGXIA.md](LONGXIA.md)。验证使用模拟上游，没有实际客户 Key 时不把模拟测试视作真实上游生成验证。

## 2026-09-15：上游 400 换渠道修复

`400` 响应中的泛化错误 `Upstream request failed. Please retry later.` 会触发现有渠道切换流程。只读取响应的 `error.message` 等明确错误字段，接受空类型或 `api_error`、`upstream_error`、`server_error`；明确的内容拒绝、参数错误码或 `param` 字段优先，仍然停止请求。

修复复用现有渠道排除和最大切换次数，不在同一请求中无限重试。HTTP 入口回归测试使用本地桩渠道：修复前仅调用渠道 1，修复后调用渠道 1、2；内容拒绝仍只调用渠道 1。所有测试不访问生产数据库、不向真实上游发起生成。

本次只修改 Sub2API。image2api 的临时修复已经撤回，其原 6555 预览已恢复。

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
