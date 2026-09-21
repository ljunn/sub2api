# WUZU（ChatGPT2API）站点接入

在站点管理中新增站点，格式选择 **WUZU（ChatGPT2API）**，首页地址填写 `https://img.wuzuapi.com`，不要带 `/profile`、`/api-docs` 或 `/v1`。可使用账号密码或后台登录 Access Token；后台令牌和模型调用 API Key 是两种不同凭据，不支持 Refresh Token。

填写 **1 USD 对应多少实际充值额度**。模型采购价和余额共用这个倍率，不将额度默认当作美元，也不根据套餐名称、公告或人民币标价猜测兑换比例。留空可同步目录，自动采购价保持未知；可以使用现有的手动美元采购价功能。修改倍率后旧自动价格立即失效，重新同步后才参与调度。

## 同步和绑定

- `POST /auth/password-login` 使用 `username/password`，读取 `ok/key`；`GET /api/auth/me` 使用后台 Bearer 令牌，读取 `identity`。账号 ID 是字符串，保存在加密凭据中；已有绑定不能换成另一身份。
- 余额读取 `identity.remaining`，单位为额度。缺失、无效和无限额度不伪造为零或有限美元余额。
- 并发读取 `effective_image_concurrency`，这是账号总并发；沿用 Sub2API 的站点共享并发控制，不把每个独立 Key 当成额外额度。
- 同步先检查 `/api/me/site-api-keys/capability` 的 `allowed`，再读取 `/api/image-models`。只接入已启用、允许 API Key 的图片配置。尚未验证的用户类型限制不推断放行，文本和视频暂不作为本类型的调用能力。
- 每个目录条目使用唯一 `config_key` 作为模型绑定标识，同一个对外 `id` 的多个来源保持独立。可将它绑定到本地 OpenAI 分组，并填写本地模型别名。
- 支持 `quota_cost_mode=tier` 的 1K/2K/4K 价格和 `fixed` 的每张额度价格。缺失档位保留未知，复杂 JSON/逐分辨率计价需要手动采购价。
- 绑定时在 `/api/me/site-api-keys` 创建 `s2site-<binding-id>` 专用 Key，限定 `allowed_model_config_keys`，继承账号并发，默认返回 base64，允许请求指定返回格式。Key 加密保存；常规目录与余额同步不会创建 Key。
- 上游只在创建时返回完整 Key。创建失败或超时后再次绑定会先查询同名 Key；若上游已创建而本地未取得密钥，显示明确错误，不自动重复创建或删除。管理员在上游删除提示中的专用 Key 后可重试。

## 图片协议与采购价保护

调用复用 Sub2API 的 OpenAI Images 转发、计费、限流、失败切换和站点价格门禁。发送前固定 `model` 和 `model_config_key`，避免同名配置之间串用采购价。

- 支持 JSON 图片生成及 multipart 单图/多图编辑；文件内容和顺序保留。模型需明确支持编辑，不提供 JSON 图片 URL、mask 的隐式转换。
- 上游的 `async=true` 不透传，避免把 HTTP 202 回执当成成图成功或与 Sub2API 自己的任务 ID 混用。客户端可使用 Sub2API 现有的图片异步入口，由本地任务等待上游同步结果。
- 每次请求检查 1～9 的图片数量和配置选择器。JSON/multipart 的价格字段重复时拒绝，避免网关与上游选择不同值。
- WUZU 前端公开计价逻辑以最终尺寸长边 `≤1536` 为 1K、`<3000` 为 2K、其余为 4K，和本地图片计费档位不同。价格检查同时考虑 `size_map`、比例、分辨率、自定义输入/输出尺寸、模型尺寸上限，以及本地输入/输出计费档位。
- 上游尺寸约束可能降档，价格不一定单调；无法确定最终尺寸时逐一检查所有可能档位。手动关闭的采购档位继续生效，不用较低档价格放行较高档请求。

## 验证

回归测试使用内存仓库和模拟上游，覆盖登录轮换、字符串身份校验、目录权限、同名配置、余额与额度换算、绑定密钥恢复边界，以及图片实际转发、二进制附件顺序、跨档采购价和未发送上游的拦截。

```bash
cd /opt/sub2api/source/backend
GOMAXPROCS=4 go test -p 2 -tags=unit ./internal/service ./internal/handler \
  -run 'Test(Wuzu|Kongfang|UpstreamSite|Site|NewAPISite|ForwardImagesRetryLater400|OpenAIGatewayHandlerImages_)'
cd ../frontend
pnpm exec vitest run src/views/admin/__tests__/UpstreamSitesView.spec.ts \
  src/api/__tests__/admin.upstreamSites.spec.ts src/components/admin/sites/__tests__ \
  src/i18n/__tests__/localeKeyCompleteness.spec.ts
pnpm exec vue-tsc --noEmit
```

可显式启用 `TestWuzuLiveReadOnly`，通过环境变量 `WUZU_LIVE_READ_ONLY=1`、`WUZU_LIVE_URL`、`WUZU_LIVE_USERNAME`、`WUZU_LIVE_PASSWORD` 验证真实后台登录、目录和余额。该测试不连接本地数据库、不创建调用 Key、不提交生图；凭据不写入测试或日志。真实生成及扣费未验证前，不将模拟转发测试描述成已完成真实出图。

2026-09-21 已用用户提供的账号通过上述只读验证：真实登录、余额和 3 个图片模型配置均可读取，并成功同步账号总并发限制。未创建真实调用 Key，未发起收费生成。

平台文档：<https://img.wuzuapi.com/api-docs/>。管理接口结构：<https://img.wuzuapi.com/openapi.json>。发布继续遵守 `ops/README.md` 的完整版本提交、推送、6556 审阅和生产确认流程。
