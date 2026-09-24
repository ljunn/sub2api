# 马夫 H3 / Wan3 视频接入与同步恢复

马夫的 `powerby-h3` 是上游视频协议标识，可把 `minimax-h3` 绑定到本地 MiniMax 分组。`wan3` 中的 `wan3.0-video` / `wan3.0-video-prime` 可绑定到本地 OpenAI 分组。匹配限定站点类型、上游平台和精确模型，不放开任意跨平台绑定；旧的 `wan3.0` 不是文档中的可提交模型。

| 协议 | 创建 | 上游查询 | 参数 |
| --- | --- | --- | --- |
| H3 优化版 | POST /v1/videos | GET /v1/videos/{id} | JSON duration 整数 4–15，720P，ratio，image_urls / first_frame / last_frame / video_urls / audio_urls |
| Wan3 | POST /v1/videos/generations | GET /v1/videos/tasks/{id} | JSON duration 整数 2–30，480P / 720P / 1080P，ratio，media[] |

对外同时接受 `/v1/videos` 和 `/v1/videos/generations`；查询兼容 `/v1/videos/{id}` 和 `/v1/videos/tasks/{id}`。分组需要媒体权限。任务归属按分组、用户、API Key 验证，查询固定原账号；已有任务不受新建任务价格停调影响。结果由既有内容代理下载，访问公共 CDN 不携带账号密钥或自定义认证头。Wan3 的 30 秒时长在暂存、完成结算和用量记录中保留。

价格以认证分组及个人倍率为准；站点隐藏 `/channels/available` 时，读取 `/api/v1/pricing/channels`，按认证组 ID 匹配公开报价，并将 `per_second` 转为视频每秒计价。H3 优化组只导入 720p 档，Wan3 Prime 分别导入三档。没有普通版报价时保持未知价，不猜测。采购额度和本地售价必须使用一致的 USD 账户计价单位，前端人民币显示标签不额外乘一次汇率。分组利润保护和档位价格门继续生效。

站点手动同步使用独立的两分钟有界上下文，浏览器刷新或断开不会中断已接受的同步，也不会丢失新凭据；后台同步仍响应服务关闭。上游返回 `403 INSUFFICIENT_BALANCE` 时明确提示余额不足，保留有效分组 Key，不当成失效 Key 反复新建，也不保存为“连接成功但零模型”。

来源（2026-09-24）：https://token.secure-skill.com/docs#minimax-h3-video 、https://token.secure-skill.com/docs#wan3-video 、https://token.secure-skill.com/api/v1/pricing/channels 。

自动化验证使用模拟上游，覆盖同步取消恢复、余额错误恢复、公开价格与倍率、分组匹配、视频调度与失败切换、任务归属及跨 Key 拒绝、30 秒计费、CDN 凭据隔离。6556 构建统一包含同批 Gemini 视频兼容功能。真实视频生成需补齐业务售价和绑定后验证；生产仍须审阅确认后发布。
