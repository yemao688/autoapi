# Autoapi

> 自托管多协议 AI 模型路由网关 — 一个桌面应用，统一管理多家大模型 API。

把 OpenAI、Anthropic、DeepSeek、Moonshot、智谱 GLM 等多家上游聚合到一个本地网关。客户端可使用 OpenAI Chat、OpenAI Responses、Anthropic Messages 或 Gemini 原生接口；Autoapi 负责模型路由、受控协议转换、密钥管理、故障转移和使用统计。

## ✨ 核心特性

- **🔧 多上游聚合** — 一端点接入 OpenAI、Anthropic、DeepSeek、Moonshot、智谱 GLM 及任意 OpenAI 兼容服务。
- **🔌 多协议网关** — 支持 Chat Completions、Responses、Anthropic Messages 和 Gemini 原生入站协议，以及可保真的单跳协议转换。
- **🔀 智能模型路由** — 为每个客户端模型定义映射规则；每个目标独立选择原生或转换协议，并按 Tier、策略和顺序故障转移。
- **🔐 密钥安全** — 本地 AES-256-GCM 加密，主密码派生 Argon2id；上游密钥永不明文存储。
- **📊 实时统计** — Token 用量、请求日志、Provider 占比、模型排行、延迟直方图，支持 CSV/JSON 导出。
- **🖥️ 后台运行** — 关闭窗口自动收入托盘，代理持续运行；macOS Dock 智能隐藏。
- **🌍 双语界面** — 中文 / English，支持深色模式。

## 📸 界面预览

| 仪表盘 | 模型规则 | 使用统计 |
:---:|:---:|:---:|
| Dashboard | Model Rules | Usage Stats |

## 🚀 快速开始

### 下载安装

前往 [Releases](https://github.com/yemao688/autoapi/releases) 下载最新版本：

- **macOS**：`autoapi.app`（Universal，支持 Intel / Apple Silicon）
- **Windows**：`autoapi.exe`
- **Linux**：`autoapi.deb` / `autoapi.tar.gz`

下载后拖入 Applications（macOS）或双击安装即可。

### 从源码构建

<details>
<summary>展开构建步骤</summary>

**前置要求**：Go 1.25+、Node.js 20+、Wails CLI v2.12+

```bash
# 安装 Wails CLI（仅需一次）
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# 克隆仓库
git clone https://github.com/yemao688/autoapi.git
cd autoapi

# 安装前端依赖
cd frontend && npm install && cd ..

# 开发模式（热重载）
wails dev

# 生产构建
wails build -platform darwin/universal -clean    # macOS
wails build -platform windows/amd64 -clean       # Windows
wails build -platform linux/amd64 -clean         # Linux
```

</details>

## 💡 使用指南

### 1. 添加上游 Provider

打开 **Providers** 页面，添加你的 API 密钥。Autoapi 已预置主流服务商模板，也支持自定义 OpenAI 兼容端点。点击「测试」验证连通性。

### 2. 配置模型原生接口

在 **上游管理 → 编辑上游 → 可用模型 → 编辑模型** 中，为每个模型配置原生接口：

- **继承上游**：使用 Provider 的默认接口能力。
- **原生支持**：明确允许该模型直接接收此协议格式。
- **不支持**：明确禁止该模型使用此协议，路由器会尝试可用的单跳转换或下一个目标。

模型级设置优先于 Provider 默认值。例如上游整体支持 Responses，但某个模型只支持 Chat 时，可将该模型的 Responses 设为“不支持”、Chat 设为“原生支持”。

### 3. 创建模型规则

在 **模型规则** 页面定义客户端可见的模型名。例如创建 `gpt-4o`，映射到：

| 优先级 | 上游 | 模型 | 首字超时 |
|:---:|---|---|---|
| T1 | OpenAI | gpt-4o | 30s |
| T2 | DeepSeek | deepseek-chat | 15s |

客户端请求 `gpt-4o` 时，Autoapi 按目标顺序为每个 Target 选择原生协议或最佳单跳转换；T1 超时或发生可重试失败后自动切换 T2。原生目标和转换目标可以出现在同一故障转移链中。

### 4. 生成 API 密钥

在 **API 密钥** 页面创建本地密钥（ID 即密钥本身），供客户端使用。

### 5. 开始调用

```bash
# 客户端只需指向本地端点
export AUTOAPI_KEY='<your-local-api-key>'

curl -X POST http://localhost:8344/v1/chat/completions \
  -H "Authorization: Bearer $AUTOAPI_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}'
```

> 客户端使用的 Bearer token 是 Autoapi 生成的本地密钥 ID，**不是**上游 Provider 的密钥。上游密钥由 Autoapi 自动解密替换。

## 🏗️ 架构

- **后端**：Go 1.25 · Wails v2 · chi/v5 · modernc.org/sqlite
- **前端**：Vue 3.5 · Vite 8 · TypeScript 6 · vue-router 4 · Chart.js
- **安全**：Argon2id 主密码 · AES-256-GCM 密钥加密 · SQLite WAL
- **界面**：Apple 设计语言 · CSS 变量主题 · 响应式布局

## 📁 数据路径

| 内容 | 路径 |
|---|---|
| SQLite 数据库 | `~/.autoapi/autoapi.db` |
| 应用日志 | `~/.autoapi/logs/autoapi.log` |
| AES 密钥 | `~/.autoapi/` |

> `wails dev` 使用独立开发实例：Bundle ID `com.wails.autoapi.dev`、数据目录 `~/.autoapi-dev/`、代理端口 `18344` 和独立单实例锁，不影响正式环境。

## 📝 开发

```bash
go test ./internal/...     # 后端测试
cd frontend && npm run build   # 前端类型检查 + 构建
wails build                 # 完整构建
```

详见 [AGENTS.md](AGENTS.md)。

## 📜 更新日志

详见 [CHANGELOG.md](CHANGELOG.md)。

## 📄 许可证

[MIT](LICENSE) © 2026 yemao688

## 🔗 链接

- **仓库**：<https://github.com/yemao688/autoapi>
- **发布**：<https://github.com/yemao688/autoapi/releases>
- **问题反馈**：<https://github.com/yemao688/autoapi/issues>

OpenAI 兼容的本地代理网关 + 可视化管理面板，wails v2 + Go + Vue 3 + TypeScript 实现。

## 项目结构

```
.
├── main.go                          # Wails 入口
├── app.go                           # 依赖装配（store/service/proxy）
├── wails.json                       # Wails 项目配置
├── internal/
│   ├── api/                         # Wails Bind 方法（前端 ↔ Go 的 RPC 层）
│   ├── model/                       # 领域 DTO / 实体定义
│   ├── store/                       # SQLite 持久化（modernc.org/sqlite）
│   ├── service/                     # 业务逻辑：主密码/加解密/Provider 测试
│   └── proxy/                       # OpenAI 兼容本地代理（0.0.0.0:8344）
├── frontend/                        # Vue 3 + Vite + TypeScript
│   ├── src/
│   │   ├── App.vue                  # 根组件（MasterGate + AppWindow + AppToast）
│   │   ├── main.ts                  # 应用入口
│   │   ├── router.ts                # vue-router 路由
│   │   ├── api/bridge.ts            # 类型化的 Wails binding 封装
│   │   ├── components/              # 布局组件（AppWindow/SidebarNav/AppToast/MasterGate）
│   │   ├── composables/             # 逻辑复用（useApi/useTheme/useToast/useMasterGate/...）
│   │   ├── views/                   # 6 个页面
│   │   └── styles.css               # 设计系统（Apple 风格 CSS 变量 + dark 模式）
│   └── wailsjs/                     # 自动生成的 binding
└── build/                           # 构建产物与平台资源

```

## 前置要求

- Go 1.25+
- Node.js 20+
- Wails CLI v2.12+

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

## 运行 / 开发

### 1. 安装前端依赖

```bash
cd frontend && npm install
```

### 2. 热重载开发模式

```bash
wails dev
```

- Wails DevServer: http://localhost:34115
- Vite DevServer:  http://localhost:5173
- 开发代理端口:    http://localhost:18344

开发构建会自动打开 `Autoapi Dev` 窗口；前端保存即时热重载，Go 修改后自动重新编译。开发版使用 `~/.autoapi-dev/autoapi.db` 和独立 Bundle ID，可与正式版同时运行。

### 3. 生产构建

```bash
# macOS universal 二进制
wails build -platform darwin/universal -clean

# macOS 当前架构
wails build -platform darwin -clean

# Windows
wails build -platform windows/amd64 -clean

# Linux
wails build -platform linux/amd64 -clean
```

构建产物在 `build/bin/autoapi.app`（macOS）或 `build/bin/autoapi.exe`（Windows）。

## 验证

### 后端

```bash
go build ./...
go test ./internal/...
go vet ./...
```

### 前端

```bash
cd frontend
npm run build
```

### 代理端到端测试

启动应用后，默认在 `0.0.0.0:8344` 监听 OpenAI 兼容 API：

```bash
# 查看网关状态
curl http://localhost:8344/

# 查看合并后的模型列表
curl http://localhost:8344/v1/models

# 使用 autoapi 生成的 API Key ID 调用 chat completions
# 需要先在前端 API 密钥页创建/复制一个 key id
export AUTOAPI_KEY='<your-api-key-id>'
curl -X POST http://localhost:8344/v1/chat/completions \
  -H "Authorization: Bearer $AUTOAPI_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}'
```

> 注意：外部客户端使用的 `Authorization` Bearer 是 **autoapi 本地 API key 的 id**，不是上游 Provider 的 key。上游 key 由 autoapi 自动解密并替换。

## 主要功能

- **Provider 管理**：添加/测试 OpenAI、Anthropic、DeepSeek、Moonshot、智谱 GLM 及自定义上游；支持为每个模型覆盖 Chat、Responses、Messages、Gemini 原生接口能力。
- **API 密钥**：本地 AES-256-GCM 加密存储，主密码解锁；支持按 Provider/环境筛选。
- **模型规则**：基于模型、任务、estimated_tokens、header、时间等条件按优先级匹配，命中目标 Provider + 目标模型；支持 `matches`/`equals`/`lt`/`gt`/`between`/`in` 操作符。
- **使用统计**：实时 Token 用量、请求日志、Provider 占比、模型排行、P95 延迟。
- **本地代理**：`0.0.0.0:8344` 提供 Chat Completions、Responses、Anthropic Messages、Gemini、embeddings 和 models 接口；支持原生透传、受控单跳转换、SSE 流式转换和请求日志落库。
- **设置**：常规/外观/路由/API 服务/数据/高级/关于，支持数据导出（JSON/CSV）和日志清理。
- **深色模式**：跟随系统或手动切换浅色/深色。
- **主密码**：首次启动设置，所有 Provider 密钥均经主密码派生密钥加密。

## 数据存储

- SQLite 数据库：`~/.autoapi/autoapi.db`（所有平台）
- 应用日志：`~/.autoapi/logs/autoapi.log`
- 设置同样存储在数据库的 `settings` 表（JSON 键值）
- 开发实例：`~/.autoapi-dev/autoapi.db`、`~/.autoapi-dev/logs/autoapi.log`、代理端口 `18344`

## 不在 v1 范围

- WebSocket `/v1/stream`
- 全局键盘快捷键
- HTTP 上游代理
- 定时清理 cron
- JSON 备份导入

## 技术栈

- 后端：Go 1.25 + Wails v2 + chi/v5 + modernc.org/sqlite
- 前端：Vue 3.5 + Vite 8 + TypeScript 6 + vue-router 4
- 安全：argon2id 主密码 + AES-256-GCM 密钥加密
- 构建：wails build（跨平台）

## 许可证

本项目以 [MIT](LICENSE) 许可证开源。详见 LICENSE 文件。
