# 本机源码部署

固定源码目录：**`/opt/sub2api/source`**。这是独立 Git 仓库，`.git` 和对象库都在此目录，不能改为指向 `/tmp` 的 worktree。

- 本机维护仓库：<https://github.com/ljunn/sub2api>，分支 `host-production`。
- 上游：`upstream` → <https://github.com/Wei-Shaw/sub2api>。
- 初始源码基线：`5de5e2bed035d43591a2e10e51f420ef6a84eb98`，对应迁移前二进制记录的提交。
- `backend/cmd/server/VERSION` 保留线上版本 `0.2.4`。上游 release 工作流会在打包时更新此文件，提交里的旧值是 `0.2.3`，不能直接据此让本机版本号倒退。
- 生产仍由 `sub2api.service` 管理，工作目录 `/opt/sub2api`，端口 **7654**。
- 配置 `/opt/sub2api/config.yaml`、安装标记、现有 PostgreSQL/Redis 和业务数据沿用现有部署，不进入 Git。
- `/opt/image2api` 是独立项目，不要为了修 Sub2API 修改它或切换其 6555 预览。

## 日常修改与构建

```bash
cd /opt/sub2api/source
git status
# 修改源码，运行与改动相关的测试
cd backend
go test -p 2 -tags=unit ./internal/service ./internal/handler \
  -run 'TestForwardImagesRetryLater400|TestOpenAIGatewayHandlerImages_(RetryLater400SwitchesAccounts|ServerErrorFailsOverAndReturnsClearErrorWhenExhausted)'
cd ..
git add <本次修改的文件>
git commit -m '说明本次修改'
git push origin host-production
./ops/build-local.sh
```

`build-local.sh` 从已提交的 `HEAD` 导出源码，在本机用锁定的 pnpm 9.15.9 和 `pnpm-lock.yaml` 构建前端，再以 `-tags embed` 将前端嵌入 Go 二进制。Go 版本由 `backend/go.mod` 指定。构建标记为 `source`，包含完整 commit 和构建时间。

产物固定放在 `/opt/sub2api/releases/<版本>-<提交前12位>/`：`sub2api`、`manifest.json`、`SHA256SUMS`。构建不切换线上进程。已构建的同一提交复用其经过校验的产物。

## 2026-09-15：上游 400 换渠道修复

`400` 响应中的泛化错误 `Upstream request failed. Please retry later.` 会触发现有渠道切换流程。只读取响应的 `error.message` 等明确错误字段，接受空类型或 `api_error`、`upstream_error`、`server_error`；明确的内容拒绝、参数错误码或 `param` 字段优先，仍然停止请求。

修复复用现有渠道排除和最大切换次数，不在同一请求中无限重试。HTTP 入口回归测试使用本地桩渠道：修复前仅调用渠道 1，修复后调用渠道 1、2；内容拒绝仍只调用渠道 1。所有测试不访问生产数据库、不向真实上游发起生成。

本次只修改 Sub2API。image2api 的临时修复已经撤回，其原 6555 预览已恢复。

## 生产发布

先完成本地验证并向用户提供改动和测试结果，**获得明确上线确认后**再执行：

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
