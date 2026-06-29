# AGENTS.md

本文档面向在 WeKnora 仓库中工作的 AI 编码代理和维护者。开始改动前先读本文件；进入 `cli/` 子模块时还必须阅读并遵守 `cli/AGENTS.md`，它定义了 CLI 面向 Agent 的输出契约、错误码和测试要求。

## 项目概览

WeKnora 是一个企业级知识管理与 RAG/Agent 框架。核心能力包括知识库管理、文档解析、混合检索、Agent 工具调用、Wiki 生成、MCP 服务、IM/小程序集成、网站嵌入 Widget、多租户 RBAC、模型与向量库适配、Langfuse 可观测性。

仓库是多组件 monorepo：

- 根 Go 模块：主后端服务，模块路径 `github.com/Tencent/WeKnora`，入口在 `cmd/server`。
- `frontend/`：Vue 3 + Vite + TypeScript 前端，含主 Web UI 和嵌入式 Widget 入口。
- `client/`：Go HTTP SDK，供 CLI、示例和外部集成使用。
- `cli/`：独立 Go 模块 `github.com/Tencent/WeKnora/cli`，实现 `weknora` 命令行和 CLI MCP server。
- `docreader/`：Python gRPC 文档解析服务，负责 PDF/Office/HTML/EPUB/MHTML 等解析。
- `mcp-server/`：Python MCP server，作为 WeKnora REST API 的 MCP 适配层。
- `miniprogram/`：微信小程序客户端。
- `docker-compose.yml`、`docker/`、`helm/`：本地、Compose 和 Kubernetes 部署资产。

## 关键目录

- `cmd/server/`：HTTP 服务启动、DI 容器构建、优雅关闭、启动引导逻辑。
- `cmd/desktop/`：Wails 桌面端入口。
- `internal/container/`：依赖注入注册和运行时组装。
- `internal/router/`：Gin 路由注册、任务路由和文件路由。
- `internal/handler/`：REST API handler，按资源拆分。
- `internal/application/service/`：业务服务层，包含知识库、知识解析、会话、Agent、Wiki、审计、租户等逻辑。
- `internal/application/repository/`：数据库与向量检索持久化实现。
- `internal/types/`：领域类型、请求/响应结构和接口契约。
- `internal/models/`：Chat、Embedding、Rerank、ASR、VLM 与 provider 适配。
- `internal/agent/`：ReAct Agent 引擎、工具、技能、审批、记忆和 token 估算。
- `internal/infrastructure/`：chunker、docparser、web fetch/search 等基础设施。
- `internal/datasource/`：飞书、Notion、语雀、RSS 等数据源同步。
- `internal/im/`：企业微信、飞书、Slack、Telegram、钉钉、Mattermost、微信等 IM 适配。
- `internal/middleware/`：鉴权、RBAC、审计、日志、中间件。
- `internal/tracing/langfuse/`：Langfuse trace 管理和观测封装。
- `config/`：默认配置和 prompt template。
- `migrations/`：MySQL、ParadeDB/PostgreSQL、SQLite 和版本化迁移。
- `skills/preloaded/`：内置 Agent Skills。
- `docs/`：用户、开发、API、Wiki 和部署文档。

## 常用命令

后端：

```bash
go test ./...
go test ./internal/application/service/...
go test ./internal/agent/...
go fmt ./...
golangci-lint run
go build -o WeKnora ./cmd/server
```

根目录 Makefile：

```bash
make build
make test
make fmt
make lint
make docs
make dev-start
make dev-app
make dev-frontend
make dev-stop
```

前端：

```bash
cd frontend
npm run dev
npm run build
npm run type-check
npm test
```

CLI：

```bash
cd cli
go test -count=1 ./...
go vet ./...
go build -o weknora .
```

DocReader：

```bash
cd docreader
python -m pytest
```

MCP server：

```bash
cd mcp-server
python main.py --check-only
python test_module.py
```

微信小程序：

```bash
cd miniprogram
npm test
```

## 本地开发流程

推荐使用快速开发模式：

```bash
make dev-start
make dev-app
make dev-frontend
```

服务地址：

- Web UI: `http://localhost:5173`
- 后端 API: `http://localhost:8080`
- PostgreSQL: `localhost:5432`
- Redis: `localhost:6379`
- MinIO Console: `http://localhost:9001`
- Neo4j Browser: `http://localhost:7474`

完整 Compose 部署：

```bash
cp .env.example .env
docker compose up -d
```

可选 profile 包括 `full`、`neo4j`、`minio`、`langfuse`、`searxng`、`qdrant`、`milvus`、`weaviate`、`doris`、`mcp` 相关服务。修改 Compose、环境变量或部署脚本时，检查 `.env.example`、`docker-compose.yml`、`docs/` 之间是否需要同步。

## 架构边界

后端按大致分层组织：

1. `handler` 只处理 HTTP 绑定、参数解析、响应和权限上下文传递。
2. `service` 承载业务规则、事务边界、任务编排、Agent/Wiki/RAG 流程。
3. `repository` 封装数据库、向量库和持久化查询细节。
4. `types` 和 `types/interfaces` 定义跨层契约，避免 handler 直接依赖基础设施实现。
5. `container` 是依赖注入集中点，新增服务或实现时通常需要在这里注册。

不要在 handler 中塞复杂业务逻辑；不要让 repository 反向调用 handler/service；不要绕过 service 直接从 API 层写数据库，除非已有同类本地模式明确这样做。

## 执行与路线图纪律

当用户要求“对齐路线图”“继续主线”“按路线图推进”或类似表达时，先重述当前主目标、所处阶段和本轮下一刀，再动手。这里的“下一刀”必须是可交付、可验证、可回挂到路线图或执行计划的一步。

每一刀都要可追踪。功能实现、迁移、清理、下线旧入口等改动，至少满足其中一项：

- 回挂到 `docs/ROADMAP.md`、`docs/wiki/项目概述/版本路线图.md` 或相关路线图文档。
- 登记到 `docs/exec-plans/` 下的执行计划或进度日志；目录不存在时，为跨轮长任务创建它。
- 明确登记为技术债追踪项，并说明它与当前主线的关系。

超过一轮的实现、迁移或清理必须落计划：在 `docs/exec-plans/` 写明目标、阶段、任务拆分、当前进度、验证口径和剩余缺口，并在后续轮次持续更新进度日志。不要让长任务只存在于对话上下文里。

主线优先级规则：

- 清理不能替代交付。连续两轮主要在做治理减法后，下一轮优先回到未完成主线。
- 当前规划与旧实现直接冲突时，先删除或下线阻碍主线的旧页面、旧命名、旧命令、旧文档，再继续实现；不要为了“看起来兼容”保留双轨。
- 已经选定本轮主线后，不要为顺手问题偏航。只有该问题直接阻塞当前交付、会让新改动变成假配置/假入口，或用户明确要求时，才切过去处理。
- 任何治理、重构、删除动作，动手前必须能用一句话说明它如何直接帮助当前主线交付；说不出来就记录为后续项，不立即执行。
- 实现主线时即使发现多个周边问题，默认只处理最直接阻塞的一项，其余登记后立即回到主线，不串行深挖。

完成和验证口径：

- 用户问“完成了么”时，先回答主线目标是否完成；周边清理、额外校验、可选优化必须单独标为“已做”或“未做”，不能混成“还差一点边角所以整体未完成”。
- 验证以证明交付为上限。先覆盖当前改动的真实风险；已经证明主线可交付后，不要因为还能继续跑更重检查就无限追加验证并拖延收口。
- 非纯问答的开发任务收尾必须给 `本轮完成度: X%`，并说明主线目标是否完成、验证情况、剩余缺口和下一刀。
- 路线图、长任务或多阶段主线还要给 `整体目标完成度: Y%`，并说明百分比口径。

## 编码约定

- Go 代码使用 `gofmt`/`gofumpt`，仓库 lint 启用 `lll`、`govet`、`revive`。
- 新增导出类型、函数、常量时写有意义的 godoc，说明原因或约束，不复述名字。
- 保持错误可诊断，优先包装上下文；用户可见错误需要能指导下一步。
- 环境变量、配置项和 API 字段改动要同步文档、示例和测试。
- 多租户、RBAC、API Key、JWT、加密凭据、SSRF、文件访问、对象存储签名 URL 相关改动必须额外谨慎，补充安全/权限测试。
- 处理文档解析、向量检索、Agent 流式事件、异步队列时，优先查找已有 pipeline、span、状态机和任务队列模式，不要另起一套状态字段。
- 修改数据库模型时同步 `migrations/versioned`，并考虑 SQLite Lite 模式和 ParadeDB/PostgreSQL 差异。
- 修改前端 API 类型或后端响应时，同时检查 `frontend/src/api`、`frontend/src/types`、相关组件测试和 `client/` SDK。
- 修改 CLI 时遵守 `cli/AGENTS.md`，尤其是 stdout/stderr、JSON envelope、NDJSON、错误码、exit-10 和 dry-run 契约。

## 测试策略

按改动范围选择最小但足够的验证：

- 纯 Go 单元改动：运行目标包测试，例如 `go test ./internal/application/service/...`。
- 共享类型、鉴权、租户、模型、检索、Agent 或解析状态改动：运行相关包及其相邻测试，必要时扩大到 `go test ./...`。
- API handler 改动：覆盖 handler、middleware、service 的相关测试，并检查 `docs/api/` 是否需要更新。
- 前端行为改动：运行 `npm test` 和必要的 `npm run type-check`；涉及构建配置时运行 `npm run build`。
- CLI 改动：在 `cli/` 运行 `go test -count=1 ./...`，如改动 wire contract，按 `cli/AGENTS.md` 更新 golden。
- DocReader 改动：运行 `docreader` 下相关 pytest；解析器并发、PDF、MHTML、Office、HTML 改动要覆盖对应测试。
- Docker/部署改动：至少验证 `docker compose config`，并检查 profile、healthcheck、volume、env_file 行为。

如果无法运行某项验证，在最终回复中明确说明原因和剩余风险。

## 前端注意事项

`frontend/` 使用 Vue 3、Vite、TypeScript、Pinia、Vue Router、TDesign Vue Next。入口包括：

- `src/main.ts`：主应用。
- `src/embed-main.ts`：网站嵌入 Widget。
- `vite.config.ts`：开发代理、embed HTML fallback、构建分块。

开发代理默认指向 `http://localhost:8080`，可通过 `VITE_DEV_PROXY_TARGET` 或 `FRONTEND_BACKEND_URL` 覆盖。UI 改动要遵循现有组件和 TDesign 风格，不要引入新的 UI 框架或大范围视觉重写，除非任务明确要求。

## 文档解析与文件处理

DocReader 是独立 Python gRPC 服务，默认端口 `50051`，由 Go App 通过 `DOCREADER_ADDR` 调用。扫描 PDF 会被渲染为图片后交由 Go App 侧 OCR/VLM 流程处理。涉及文件上传、解析、图片产物、临时目录或对象存储时，检查：

- `docreader/README.md`
- `internal/infrastructure/docparser/`
- `internal/application/service/knowledge_*`
- `docker-compose.yml` 中 `docreader` 环境变量和 volume

注意 `MAX_FILE_SIZE_MB`、`DOCREADER_*_MAX_WORKERS`、TLS/Auth、对象存储 URL、图片 URL 白名单等配置。

## Agent、Skills 与 MCP

Agent 相关代码集中在 `internal/agent/`，内置技能在 `skills/preloaded/`，CLI skills 在 `cli/skills/`。工具调用、审批、沙箱执行和流式展示都属于高风险行为，修改时要检查：

- 工具参数校验和类型转换。
- 审批 gate 与 destructive 行为。
- 沙箱模式和超时配置。
- Langfuse 观测事件。
- 前端/IM 对工具调用和思考过程的展示。

MCP 有两套实现：

- 后端内置 MCP 相关能力在 `internal/mcp`、`internal/handler/mcp_*`。
- 独立 Python MCP server 在 `mcp-server/`。
- CLI MCP server 在 `cli/internal/mcp`，规则见 `cli/AGENTS.md`。

不要把高风险写操作随意暴露给 AI-agent-callable 的 MCP tool surface。

## 安全与配置

`.env.example` 包含大量部署配置。不要提交真实密钥、token、私有 endpoint、生产密码或用户数据。涉及以下领域时必须检查权限和日志泄露风险：

- `JWT_SECRET`、API Key、OAuth2、模型凭据、MCP 凭据、数据源凭据。
- `TENANT_AES_KEY`、`SYSTEM_AES_KEY`、`CRYPTO_MASTER_KEY`、`CRYPTO_SALT`。
- SSRF 白名单、web fetch、搜索引擎、向量库连接测试。
- 文件下载、预签名 URL、本地文件路径、对象存储路径前缀。
- 多租户 RBAC、系统管理员引导、租户邀请、共享空间。

生产部署不应直接暴露未加固的内部服务端口；Compose 中部分服务默认仅用于本地或 profile opt-in。

## 文档同步

根据改动类型同步以下文档：

- 用户可见功能：`README.md`、`README_CN.md`、`docs/wiki/`。
- API 行为：`docs/api/` 和 Swagger 生成文件。
- 开发流程：`docs/开发指南.md`、`docs/dev/`。
- MCP：`docs/MCP功能使用说明.md`、`mcp-server/MCP_CONFIG.md`、`docs/zh/mcp-approval.md`。
- 内置模型/服务：`docs/BUILTIN_MODELS.md`、`docs/BUILTIN_MCP_SERVICES.md`。
- 版本说明：`CHANGELOG.md`。

不要只改实现不改示例配置，尤其是环境变量、Compose profile、端口和 API 字段。

## 提交前检查清单

- 改动范围与请求一致，没有顺手重构无关模块。
- 已读并遵守更深目录的 `AGENTS.md`。
- 格式化已执行，测试按风险范围运行。
- 配置、迁移、文档、前端类型和 SDK 已按需要同步。
- 没有提交真实密钥、生成缓存、构建产物或无关本地文件。
- 最终回复说明已做改动、验证命令和未验证项。
