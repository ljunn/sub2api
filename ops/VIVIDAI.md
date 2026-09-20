# VividAI 上游协议

在 OpenAI / API Key 账号中启用 **VividAI 上游协议**，填写 `https://vividai.run`（也可带 `/v1`）、客户 Key 和模型映射。账号支持图片及 Seedance（Ark）视频任务入口，不参与聊天/Responses/Embeddings 调度；LongXia 与 VividAI 不能同时启用。

账号必须绑定客户端 API Key 所属的分组，并为该分组开启“允许图片生成”：当前视频任务也使用这个媒体权限开关，关闭时返回 403 `permission_error`。仅“测试连接”成功不代表分组权限、绑定和模型调度已经配置完成。

## 图片

对外沿用 `/v1/images/generations` 和 `/v1/images/edits`。编辑接口支持原有 multipart 文件，以及 JSON `images: [{"image_url":"https://..."}]` 或 data URL。适配器按实际文件格式申请 `/v1/uploads`，使用签名 URL PUT 上传，再将返回的 key 放入 `/v1/generate` 的 `refs`。签名上传、参考下载和成品下载均不携带上游 API Key。

`size` 的像素尺寸会换算到 `quality=1K/2K/4K` 和最简宽高比；也支持直接使用 1K/2K/4K。默认 1K、1:1；OpenAI `quality=high/medium/low` 表示渲染强度，不会覆盖按 size 选出的分辨率。只支持一张、非流式图片，无 mask、透明背景或输出格式控制。URL 和 b64_json 两种结果格式均可使用，缺省返回 Base64。

图片计费沿用 Sub2API 的图片模型价格和分辨率档位。配置价格后再向用户开放模型；上游积分不冒充模型 token 用量。

## 视频

image2api custom 使用 `seedance` 协议连接 Sub2API，对外 POST `/api/v3/contents/generations/tasks`，例如：

```json
{
  "model": "your-vivid-video-alias",
  "content": [{"type": "text", "text": "A cinematic river at sunrise"}],
  "resolution": "720p",
  "duration": 8
}
```

`resolution` 对应上游必填 `quality`，必须使用该模型支持的档位；范围时长型号必须传 `duration`。视频 `ratio` 按上游合同忽略，首尾帧角色和 `generate_audio` 等不能表达的参数报错。参考图、视频和音频通过 content 的 image_url/video_url/audio_url 上传，支持通用参考角色。

创建时返回 `id: "vividai:<jobId>"`。客户端保存完整 ID，GET 同一任务入口 `/{id}`，成功读取 `content.video_url`。创建只读取任务回执；轮询用同一个上游 jobId 续等，不创建第二个任务。DELETE 返回 405。任务所有权与原生 Ark 一样绑定用户、Key、分组和创建账号。

视频沿用当前 Seedance 完成后计费入口，采用**明确标记的积分等价单位**：1 个上游积分 = 1000 个计费单位。响应 `billing.unit=credit_equivalent`、`billing.measured_tokens=false` 表示这些不是实测 token。配置渠道/分组模型的输出单位价格时，若希望每个积分收取 P 美元，则每百万等价单位的输出价设为 `1000 × P` 美元；也可配置固定按次价格。不要给该视频映射套用原生 Ark 的 token 价格或 Grok 的秒价。首次成功查询计费，重复轮询共用持久化去重键，失败不计费。

## 任务与错误

- NDJSON 心跳空行忽略，读取到 jobId 后，图片等待断流会用仅包含该 jobId 的请求续等；不重建已受理任务。
- 明确返回 `4011/4002/4293` 且无任务回执的非 5xx 拒绝，才允许现有调度器换账号。业务拒绝 4001、服务异常 5001、网络断开及已受理后失败不会自动重建。
- 4001 且明确提示“该渠道暂无可用账号”、没有任务回执时，对外返回 503 `upstream_capacity_unavailable`；保留不自动重建任务的行为。参数拒绝仍返回 400。
- 成品 URL 上游仅保留 30 分钟，应及时下载保存。
- 账号连接测试仅查询 `/v1/balance`，不调用收费生成。

测试使用模拟上游，覆盖素材上传、NDJSON 续等、成品下载、错误/重试边界和视频计费字段。真实账号余额、容量及成品交付需要实际客户 Key 验证。提交、推送后使用 `ops/build-local.sh` 构建，在 6556 预览；获得用户明确确认后才能生产发布。
