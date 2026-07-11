# Autoapi — AI 模型路由桌面端

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

开发构建会自动打开 macOS 应用窗口；前端保存即时热重载，Go 修改后自动重新编译。

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

- **Provider 管理**：添加/测试 OpenAI、Anthropic、DeepSeek、Moonshot、智谱 GLM 及自定义 OpenAI 兼容网关。
- **API 密钥**：本地 AES-256-GCM 加密存储，主密码解锁；支持按 Provider/环境筛选。
- **模型规则**：基于模型、任务、estimated_tokens、header、时间等条件按优先级匹配，命中目标 Provider + 目标模型；支持 `matches`/`equals`/`lt`/`gt`/`between`/`in` 操作符。
- **使用统计**：实时 Token 用量、请求日志、Provider 占比、模型排行、P95 延迟。
- **本地代理**：`0.0.0.0:8344` OpenAI 兼容接口（chat completions、embeddings、models），SSE 流式透传，请求日志自动落库。
- **设置**：常规/外观/路由/API 服务/数据/高级/关于，支持数据导出（JSON/CSV）和日志清理。
- **深色模式**：跟随系统或手动切换浅色/深色。
- **主密码**：首次启动设置，所有 Provider 密钥均经主密码派生密钥加密。

## 数据存储

- SQLite 数据库：`~/.autoapi/autoapi.db`（所有平台）
- 应用日志：`~/.autoapi/logs/autoapi.log`
- 设置同样存储在数据库的 `settings` 表（JSON 键值）

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
