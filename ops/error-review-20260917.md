# 2026-09-17 图片错误归类修复

生产最近 24 小时存在 267 次 HTTP 400：`type=invalid_request_error`、`code=upstream_error`、`message=Upstream request failed. Please retry later.`，旧精确匹配规则被 type 阻断。现在只在 code 为 upstream_error / server_error、文案精确匹配且没有 param 时接受这一类型；其他参数错误仍终止。

内容拒绝包括明确 content_policy 错误码、image_unsafe / prompt_unsafe / video_unsafe，以及错误 message 内嵌的 poll failed JSON。它们优先于状态码重试规则，不因网关包成 5xx 就轮换或惩罚账号；HTTP / SSE 返回稳定拒绝类型与错误码，模糊的 Please retry later 替换为修改提示词或参考图片的建议，原始上游信息仍进入运维日志。已有 moderation_blocked 等明确错误码继续保留。无法确定原因的普通 451 不自动重试。

原生图片接口另有一次 closed network connection 普通错误没有进入 failover。该接口现在复用既有 handleOpenAIUpstreamTransportError；连接故障进入换渠道，客户端取消仍终止。

验证：

```bash
cd backend
go test -p 2 -tags=unit ./internal/service ./internal/handler \
  -run 'Test.*(Images|RetryLater|ContentPolicy|ClientError|FailoverOpenAIUpstream|HandleOpenAIUpstreamTransportError)' -count=1
```

测试使用本地桩，没有真实生成。HTTP 400 桩验证账号 1 → 2，内容拒绝仅账号 1；HTTP / SSE、嵌套 unsafe、5xx 内容拒绝和取消均有回归覆盖。发布前先提交推送并执行 `ops/build-local.sh`，用户确认后才执行 `ops/release-local.sh`。

image2api 的中文展示与内容错误承接另在 `/opt/image2api` 修改，其 6555 预览保持既有拓扑和生产数据库，不作为 Sub2API 生产调度已更新的证明。
