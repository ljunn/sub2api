# Sub2API 本机维护约定

- 固定源码仓库为 `/opt/sub2api/source`，部署说明在 `ops/README.md`。
- 不依赖 `/tmp` 的源码副本或 worktree，不随意另建长期维护入口。
- 保留已有未提交修改；仅提交本次任务的文件。
- 生产代码先提交并推送至 `origin/host-production`，再从本机已提交 HEAD 构建。
- 未经用户明确确认不得切换或重启生产版本；先完成本地构建和相关测试，将待发布版本运行在 `0.0.0.0:6556` 供用户审阅，用户确认预览并明确同意上线后才能发布。
- Sub2API 的 `6556` 预览直接复用 Sub2API 自身的生产 PostgreSQL 数据库、原有账号和业务数据。启动或更新前核对数据库连接与 `/opt/sub2api/config.yaml` 及生产服务实际环境一致；不得自行切换为独立 review/test 数据库或创建替代预览账号。自动化集成测试仍使用专用测试数据库，不向共享生产数据库写入测试夹具。
- 预览使用独立进程与本机私有配置，不覆盖生产配置、不切换 `current`、不重启 `sub2api.service`；凭据不提交 Git、不打印到终端。6556 是 Sub2API 的固定审阅入口，不能占用或替换 image2api 的 6555 预览。具体规则见 `ops/README.md`。
- 生产发布只运行本仓库 `ops/release-local.sh`；它保留旧二进制并检查健康状态。
- 配置与数据仍在 `/opt/sub2api`，生产端口为 7654；不得重建或删除数据库、Redis、业务存储。
- Sub2API 与 `/opt/image2api` 是不同项目。用户未要求时，不修改 image2api，也不更换其 6555 预览。
- 不使用后台二进制下载更新或上游安装脚本覆盖源码构建的版本。
- 各个 custom 渠道的账号池、模型路由、失败切换、重试、熔断、限流和健康调度统一由 Sub2API 负责；image2api 的 custom 适配层主要连接 Sub2API、转发请求和处理统一响应，避免在两个项目重复实现调度策略。
- 当前修复：上游返回 HTTP 400 且明确错误为 `Upstream request failed. Please retry later.` 时触发换渠道；内容拒绝、参数错误和带 `param` 的错误仍保持终止请求。
- image2api 的固定源码目录是 `/opt/image2api`，不要为了 custom 调度在该项目重复实现 Sub2API 的渠道策略。
