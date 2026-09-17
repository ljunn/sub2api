# 2026-09-17：model_not_found 的重试与实际请求核对

用户提供的错误为：

```json
{"detail":"{\"error\":{\"message\":\"Model \\\"gpt-image-2.5-flare\\\" is not supported by any configured account in this group\",\"type\":\"model_not_found\"}}"}
```

## 生产事实

北京时间 2026-09-17 22:35:04，同文案请求 `a1845830-2c29-4649-81b3-38f057043405`：

1. 账号 17（supergpt）返回 HTTP 404，错误为 `type=model_not_found`，上游分组没有支持 Flare 的账号。
2. Sub2API 对此账号的该模型写入 30 分钟冷却，并记录 `upstream_failover_switching`，switch_count=1。
3. 切换至账号 18（飞羽），收到 HTTP 503，再记录 switch_count=2。
4. 选择结束：`pool=3, filtered: excluded=2 model_not_supported=1`。

因此这次确实换过账号。当前分组 3 的数据库配置（只读核对）中，账号 10 仅支持 gpt-image-2 / gpt-image-medium；账号 7 支持 Flare 但 schedulable=false，账号 14 同样停用且不支持 Flare。此次未启用停用账号，也未修改模型映射。

## 另外复现并修复的遗漏

- HTTP 400 的模型不可用判断原先仅识别 `error.code` 或少量 message 短语，遗漏 `error.type=model_not_found` 及含模型名称的实际句式。
- 序列化在 `detail` / `message` 内的错误未统一解析。新解析仅遍历错误字段，有递归上限，不检查回显 prompt / request；明确的其他 code、内容拒绝及非 model 参数错误仍终止。
- HTTP 404 的模型错误现在可以直接进入已有换账号流程，不再依赖可选的账号冷却服务及其状态码过滤。
- 模型不可用明确禁止同账号重试，即使池重试配置包含 400 / 404，也直接排除当前账号后选择下一个支持模型的账号。
- Responses 图片 SSE 的明确模型不可用错误，在实际输出尚未写给客户端时允许切换；已经输出图片后仍不切换。

image2api 只新增该类错误的中文展示：`当前没有可用渠道支持此模型，请选择其他模型或联系管理员。` 没有在 image2api 新增渠道调度策略。

## 验证

新增复现测试在修改实现前失败：HTTP 400 的 type-only 错误未触发切换、模型不可用仍允许同账号重试、Responses SSE 404 被判终止。修复后以本地桩确认 400 / 404 / detail 字符串 / detail 对象均调用账号 1 → 2，剩余账号不支持模型时只调用账号 1，内容拒绝和参数错误仍不换账号。

```bash
cd backend
go test -p 2 -tags=unit ./internal/service ./internal/handler \
  -run 'Test.*(ModelUnavailable|ModelNotFound|Images|RetryLater|ContentPolicy|ClientError|FailoverOpenAIUpstream)' -count=1
```

未调用真实上游生成，未写入生产业务数据。6555 的展示函数已通过浏览器加载验证。生产 Sub2API 仍为 `3d9bd430a9d4`，上一轮和本轮源码修复均需用户明确确认后才按源码发布流程上线。
