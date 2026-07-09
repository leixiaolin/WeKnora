# 微信服务号接入方案（WeKnora）

## 分析结论

WeKnora 已有统一 IM 通道抽象（`internal/im/`）和多平台适配器，微信服务号应作为新的 IM 平台 `wechat_mp` 接入，而不是复用现有 `wechat` 平台。

原因：

- `wechat` 当前代表微信个人号 iLink Bot 长轮询适配器，凭证、收发协议、运行模式都不同。
- 微信服务号使用公众号服务器配置回调，接收 XML 消息，回复通常需要走客服消息 API。
- WeKnora 的 `IMCallback` 已具备“同步 ack + 异步处理消息”的结构，适合规避微信回调 5 秒超时限制。
- `im.Adapter`、`AdapterFactory`、`IMChannel.credentials` JSONB 已足够承载 MVP，无需新增数据库表或迁移。

推荐 MVP：

- 平台标识：`wechat_mp`
- 账号类型：微信公众平台测试号或认证服务号
- 回调模式：明文模式，`signature + timestamp + nonce` 校验
- 身份体系：不打通 WeKnora 用户体系，`openid` 即 IM 用户身份
- 输出方式：`IMCallback` 立即返回空 body，后台异步调用客服消息 API 推送文本
- 内容格式：Markdown 降级为微信纯文本，不做图片、文件、语音输入处理
- UI 配置：第一阶段可仅支持 API/数据库创建渠道；若要前端可配置，需要同步 IM 渠道面板

## 当前代码差距

目标文档原草案方向正确，但与当前代码有几处必须修正或补充：

| 差距 | 当前代码位置 | 需要动作 |
|---|---|---|
| 平台常量缺失 | `internal/im/adapter.go` | 新增 `PlatformWeChatMP Platform = "wechat_mp"` |
| 平台白名单缺失 | `internal/handler/im.go` 的 `validIMPlatforms` | 加入 `wechat_mp`，并更新错误提示 |
| 默认接入模式不适配 | `internal/handler/im.go` 的 `CreateIMChannel` | `wechat_mp` 默认 `mode=webhook`、`output_mode=full` |
| 回调 ack 不符合公众号语义 | `internal/handler/im.go` 的 `IMCallback` | `wechat_mp` 的 nil message 和正常 message 都返回 `200` 空 body |
| 工厂未注册 | `internal/container/container.go` | import 并注册 `wechat_mp.NewFactory()` |
| bot 身份计算位置写错 | `internal/im/types.go` | 在 `computeBotIdentity` 加 `wechat_mp:<app_id>`，不是 `service.go` |
| 去重 key 可能跨平台冲突 | `internal/im/service.go` 的 `isDuplicate` 调用 | 对 `wechat_mp` 建议使用 `channelID + ":" + msg.MessageID` 作为 dedup key，或全局改成带 channel 前缀 |
| 前端不可选平台 | `frontend/` IM 渠道配置组件 | 若要求后台可视化创建，需新增平台选项和凭证表单 |
| IM 开发文档未覆盖 | `docs/IM集成开发文档.md` | MVP 合入后补充微信服务号接入章节 |

## 设计目标

### 本轮 MVP 范围

MVP 只解决“用户在服务号发文本，WeKnora 异步回复纯文本答案”的闭环：

1. 公众平台 GET 接入校验成功。
2. POST 文本消息签名校验成功。
3. XML 文本消息解析为 `im.IncomingMessage`。
4. `IMCallback` 在 5 秒内返回空 body。
5. 后台复用现有 IM Service、QA 队列、限流、会话和 Agent 流程。
6. 最终答案通过客服消息 API 发给 `openid`。
7. 重试消息 5 分钟内去重。

### 暂不实现

| 能力 | 暂不实现原因 | 后续扩展方式 |
|---|---|---|
| `StreamSender` | 微信客服消息不支持编辑同一条消息来模拟流式；频繁发送分片也容易触发限流 | 保持 `output_mode=full` |
| `FileDownloader` | 图片、语音、文件需要临时素材下载、格式识别和知识库入库策略 | 第二阶段复用 WeCom 下载与 SSRF allowlist 模式 |
| 加密模式 | MVP 使用明文模式，降低实现与调试复杂度 | 后续增加 `encoding_aes_key` 和 AES 解密 |
| 菜单、模板消息、订阅通知 | 与问答闭环无直接关系 | 单独作为运营能力接入 |
| 多客服转接 | 不属于 WeKnora Agent 问答主链路 | 如需人工兜底再设计 |

## 核心链路

### URL 验证

```
微信服务器 --GET /api/v1/im/callback/{channel_id}?signature=...&timestamp=...&nonce=...&echostr=...
  -> IMCallback
  -> adapter.HandleURLVerification
  -> 校验 signature
  -> 200 text/plain echostr
```

`HandleURLVerification` 不能只回显 `echostr`，必须先校验签名；否则任何人都可以探测和伪造接入校验。

### 文本消息

```
微信服务器 --POST XML--> /api/v1/im/callback/{channel_id}
  -> VerifyCallback 校验 signature
  -> ParseCallback 解析 XML text
  -> IMCallback 立即 200 空 body
  -> goroutine HandleMessage
  -> QA 队列 / Agent / RAG
  -> adapter.SendReply
  -> 客服消息 API 推送文本给 openid
```

`IMCallback` 对 `wechat_mp` 返回空 body 的位置有两个：

- `msg == nil`：关注、取关、图片等 MVP 未处理事件。
- `msg != nil`：文本消息已入后台异步处理。

不要返回 `{"success": true}`。微信会把非 XML/非空响应视为被动回复内容，可能把 JSON 字符串直接发给用户或判为异常响应。

## 新增文件

| 文件 | 职责 |
|---|---|
| `internal/im/wechat_mp/adapter.go` | 实现 `im.Adapter`：签名校验、URL 验证、XML 解析、发送回复 |
| `internal/im/wechat_mp/client.go` | 公众号 HTTP 客户端：access token 缓存、客服消息发送、错误码处理 |
| `internal/im/wechat_mp/factory.go` | `NewFactory() im.AdapterFactory`，从 `IMChannel.credentials` 构造 Adapter |
| `internal/im/wechat_mp/types.go` | XML incoming 结构体、客服消息 JSON payload、微信 API 响应结构 |
| `internal/im/wechat_mp/markdown.go` | Markdown 到微信纯文本降级、长文本分片 |
| `internal/im/wechat_mp/sign.go` | `token/timestamp/nonce` SHA1 签名计算与常数时间比较 |
| `internal/im/wechat_mp/adapter_test.go` | 适配器、签名、XML、Markdown、分片和 client 单测 |

## 修改现有文件

### `internal/im/adapter.go`

新增平台常量：

```go
PlatformWeChatMP Platform = "wechat_mp"
```

不要复用 `PlatformWeChat`，该常量已被个人号适配器使用。

### `internal/handler/im.go`

需要三类改动。

第一，平台白名单加入 `wechat_mp`：

```go
var validIMPlatforms = map[string]bool{
    "wecom": true, "feishu": true, "slack": true, "telegram": true,
    "dingtalk": true, "mattermost": true, "wechat": true,
    "wechat_mp": true, "qqbot": true,
}
```

第二，创建渠道时默认 webhook/full：

```go
if req.Platform == "wechat" {
    channel.Mode = "longpoll"
    channel.OutputMode = "full"
} else if req.Platform == "wechat_mp" {
    channel.Mode = "webhook"
    channel.OutputMode = "full"
} else {
    // existing defaults
}
```

第三，`IMCallback` 对 `wechat_mp` 空 body ack：

```go
if msg == nil {
    if channel.Platform == "wechat_mp" {
        c.String(http.StatusOK, "")
        return
    }
    c.JSON(http.StatusOK, gin.H{"success": true})
    return
}

if channel.Platform == "wechat_mp" {
    c.String(http.StatusOK, "")
} else {
    c.JSON(http.StatusOK, gin.H{"success": true})
}
```

### `internal/container/container.go`

新增 import：

```go
wechatmp "github.com/Tencent/WeKnora/internal/im/wechat_mp"
```

在 IM adapter factory 注册区新增：

```go
imService.RegisterAdapterFactory("wechat_mp", wechatmp.NewFactory())
```

### `internal/im/types.go`

`computeBotIdentity` 新增：

```go
case "wechat_mp":
    if appID := str("app_id"); appID != "" {
        return "wechat_mp:" + appID
    }
```

这样同一个服务号 AppID 不会被重复绑定到多个 IM 渠道。测试号与正式服务号的 AppID 不同，可并存。

### `internal/im/service.go`

当前 `isDuplicate(ctx, messageID)` 使用原始 `MessageID` 构造 Redis/local key。微信 `MsgId` 理论上全局性较强，但不应该把跨平台、跨 channel 不冲突建立在外部平台假设上。

建议在 `HandleMessage` 中改为：

```go
dedupID := channelID + ":" + msg.MessageID
if s.isDuplicate(ctx, dedupID) {
    ...
}
```

该改动会影响所有 IM 平台，但行为更正确：同一个平台消息在同一 channel 内仍去重，不同 channel 不会误伤。如果只想最小改动，也可在 `wechat_mp.ParseCallback` 里把 `MessageID` 设为 `"wechat_mp:" + msg.MsgID`，但这会把去重策略分散到平台适配器，不推荐。

### 前端配置（可选但推荐）

如果本期要求用户能在后台配置服务号，需要同步：

- IM 渠道平台列表增加“微信服务号”。
- 凭证字段增加 `app_id`、`app_secret`、`token`、`api_base_url`。
- 选择 `wechat_mp` 时禁用 WebSocket，固定 `webhook/full` 或隐藏无效选项。
- 渠道卡片显示回调 URL：`/api/v1/im/callback/{channel_id}`。

若 MVP 只通过 API 或 SQL 创建渠道，可把前端列为第二阶段，但文档必须明确。

## Adapter 实现要点

### 凭证结构

`im_channels.credentials` JSONB：

```json
{
  "app_id": "wx...",
  "app_secret": "...",
  "token": "开发者自填 Token，用于 SHA1 签名校验",
  "api_base_url": "https://api.weixin.qq.com"
}
```

字段约束：

- `app_id` 必填。
- `app_secret` 必填。
- `token` 必填。
- `api_base_url` 可选，默认 `https://api.weixin.qq.com`。
- `api_base_url` 只允许 `https`；如支持私有代理，必须走已有 SSRF 校验或明确 allowlist。
- 日志禁止输出 `app_secret`、`access_token` 和完整请求 URL query。

### Factory

`factory.go` 负责解析 credentials 并做启动前校验：

- JSON 解析失败返回可诊断错误。
- 缺 `app_id/app_secret/token` 直接返回错误。
- `api_base_url` trim 末尾 `/`。
- 构造 `Adapter`，不启动任何长连接。
- 返回 `nil` cancel 即可，或返回 no-op cancel。

### 签名校验

明文模式签名规则：

1. 取 `token`、`timestamp`、`nonce` 三个字符串。
2. 字典序排序。
3. 拼接后做 SHA1。
4. hex 小写后与 query `signature` 做 `hmac.Equal` 常数时间比较。

防御要求：

- 缺任意字段返回错误。
- `timestamp` 建议限制在当前时间 ±5 分钟，避免重放。
- GET URL 验证和 POST 消息都必须验签。

### XML 解析

微信 POST body 是 XML。`VerifyCallback` 和 `ParseCallback` 都可能读取 body，读完必须塞回：

```go
bodyBytes, err := io.ReadAll(c.Request.Body)
if err != nil {
    return nil, err
}
c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
```

文本消息映射：

| 微信 XML 字段 | `im.IncomingMessage` 字段 |
|---|---|
| `FromUserName` | `UserID`、`UserName` |
| `ToUserName` | 可放入 `Extra["to_user_name"]` |
| `MsgType=text` | `MessageTypeText` |
| `Content` | `Content` |
| `MsgId` | `MessageID` |
| 无群聊概念 | `ChatTypeDirect`，`ChatID=""` |

事件处理：

- `event=subscribe`：MVP 返回 `nil, nil`，只 ack，不进入 QA。
- `event=unsubscribe`：返回 `nil, nil`。
- `image/voice/video/location/link`：MVP 返回 `nil, nil`，后续第二阶段处理。
- 未知 `MsgType`：记录 info 日志后返回 `nil, nil`。

### 客服消息发送

`SendReply(ctx, incoming, reply)` 实际走客服消息 API：

1. `cleanIMContent` 已在 Service 层清理部分 IM 内容；适配器仍需做微信纯文本降级。
2. `SimplifyForWeChat(reply.Content)` 转纯文本。
3. `SplitForWeChat(text, 550)` 分片。
4. 逐片 `POST /cgi-bin/message/custom/send?access_token=...`。
5. 每片之间 sleep 200ms，降低触发频率限制的概率。

文本 payload：

```json
{
  "touser": "OPENID",
  "msgtype": "text",
  "text": {
    "content": "回复文本"
  }
}
```

错误处理：

| errcode | 处理 |
|---|---|
| `0` | 成功 |
| `45015` | 用户不在客服消息窗口内，warn 日志，不重试 |
| `45009` | 接口调用达到限制，warn 日志，不立即重试；避免放大限流 |
| `40001` / `42001` | access token 无效或过期，清空 token cache 后重试一次 |
| 其他非 0 | 返回错误，让 Service 记录发送失败 |

## Access token 缓存

实现方式参考 `internal/im/wecom/webhook_adapter.go`：

- Adapter 内维护 `tokenMu sync.Mutex`、`tokenCache string`、`tokenExpAt time.Time`。
- 读取缓存前先判断 `tokenCache != "" && time.Now().Before(tokenExpAt)`。
- 过期时请求：

```text
GET /cgi-bin/token?grant_type=client_credential&appid={app_id}&secret={app_secret}
```

- 成功后缓存 `expires_in - 5min`。
- token 请求 URL 不要原样打日志，避免 secret 泄露。
- 多实例各自缓存可接受；如果未来实例数较多，再引入 Redis 分布式 token 缓存。

## Markdown 降级规则

微信客服文本不支持 Markdown。降级目标是“可读、稳定、不触发格式异常”。

| Markdown | 微信纯文本 |
|---|---|
| ```` ```lang ... ``` ```` | 保留代码内容，每行缩进 4 空格；超过 20 行截断 |
| 表格 | 移除对齐分隔行，按 `列1 | 列2` 输出 |
| `> quote` | 行首替换为 `｜` |
| `# / ## / ### 标题` | `【标题】` |
| `[text](url)` | `text` |
| `![alt](url)` | `[图片] alt` 或 `[图片]` |
| `**x**` / `*x*` / `~~x~~` | 去掉标记符 |
| `<think>...</think>` | 不应到达适配器；若出现则丢弃 |
| `<kb .../>` / `<web .../>` | 不应到达适配器；若出现则丢弃 |

分片规则：

- 按 rune 计数，不按 byte。
- 默认每片最多 550 rune。
- 优先按空行、换行、句号/问号/感叹号切分。
- 单段超过上限时硬切。
- 空白分片丢弃。

## 安全与合规

- 回调必须验签，不能只依赖隐藏 URL。
- `timestamp` 必须有重放窗口限制。
- `app_secret`、`access_token`、`token` 不进入响应、不进入普通日志。
- 自定义 `api_base_url` 必须限制 `https` 并做 SSRF 防护。
- 公众号 openid 属于外部用户标识，只作为 IM 用户 ID 使用，不自动绑定 WeKnora 账号。
- 不在客服消息里输出内部错误、堆栈、模型供应商密钥或签名 URL 的敏感 query。
- 如果答案包含由对象存储生成的临时 URL，需要确认当前租户策略允许外部微信客户端访问。

## 测试计划

### 单元测试

| 测试项 | 覆盖点 |
|---|---|
| 签名合法 | 正确 token/timestamp/nonce/signature 通过 |
| 签名缺字段 | 缺 `signature/timestamp/nonce` 任一字段失败 |
| 签名错误 | token 错误或 nonce 被改动失败 |
| timestamp 重放 | 超过 ±5 分钟失败 |
| URL 验证 | GET `echostr` 成功返回原文 |
| XML text | 解析 openid、content、msgid、direct chat |
| XML event | subscribe/unsubscribe 返回 nil message |
| XML unsupported | image/voice/link 返回 nil message |
| body rewind | Verify 后 Parse 仍能读到 body |
| Markdown 降级 | 代码块、表格、标题、引用、链接、图片、think 块 |
| 分片 | 550、1100、单段 800、中文 rune 边界 |
| token 缓存 | 首次获取、缓存命中、过期刷新、并发只刷新一次 |
| 发送错误码 | 0、45015、45009、40001/42001 重试一次 |

### Handler 测试

新增或扩展 `internal/handler` 测试：

- `wechat_mp` 平台创建渠道时默认 `webhook/full`。
- `wechat_mp` 正常 POST 回调返回 `200` 且空 body。
- `wechat_mp` `msg == nil` 回调返回 `200` 且空 body。
- 其他平台仍返回原有 JSON ack。
- 白名单接受 `wechat_mp`，非法平台仍拒绝。

### Service 测试

如果调整 dedup key：

- 同一 `channelID + messageID` 第二次被去重。
- 不同 channel 的相同 messageID 不互相去重。
- Redis 模式 key 前缀仍为 `im:dedup:`。

### 集成测试

使用 `httptest.Server` mock 微信 API：

1. mock `GET /cgi-bin/token` 返回 access token。
2. mock `POST /cgi-bin/message/custom/send` 收集 payload。
3. 构造微信 XML text callback。
4. 调用 `IMCallback`，断言同步响应为空 body。
5. 等待异步处理，断言客服消息请求数量、顺序和内容分片正确。

如果 QA 管道不易集成，可 mock 一个固定输出的 adapter/service 层测试，先证明平台适配器闭环。

## 本地调试流程

1. 启动 WeKnora 后端，确保公网 HTTPS 能访问回调地址。
2. 使用 ngrok 或等价内网穿透：

```bash
ngrok http 8080
```

3. 打开微信公众平台测试号页面，记录 `appID`、`appsecret`。
4. 在 WeKnora 创建 IM channel：

```json
{
  "platform": "wechat_mp",
  "name": "微信服务号测试",
  "mode": "webhook",
  "output_mode": "full",
  "credentials": {
    "app_id": "wx...",
    "app_secret": "...",
    "token": "your-random-token"
  },
  "enabled": true
}
```

5. 在微信公众平台“接口配置信息”填写：

```text
URL:   https://{ngrok-domain}/api/v1/im/callback/{channel_id}
Token: 与 credentials.token 一致
```

6. 保存配置，确认 GET 校验通过。
7. 扫码关注测试号，发送文本。
8. 预期现象：

- WeKnora 立即返回 `200` 空 body，微信不重试。
- 日志出现 `platform=wechat_mp` 回调和异步处理记录。
- 数秒后用户收到客服消息文本回复。

## 上线验收口径

MVP 完成必须满足：

- [ ] `go test ./internal/im/wechat_mp ./internal/im ./internal/handler/...` 通过。
- [ ] `go test ./internal/container/...` 或包含 container 的构建检查通过。
- [ ] `go build ./cmd/server` 通过。
- [ ] 测试号 GET URL 验证通过。
- [ ] 测试号文本消息能收到异步客服回复。
- [ ] 微信重试同一 `MsgId` 不会触发重复 QA。
- [ ] 客服消息窗口外错误不会无限重试。
- [ ] 日志不包含 `app_secret`、`access_token`、真实用户消息以外的敏感凭证。

如果本期包含前端配置，还需：

- [ ] `cd frontend && npm run type-check` 通过。
- [ ] IM 渠道创建页能选择“微信服务号”并保存凭证。
- [ ] 前端展示的回调 URL 可直接复制到公众平台。

## 分阶段执行建议

### Phase 1：后端 MVP

- 新增 `internal/im/wechat_mp` 包。
- 增加平台常量、白名单、默认模式、工厂注册、bot identity。
- 修正 `wechat_mp` 回调 ack 为空 body。
- 增加签名、XML、Markdown、client、handler 测试。

交付物：通过 API/SQL 创建渠道后，测试号文本问答闭环可用。

### Phase 2：可视化配置与文档

- 前端 IM 渠道面板支持 `wechat_mp`。
- `docs/IM集成开发文档.md` 增加微信服务号章节。
- README / README_CN 的 IM 平台列表按需补充。

交付物：用户无需手写 SQL 即可配置服务号。

### Phase 3：多媒体与生产增强

- 支持图片消息下载并入库或交给 VLM。
- 支持加密模式。
- 增加 Redis access token 缓存选项。
- 增加运营事件处理：关注欢迎语、菜单、人工兜底提示。

交付物：更完整的服务号生产接入能力。

## 风险清单

| 风险 | 影响 | 防御 |
|---|---|---|
| 5 秒内未 ack | 微信重试，可能重复触发 QA | `IMCallback` 立即空 body，异步处理 |
| `MsgId` 跨 channel 冲突 | 不同渠道消息被误去重 | dedup key 加 `channelID` |
| 客服消息窗口限制 | 长时间未互动用户收不到回复 | 识别 `45015`，记录 warn，不重试 |
| access token 失效 | 回复失败 | `40001/42001` 清缓存重试一次 |
| Markdown 原样发送 | 微信展示混乱 | 适配器强制纯文本降级 |
| 自定义 API endpoint 泄密 | secret/token 发往恶意地址 | 限制 https + SSRF 校验 |
| 前端未限制模式 | 用户误选 websocket/stream | `wechat_mp` 固定 webhook/full |
| 签名只在 GET 校验 | POST 可被伪造 | GET/POST 都验签 |

## 参考代码

- `internal/im/adapter.go`：IM 平台接口与常量
- `internal/im/types.go`：`IMChannel`、`computeBotIdentity`
- `internal/handler/im.go`：渠道创建、统一回调 ack、异步处理
- `internal/container/container.go`：适配器工厂注册
- `internal/im/wecom/webhook_adapter.go`：XML body rewind、access token 缓存、HTTP client、文件下载 SSRF 模式
- `internal/im/wechat/adapter.go`：现有微信个人号实现，仅作命名冲突和能力边界参考
- `docs/IM集成开发文档.md`：IM 模块总体设计，后续需补充微信服务号章节
