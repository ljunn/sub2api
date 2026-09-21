# 空凡站点适配

站点管理新增「空凡」，首页地址填写 `https://st.kongfangai.com`，不包含 `/user/balance`。
使用后台用户名/邮箱及密码，或后台登录 Access Token（不是模型 API Key）。空凡 JWT 来自 `/api/v1/admin/auth/login` 的 `data.token`；没有已核验的 Refresh Token 接口，密码模式在 JWT 过期后重新登录。

## 目录与密钥

同步读取 `/api/v1/user/profile`、`/api/v1/user/pricing`、`/api/v1/user/models`，并用现有启用的 API Key 分别读取 `/v1/models` 和 `/v1beta/models`。两个协议分别显示为 OpenAI 图片、Gemini 图片；绑定要求本地平台一致。只接入账号启用且已知为图片的模型，不从文档里的视频名单推断可用能力。未知图片模型显示不可核价。

密钥列表 `/api/v1/user/api-keys` 返回的 `key_raw` 只在服务端使用。每个绑定以 `s2site-<绑定 UUID>` 为名字创建独立 Key，创建超时后查找同名 Key 恢复，不重复创建；已有同名但停用/不可读的 Key 会报告错误。上游没有已核验的 Key 模型限制，模型限制由本地绑定执行。同步不会创建新 Key；上游至少需要一个启用的可读 Key。

## 积分和价格

采购美元价 = 上游积分标价 × 账号 VIP 折扣 × 每积分美元成本。

「每积分美元成本」由管理员填写：实际充值人民币金额 ÷ 到账积分 ÷ 美元兑人民币汇率。它表示积分购入成本，不包含 VIP 折扣；折扣从个人资料读取，避免重复折算。不猜测汇率或充值优惠，0/留空仍显示模型目录，但不能通过价格保护。修改换算成本后立即废弃旧价格，重新同步后才恢复。VIP 状态、折扣或档位价格缺失时不能按免费处理。

每个模型有 1K、2K、4K 三个采购价，继续复用站点价格过期检查、分组售价、利润保护和逐次发送检查。空凡的最长边阈值（1600/2800）与本地像素计费档位不同；采购价同时受采购档位与本地请求计费档位的售价上限约束。另一个采购档位的启停开关不会自动关闭当前档位。

## 站点余额

统一版本同时包含余额查询和低余额提醒设置。空凡个人余额显示为原始「积分」，不乘 VIP 折扣或美元成本；无需先配置积分成本即可查询。提醒、刷新周期和错误处理详见 [UPSTREAM_SITE_BALANCE.md](UPSTREAM_SITE_BALANCE.md)。

## 图片请求

- OpenAI：`POST /v1/images/generations`，可使用 GPT Image 和目录发现的 Nano Banana 图片别名。明确的 `quality` / `resolution` 采用 1k、2k、4k；缺省时按空凡尺寸规则判断，完全缺省为 2K。发送前写入明确的 `quality`，避免上游再次推测档位。
- Gemini：原生 `generateContent` / `streamGenerateContent`，支持 flat `generationConfig.resolution` 和标准 `generationConfig.imageConfig.imageSize`；发送时补充空凡的 flat `resolution`、`aspectRatio`，本地图片计费也采用该分辨率。
- 互相矛盾的分辨率参数，以及站点文档解释矛盾的 `quality=high`，不参与调度；请使用明确的 1k、2k、4k。
- 尚未核验 Images Edits、文本 Chat、Responses 和视频的完整计费契约，本适配在实际发送前拒绝这些端点。图生图使用 Gemini 原生参考图片，或 Images Generations 的上游 `image_urls` 扩展。已有其他站点不受此限制。

测试使用本地 HTTP 上游桩与内存仓库，覆盖登录过期恢复、VIP 折扣、协议目录、密钥超时恢复、未知价格、成本变更失效、档位差异、请求重试、Gemini 分辨率计费，以及前端保存与平台过滤。测试不访问生产数据库，不发起真实生成。

构建、6556 预览和确认后发布遵守 `ops/README.md`。用户已授权通过分组隔离测试，6556 托管账号正常按价格条件参与调度。并发优先读取个人资料中的 `effective_max_concurrency`，缺失时读取 `max_concurrency`；未公开时默认 1000，后续同步更新已有托管账号。
