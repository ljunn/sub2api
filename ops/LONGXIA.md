# LongXia 视频上游

本适配依据用户提供的 api8.longxiaai.store 客户接口合同实现。它使用现有 OpenAI API Key 账号和 Seedance（Ark）任务入口，账号池、路由、所有权、并发和计费去重仍由 Sub2API 管理。

## 账号与价格

1. 新增或编辑 OpenAI / API Key 账号，启用 **LongXia 视频协议**。同一账号不能同时启用 VividAI。
2. 地址填写 `https://api8.longxiaai.store`（也接受 `/v1` 结尾），填写客户 API Key。
3. 配置公开别名到完整 LongXia 模型 ID 的模型映射，将账号绑定到客户端 API Key 所属的分组。分组需开启“允许图片生成”：当前视频也使用此媒体权限开关，关闭时返回 403 `permission_error`。
4. 在渠道模型定价或分组模型覆盖价格中，为调用模型配置 **按次** 或 **视频按秒**。未配置合适的模型价格时，提交会被本地拒绝，避免默认套用 Grok 价格或计为免费。

按次价格乘生成数量（固定 1），视频按秒价格乘提交时长。两者是 Sub2API 管理员配置的销售价格，与上游是否选择 `-PerSecond` SKU 独立。首次查询确认完成后计费；失败或取消不计费，反复查询共用持久化去重键。25 秒和 1440p 元数据不会被 Grok 的 15 秒/分辨率默认值覆盖。

账号的“测试连接”仅 GET `/v1/models`，不会提交收费生成。模型可见不保证容量或余额可用。

站点管理可按 **New API** 类型登录 LongXia。目录根据公开完整 SKU 和 `openai-video` 端点识别视频协议：`-PerSecond` 采购价按美元/秒展示，其余已知型号仍按次；不会根据 New API 的 `quota_type=1` 将按秒型号误标为按次。托管绑定自动开启 LongXia 协议，固定分辨率作为价格档位。同一分组可以将 480p、720p 两个 SKU 分别绑定成两个账号，共用一个本地别名；请求指定 `resolution` 时只选择匹配账号，省略时使用所选 SKU 的固定分辨率。视频价格与本地显式按秒价格比较，未知 SKU 或不兼容的价格单位禁止调度。售价由管理员设置，接入不会自动填写。

已受理任务查询固定使用原账号，可在采购价格过期或创建档位暂停后继续读取；仍校验任务所有权、账号状态和分组归属。

## 客户端调用

image2api 的 custom 使用 `seedance` 协议连接 Sub2API，参考模式选通用参考素材。客户端调用：

```http
POST /api/v3/contents/generations/tasks
Authorization: Bearer <SUB2API_KEY>
Content-Type: application/json

{
  "model": "your-video-alias",
  "content": [
    {"type": "text", "text": "Animate @image1"},
    {"type": "image_url", "image_url": {"url": "https://example.com/reference.png"}, "role": "reference_image"}
  ],
  "duration": 8,
  "ratio": "16:9",
  "resolution": "720p"
}
```

响应包含 `id: "longxia:task_..."`，必须原样保存；每 30 秒 GET `/api/v3/contents/generations/tasks/{id}`。状态统一为 `queued/running/succeeded/failed/cancelled`，成功时读取 `content.video_url`，及时下载保存（上游 URL 有效期 24 小时）。任务只能由创建它的用户、Key、分组查询，并固定使用创建账号。

## 参数边界

- `ratio` 转换为上游 `size`；默认 `9:16`，`duration` 默认 8 秒。素材从 Ark `content` 转换成 `assets`，HTTPS URL 原样传递，data URL 去除前缀成为原始 Base64。
- 提示词必须包含每个素材的连续编号 `@image1`、`@audio1`、`@video1` 等；不会自动改写提示词。音频只接受 MP3；媒体深层格式、实际时长及远程素材体积由上游验证。
- `resolution`、`generate_audio` 只用于核对所选 SKU 固定能力，匹配后不发送给上游。首尾帧角色及其余无法表达的参数明确报错。
- 支持合同列出的 Seedance 2.0 / 2.5、H3、Omni 共 11 个 SKU，不将“库存不足”硬编码为永久禁用。是否实际可用由上游决定。
- 合同多处写“两个”型号支持参考视频，但模型表只明确列出一个 `720p-reference-video` SKU；目前仅该明确型号开放视频素材，最长 15 秒。没有凭不完整能力 JSON 猜测第二个型号。
- 未公开取消接口，因此 DELETE 返回 405。仅明确的鉴权/限流拒绝、且未返回任务回执时，允许现有调度器换账号；400 参数错误、5xx、网络中断或不完整回执不自动重新提交，避免重复扣费。已受理任务失败也不会重新创建。

## 验证与发布

单元测试使用模拟上游及内存缓存，覆盖字段转换、状态、非法参数、错误/重试边界、价格模式、25 秒/1440p 和重复轮询计费，不访问生产数据或真实生成接口。真实容量、账号余额和成品交付需使用实际客户 Key 另行验证。

发布继续遵守 `ops/README.md`：测试、提交、推送，从已提交 HEAD 构建，在 6556 复用 Sub2API 生产数据库预览，用户明确确认后才发布生产。
