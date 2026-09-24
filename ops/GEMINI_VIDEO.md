# Gemini 分组的视频兼容接口

Gemini 分组可以通过 `POST /v1/videos`（或 `/v1/videos/generations`）提交上游提供的 OpenAI Videos 任务。模型由 Gemini 原有调度器选择，只有 API key 账号且站点模型明确声明 `openai-video` / `openai-videos` 的上游可用。视频协议保存在站点策略与账号凭据缓存中，旧进程同步站点时不会丢失最后有效的协议声明。

`GET /v1/videos/{id}` 和 `GET /v1/videos/{id}/content` 复用现有任务归属记录，按分组、用户和 API key 验证，始终查询创建任务的原账号。提交后即使停止创建调度，原任务仍可查询；查询失败不换账号创建新任务。

提交阶段不收取成功视频费用；完成查询与下载共用扣费去重。分组按次价格按完成的视频数计费，按秒价格才乘时长。

Gemini 图片仍使用 `generateContent`。上述视频入口是兼容中转协议，不是 Google 原生 `predictLongRunning` / operations API。

image2api 中选择 Gemini 兼容协议，勾选本地 Veo 模型并映射到本分组 `/v1/models` 返回的名称。MiniMax 在 Sub2API 已有文本平台类型；本次 image2api 的 MiniMax 改动是模型归属分类。

验证覆盖：原生 Gemini 调度入口、失败换账号、提交与查询归属、跨 Key 拒绝、价格暂停后查询、按次收费不乘时长、图片接口隔离，以及中转协议元数据缓存。
