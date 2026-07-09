# 微信服务号 IM 接入执行计划

## 目标

按 `docs/dev/wechat-mp-integration-plan.md` 完成微信服务号 `wechat_mp` 后端 MVP：明文回调验签、XML 文本消息解析、5 秒内空 body ack、异步客服消息回复、消息去重和最小测试覆盖。

## 阶段

1. 后端 MVP：新增 `internal/im/wechat_mp`，完成平台注册、handler ack、bot identity、dedup key 和测试。
2. 可视化配置：前端 IM 渠道面板支持微信服务号，并补充用户文档。
3. 生产增强：图片/语音/文件、加密模式、运营事件和分布式 access token 缓存。

## 当前进度

- Completed: 新增 `internal/im/wechat_mp` 后端适配器包，覆盖明文签名、URL 验证、XML 文本解析、Markdown 降级、客服消息发送和 access token 缓存。
- Completed: 接入平台常量、handler 白名单、`wechat_mp` 默认 `webhook/full`、空 body ack、container 工厂注册、bot identity 和带 `channelID` 的去重 key。
- Completed: 添加目标测试，`go test ./internal/im/wechat_mp ./internal/im ./internal/handler/...` 已通过。
- Blocked by local environment: `go build ./cmd/server` 和 `go test ./internal/container` 仍失败，原因是本机 CGO 依赖缺少 `sqlite3.h`（`github.com/asg017/sqlite-vec-go-bindings/cgo`），不是本次微信服务号代码的编译错误。
- Completed: Phase 2 前端配置已接入，`IMChannelPanel` 支持选择「微信服务号」、填写 `app_id/app_secret/token/api_base_url`，并固定 `webhook/full`。
- Completed: `docs/IM集成开发文档.md` 已补充微信服务号快速接入、凭证字段、适配器行为、关键阈值和错误处理。
- Completed: `npm run build-only` 已通过。
- Blocked by existing frontend type debt: `npm run type-check` 仍失败，但失败项为全仓既有类型问题；本轮相关的 `IMChannelPanel.vue` 局部隐式 any 已修复，复跑输出中不再出现该文件。
- Pending: Phase 3 多媒体与生产增强。

## 验证口径

- `go test ./internal/im/wechat_mp ./internal/im ./internal/handler/...`
- `cd frontend && npm run build-only`
- `go build ./cmd/server`
- 测试号 GET URL 验证通过。
- 测试号文本消息收到异步客服消息回复。

## 剩余缺口

- 微信加密模式和多媒体输入尚未纳入本轮。
- 当前环境尚未做真实微信测试号联调。
