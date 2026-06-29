# WeKnora 项目分析与 Windows 启动命令

## 项目概览

**WeKnora** 是腾讯开源的企业级 LLM 知识库框架（RAG + ReAct Agent + 自动 Wiki），由以下部分组成：

| 模块 | 技术栈 | 路径 |
|------|--------|------|
| 后端 API | Go 1.26 (Gin) | [cmd/server/](../cmd/server/) |
| 前端 Web UI | Vue 3 + Vite + TDesign | [frontend/](../frontend/) |
| 文档解析服务 | Python (gRPC :50051) | [docreader/](../docreader/) |
| CLI 工具 | Go (`weknora`) | [cli/](../cli/) |
| 桌面应用 | Wails (cmd/desktop) | [cmd/desktop/](../cmd/desktop/) |
| 基础设施 | PostgreSQL(paradedb) + Redis + 可选 MinIO/Qdrant/Neo4j/Langfuse | [docker-compose.yml](../docker-compose.yml) |

**重要提示**：项目的 [scripts/](../scripts/) 目录里全是 `.sh` 脚本（dev.sh、start_all.sh 等），Windows 下不能直接用 cmd/PowerShell 跑。需要 **Git Bash**、**WSL** 或直接走 **Docker**。

---

## Windows 启动方案

### 方案 1：Docker 一键启动（最推荐，零环境依赖）

前置：安装 [Docker Desktop for Windows](https://www.docker.com/products/docker-desktop/) 并启动。

在 **PowerShell** 或 **Git Bash** 中：

```bash
cd D:\cursor_workspace\WeKnora

# 1. 准备环境配置（首次运行）
copy .env.example .env      # PowerShell
# 或 Git Bash: cp .env.example .env
# 按需编辑 .env，至少设置 DB_USER / DB_PASSWORD / DB_NAME / REDIS_PASSWORD

# 2. 启动核心服务（前端 + 后端 + docreader + postgres + redis）
docker compose up -d

# 3. 访问
# Web UI:        http://localhost
# 后端 API:      http://localhost:8080
# Swagger 文档:  http://localhost:8080/swagger/index.html
```

可选 profile（按需追加）：

```bash
docker compose --profile neo4j --profile minio up -d      # 知识图谱 + 对象存储
docker compose --profile langfuse up -d                   # 自建可观测性 http://localhost:3000
docker compose --profile full up -d                       # 全部组件
docker compose down                                       # 停止
```

---

### 方案 2：开发模式（前端热重载，后端 Air 热重启）

适合频繁改代码。需要先装：**Go 1.26+**、**Node.js 18+**、**Docker Desktop**、**Git Bash**（或 WSL）。

```bash
# 在 Git Bash 中执行（PowerShell 跑不了 .sh）
cd /d/cursor_workspace/WeKnora
cp .env.example .env

# 终端 1：启动基础设施（postgres / redis / docreader / langfuse）
./scripts/dev.sh start
# 或带可选服务：./scripts/dev.sh start --minio --qdrant

# 终端 2：启动后端（Go，需要 Air 实现热重载）
go install github.com/air-verse/air@latest
./scripts/dev.sh app
# 等价于: make dev-app

# 终端 3：启动前端（Vite，自动热重载）
cd frontend && npm install
./scripts/dev.sh frontend     # 或 make dev-frontend
# 前端访问: http://localhost:5173 （代理到后端 8080）
```

常用开发命令：

```bash
./scripts/dev.sh status       # 查看容器状态
./scripts/dev.sh logs         # 查看日志
./scripts/dev.sh stop         # 停止基础设施
./scripts/dev.sh restart      # 重启
```

---

### 方案 3：Lite 模式（单二进制，零外部依赖）

最轻量，使用 SQLite + 内存队列，无需 Docker。适合个人本地体验。

```bash
# Git Bash 中
cd /d/cursor_workspace/WeKnora
cp .env.lite.example .env.lite
# 编辑 .env.lite，主要改 TENANT_AES_KEY / JWT_SECRET

# 构建（先构建前端到 web/，再构建 Go 二进制；CGO 因为 SQLite 必须启用）
make build-lite
# 跳过前端重建: SKIP_FRONTEND=1 make build-lite

# 启动
make run-lite
```

> 注意：Windows 下 Lite 构建需要 GCC（如 MinGW-w64 / TDM-GCC），因为启用了 `CGO_ENABLED=1` 用于 sqlite-vec。

---

### 方案 4：桌面端（Wails，Windows 原生应用）

```bash
cd /d/cursor_workspace/WeKnora/cmd/desktop
# 需要先安装 Wails CLI: go install github.com/wailsapp/wails/v2/cmd/wails@latest
wails dev       # 开发模式
wails build     # 打包 .exe
```

---

## 推荐选择

| 场景 | 推荐方案 |
|------|---------|
| 第一次体验 / 不改代码 | **方案 1：Docker 一键启动** |
| 二次开发 / 改 Go 或 Vue 代码 | **方案 2：开发模式**（Git Bash） |
| 想要个轻量本地版（不要 Docker） | **方案 3：Lite 模式** |
| 想要 Windows 原生桌面 App | **方案 4：Wails** |

**最关键的一句**：Windows 用户如果只想跑起来看看，直接装 Docker Desktop 然后 `docker compose up -d` 就行，不需要折腾 Go/Node/Python 环境。
