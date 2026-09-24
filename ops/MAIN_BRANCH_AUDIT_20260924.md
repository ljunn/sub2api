# Sub2API 主分支整理（2026-09-24）

按用户要求，本机维护仓库 `ljunn/sub2api` 统一使用 `main`。固定源码目录仍是 `/opt/sub2api/source`，上游仍是 `Wei-Shaw/sub2api` / remote `upstream`。

## 已整合内容

- 整理起点：原 `origin/main` 为 `badfad8b7248b8aac0e6b503a06e392aa31cb294`，完整本机版本 `host-production` 为 `a113fdd267491338d87b50584715e7a4610e0221`。
- `origin/main` 是完整版本的祖先，缺少 380 个提交。通过快进合并纳入全部历史，无冲突、无强制推送、无 reset 或 squash。
- 包含上游 v0.2.8、本机站点管理、余额、采购价、调度、失败切换、Grok/Seedance 媒体接入、Grok 上游格式保存、透明背景能力，以及原有发布/回滚维护流程。
- 整理期间发现同时写入的 New API `tier("base", fixed(...))` 按次价格解析修复，连同价格目录/图片调度回归和文档一起验证、提交到 main，避免形成遗漏的工作区修改。
- 本地 `main` 的跟踪目标由旧的 `upstream/main` 改为 `origin/main`；GitHub 默认分支统一为 `main`。`host-production` 保留迁移前历史，不再作为维护或发布入口。
- `AGENTS.md`、构建/发布脚本、产物 manifest 及 image2api 中的跨仓库维护约定同步改为 main。代码推送或分支迁移不授权生产部署。

## 分支和工作区核查

已获取 origin/upstream 分支，并补全原 shallow clone 的完整历史后检查祖先关系，避免把浅历史边界误判成未合并。原 `fix/group-model-allowlist-schema-repair` 已在 main 历史中，不重复应用。

本地只有 `main`、`host-production` 两个分支，固定源码目录是唯一 worktree；开始整理时工作区干净、没有 stash，reflog 中没有丢在现有引用之外的提交。所有本机维护提交均进入 main。

下面 5 个 origin 分支仍有 main 之外的历史，均来自上游，保留原引用；不把上游未采用的旧开发路线、实验或 CLA 数据当作本机待发布功能合并。

| 保留分支 | main 之外的提交数 | 核查及处理 |
| --- | ---: | --- |
| `cla-signatures` | 665 | CLA 签署记录；是 upstream 同名分支的祖先，没有本机独有提交，不属于应用代码。 |
| `dev` | 204 | 与 upstream 同名分支相同，旧 0.1.x 开发路线；保留历史。 |
| `fix/5394-moderation-fail-closed-scope` | 1 | 与 upstream 同名分支相同，尚未被 upstream/main 采用的风控行为修改；保留待上游审阅状态。 |
| `preview` | 2 | 与 upstream 同名分支相同，插件版预览初始化；保留。 |
| `preview-dev` | 13 | 与 upstream 同名分支相同，插件拆分实验/审计路线；保留。 |

其余 origin 应用分支均已被 main 包含。没有删除其他分支、标签、提交或业务数据。

## 校验与交付约束

- `ops/require-main.sh` 要求干净工作区，以及 HEAD、本地 main、现场 fetch 的 origin/main 完全一致；可接受同一提交的 detached checkout，拒绝旧 host-production 或功能分支。
- 构建前后、实际发布切换前重复检查。远端前进、无法访问远端、本地 main 未推送或有遗漏修改时停止。
- `python3 ops/test-require-main.py` 在临时 Git 仓库验证 12 个场景：正常 main、同提交 detached、旧分支、功能分支、未跟踪/未暂存/已暂存修改、未推送、远端前进、detached 与本地 main 不一致、缺少 main、远端不可访问。
- 新增价格解析改动使用模拟目录和账号进行 service 回归，不向共享生产数据库写测试数据。
- 从完整 main 构建并更新 6556 预览，继续复用现有生产数据库和账号、独立 Redis DB 14。生产服务、current 链接、7654 和 image2api 的 6555 不切换。

生产上线仍须用户审阅 6556 并明确确认后运行 `ops/release-local.sh`。
