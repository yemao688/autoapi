# deepwork — Autoapi 桌面端原型实现

**任务**: 从 Open Design HTML 原型构建完整 wails v2 + go 桌面应用
**项目 ID**: 8f78e7d5-e16a-4824-b44c-163114122c51
**工作区**: /Volumes/E/work/go/autoapi-wails
**原型源**: /Users/shao/Library/Application Support/Open Design/namespaces/release-stable/data/projects/8f78e7d5-e16a-4824-b44c-163114122c51

## 目标

把 Apple/macOS 风格的 autoapi HTML 原型（AI 模型路由桌面应用）转成真实可运行的 wails v2 桌面应用。**保留视觉/交互设计**，前端工程化以支撑接口对接。

## 架构决策（已与用户确认）

| 维度 | 选择 | 理由 |
|---|---|---|
| 前端框架 | **Vue 3 + Vite + TS** | Wails 官方主推，TS 类型对接 Go 接口安全，原型 HTML 可逐页拆 .vue 组件 |
| 后端分层 | **internal/{model,store,service,proxy,api}** | 标准分层，可测试，可演进 |
| SQLite 驱动 | **modernc.org/sqlite** | 纯 Go，免 CGO，跨平台打包友好 |
| 数据目录 | **adrg/xdg** | 跨平台用户目录解析 |
| 本地代理 | **net/http/httputil.ReverseProxy + go-chi/chi/v5** | stdlib + 轻量路由，参照 one-api 架构 |
| 代理端口 | **0.0.0.0:8344** | 用户指定，对外可访问 |
| API key 存储 | **SQLite + argon2id 主密码哈希 + AES-256-GCM 加密密文** | 跨平台、可测试，避免 go-keyring 的 Linux dbus 依赖 |
| 双服务面 | **UI 走 Wails bridge (window.go.*)；外部客户端走 8344 HTTP** | 不可混用，UI 永不 fetch 8344 |

## 实现范围（全做）

- ✅ Provider/ApiKey/Route CRUD + SQLite 持久化
- ✅ OpenAI 兼容本地代理（/v1/chat/completions, /v1/embeddings, /v1/models）
- ✅ 路由规则引擎（matches/equals/lt/gt/between/in，优先级匹配，命中统计）
- ✅ 请求日志记录 + 聚合统计（dashboard/usage-stats 从真实数据聚合）
- ✅ 设置持久化 + JSON/CSV 数据导出
- ✅ 系统健康指标（先用 runtime.MemStats + listener 计数；CPU 可选 gopsutil）

## 不在 v1 范围（YAGNI，oracle 建议砍）

- ❌ WebSocket `/v1/stream` 端点（非 OpenAI 兼容）
- ❌ 菜单栏图标 / 登录时启动 / LaunchAgent
- ❌ 全局键盘快捷键 ⌘K / ⌘1-7
- ❌ 自动重试到备选 provider（SSE 流式下复杂度高）
- ❌ HTTP 代理上游
- ❌ 定时清理 cron → 改成"清理 N 天前"按钮
- ❌ JSON 备份导入（仅导出）

## 环境确认

- wails v2.12.0, go 1.26.4 darwin/arm64, node v25.8.1, npm 11.11.0
- 工作区是空仓库

## 已确认研究 — 原型地图 (exp-1)

### 页面清单（6 主页 + 2 归档）
- dashboard.html 总览 — KPI、7日 token 折线、provider 状态、最近活动、服务健康
- providers.html Provider 管理 — 卡片网格、测试/编辑/添加自定义 provider
- routes.html 路由规则 — 5 条规则、默认兜底、条件/目标/优先级/启用开关
- api-keys.html API 密钥 — 统计卡片、密钥表格
- usage-stats.html 使用统计 — 双面板（Token 用量 / 请求日志）单页切换
- settings.html 设置 — 7 个分区（常规/外观/路由/API服务/数据/高级/关于）
- 归档: logs.html.v0.4.2, tokens.html.v0.4.2（已被 usage-stats 合并）

### 设计系统 (styles.css)
- **设计语言**: Apple/macOS（SF Pro, traffic lights, 毛玻璃, 圆角分级）
- **CSS 变量**: `--bg #f5f5f7`, `--surface #fff`, `--fg #1d1d1f`, `--muted #6e6e73`, `--border #d2d2d7`, `--accent #0071e3`, `--radius-{xs,sm,md,lg,xl,pill}`, `--shadow-{sm,md,lg,window}`, `--font-{display,body,mono}`
- **Provider 色**: OpenAI `#10a37f`, Anthropic `#d97757`, DeepSeek `#272729`, Moonshot `#0071e3`, GLM `#2563eb`
- **状态色**: `--positive #28a745`, `--negative #d93025`, `--warning #f5a623`
- **间距**: 8px 基线网格 (`--space-1..20`)
- **动画**: `.status-dot` pulse 2.4s, `.view-pane.is-entering` 280ms, `prefers-reduced-motion` 支持
- **关键组件**: `.window`(1280×800) `.titlebar` `.sidebar`(240px) `.card` `.stat-card` `.tbl` `.tabs` `.toggle`(iOS) `.badge` `.dot` `.btn-{primary,secondary,ghost,icon}` `.theme-card` `.field`

### 领域实体
| 实体 | 关键字段 | 页面 |
|---|---|---|
| Provider | id,name,base_url,status,api_key_ref,avg_latency,monthly_tokens,last_tested_at,error_message | dashboard,providers |
| Model | id,provider_id,name,context_window | providers,routes,usage |
| Route | id,name,description,priority,enabled,created_at | routes |
| RouteCondition | route_id,field,operator,value | routes |
| RouteTarget | route_id,provider_id,model_name,action_type | routes |
| RouteStats | route_id,monthly_hits,monthly_savings | routes |
| ApiKey | id,provider_id,name,key_prefix,key_hash,permissions,environment,last_used_at,expires_at | api-keys |
| RequestLog | id,timestamp,status_code,provider,model,input_tokens,output_tokens,latency,route_id | usage,dashboard |
| TokenStats | date,input_tokens,output_tokens,cost,savings | dashboard,usage |
| Settings | key,value,category | settings |
| AppConfig | port(8344),bind_address(0.0.0.0),proxy_mode,debug,experimental | settings |

### 路由规则运算符（routes.html）
`matches`(glob), `equals`, `lt`, `gt`, `between`, `in`
条件字段: `model`, `header.x-priority`, `estimated_tokens`, `task`, `time.hour`

### 原型交互（app.js，Vue 迁移时需保留语义）
- 侧边栏 active 高亮、traffic lights 关闭动效、选项卡切换 + 键盘导航、toggle 同步、copy-btn、theme-card、live-toggle、clear-filters、相对时间刷新

## 已确认研究 — Wails v2 (lib-1)

### 项目结构
```
autoapi-wails/
├── main.go                     # wails.Run()
├── app.go                      # Bind struct 入口
├── go.mod / wails.json
├── internal/                   # 后端分层
│   ├── model/                  # 领域模型 + DTO
│   ├── store/                  # SQLite (modernc.org/sqlite)
│   ├── service/                # 业务逻辑
│   ├── proxy/                  # OpenAI 兼容代理 (httputil.ReverseProxy + chi)
│   └── api/                    # Wails Bind 方法
├── frontend/                   # Vue 3 + Vite + TS
│   ├── index.html
│   ├── src/
│   │   ├── main.ts
│   │   ├── App.vue
│   │   ├── router.ts
│   │   ├── api/                # 调用 window.go.* 的 TS wrapper
│   │   ├── components/         # 公共组件（Window, Sidebar, Titlebar...）
│   │   ├── views/              # 6 个页面 .vue
│   │   └── styles.css          # 原型 styles.css 全量保留
│   └── wailsjs/                # 自动生成的 Go 绑定
└── build/                      # 平台构建资源
```

### Go→JS Bridge
- `Bind: []interface{}{api}` 暴露方法
- 前端调用 `window.go.main.App.Method(args).then(...)`
- `frontend/wailsjs/go/main/App.js` 由 wails dev/build 自动生成
- TS 包装层把 `window.go` 包成 Promise typed client

### 关键命令
```bash
wails init -n autoapi -t vue-ts         # 初始化
wails dev -assetdir ./frontend/dist     # 热重载（Vite 产物）
wails build -platform darwin/universal -clean   # 构建
```

### 存储路径（macOS）
- DB: `~/Library/Application Support/autoapi/autoapi.db`
- 设置: 同目录 `config.json`
- 用 `adrg/xdg.DataFile("autoapi/...")` 解析

### 代理集成
- `OnStartup` 里启动 `http.Server{Addr:"0.0.0.0:8344"}` 在独立 goroutine
- `chi.Router` 处理 `/v1/*`，按路由规则选 provider，`httputil.ReverseProxy` 转发
- 每次请求写 RequestLog（异步 channel + 批量落库）
- SSE 流式响应透传

## 计划（已过 oracle 评审 APPROVE-WITH-CHANGES，9 项修订已并入）

### 并行 DAG
```
P0 ──┬── P1a (签名+DTO+generate module, 顺序) ──┬── P1b/c/d (store/service 实现) ──┬── P3 ──┬── P5 ── P5.5 ── P6
     │                                          │                                 │        │
     └──────────────────────────────────────────┴── P2 (前端骨架，并行) ────────────┘        └── P4 ──┘
```

### Phase 0 — 脚手架（顺序）
- `wails init -t vue-ts` 在临时目录后并入工作区
- **wails.json 配 Vite**：`frontend:install=npm install`, `frontend:build=npm run build`, `frontend:dev:watcher=npm run dev`, `frontend:dev:serverUrl=http://localhost:5173`
- go mod init，引入 modernc.org/sqlite, adrg/xdg, go-chi/chi/v5, golang.org/x/crypto (argon2+aesgcm)
- **macOS entitlements**：`build/autoapi.entitlements` 含 `com.apple.security.network.server=true`（0.0.0.0:8344 需要）
- 把原型 styles.css 复制到 frontend/src/styles.css
- 跑通空 wails dev（Vite HMR + Wails webview）
- **dev fixtures 种子**：`internal/store/fixtures.go`（dev 构建专用）seed 5 providers + 7 rules + token 历史

### Phase 1a — 后端契约（顺序，阻塞 P2）
- model 层：领域结构体 + DTO + JSON tag（**Model 是 lookup 表**，从上游 `/v1/models` 拉取，非用户 CRUD）
- api 层：`internal/api/app.go` 全部 Bind 方法签名骨架
- `wails generate module` 生成 frontend/wailsjs TS 类型
- **退出标准**: 前端可 import 生成的 typed bindings

### Phase 1b/c/d — 后端实现（可与 P2 并行）
- store 层：DB 初始化 + **WAL + synchronous=NORMAL + busy_timeout=5000**，版本化迁移（50 行 mini-migrator，不用 golang-migrate）
- store.Writer：单一写 goroutine + buffered channel，所有写路径都走它（避免 SQLite 单写锁竞争）
- service 层：业务逻辑（含 argon2id 主密码 + AES-256-GCM 密钥加解密）
- api 层：CRUD 实现
- **退出标准**: 所有 Bind 方法返回真实数据，单测覆盖 store + service

### Phase 2 — 前端骨架（与 P1b/c/d 并行，依赖 P1a）
- 拆公共布局：Window.vue (traffic lights + titlebar + sidebar + main)
- vue-router 配 6 个路由
- **复杂内联样式先原样保留**，再逐步抽 scoped class（routes/settings 两页 90%+ 是内联 style）
- 把 6 个原型 HTML 内容逐个搬进 .vue 模板（公共 class 走 styles.css）
- 建立 TS api wrapper（typed client over window.go）
- 保留原型交互语义（app.js 的 active 高亮/选项卡/toggle/copy/相对时间）
- **退出标准**: 6 页静态渲染 + 路由跳转 + 调通 mock/真实 Bind

### Phase 3 — 接口对接（依赖 P1b/c/d + P2）
- providers 页：列表/测试（拉 /v1/models）/编辑/添加 → 真实 CRUD
- api-keys 页：列表/添加/导出 → CRUD + argon2/aes 加解密
- routes 页：规则 CRUD + 条件编辑器（matches/equals/lt/gt/between/in）
- settings 页：7 分区持久化（视觉保留，锚点 aside 简单实现）
- dashboard 页：聚合查询
- usage-stats 页：日志查询 + token 聚合
- **退出标准**: 所有页面用真实数据，假数据删除

### Phase 4 — 代理网关（依赖 P1，可与 P3 尾部并行）
- chi router 注册 /v1/chat/completions, /v1/embeddings, /v1/models
- 路由规则引擎（condition matcher + 优先级 + action_type=skip）
- httputil.ReverseProxy 转发 + 鉴权 header 替换
- **SSE 流式**：FlushInterval=-1，context 取消传播，客户端断开检测
- ModifyResponse 捕获 usage（处理 OpenAI `data:[DONE]` 和 Anthropic `message_delta` 两种方言）
- 三级 token 计数：(a) 上游 usage 优先 → (b) tiktoken-go → (c) len/4 启发式
- 请求日志异步落库（走 store.Writer）
- **退出标准**: curl 调 8344 兼容 OpenAI 客户端，日志/统计真实更新

### Phase 5 — 系统集成
- dashboard 实时指标（runtime.MemStats + net.Listener 计数；CPU 可选 gopsutil）
- 设置页端口/绑定地址变更 → 重启代理（经 store.Writer 序列化）
- JSON/CSV 导出
- "清理 N 天前日志"按钮（替代 cron）
- 主题切换真实生效（data-theme 属性 + CSS 变量映射）
- **退出标准**: 设置变更生效，实时指标更新

### Phase 5.5 — 错误处理 + 日志（oracle 新增）
- log/slog 接入（替换 log.Default，ReverseProxy.ErrorLog 也指过来）
- 错误类型分类（ErrNotFound/ErrUnauthorized/ErrUpstream/ErrValidation...）
- UI 错误界面（toast / 错误边界 / 空状态）
- **退出标准**: 全链路有结构化日志，UI 错误有友好提示

### Phase 6 — 验证 + 打包
- wails dev 端到端跑通
- curl 测试 8344（OpenAI/Anthropic 客户端兼容性）
- wails build -platform darwin/universal
- **退出标准**: 可分发 .app，README 有运行/预览/验证命令

## 阶段状态
- [x] 调研：原型地图 (exp-1)
- [x] 调研：Wails v2 (lib-1)
- [x] 范围与架构决策
- [x] Oracle 评审计划 → APPROVE-WITH-CHANGES（9 项修订已并入）
- [x] Phase 0 脚手架（commit `9627b66`）— wails init vue-ts + 升级 Vue3.5/Vite8/TS6/vue-router4 + modernc/xdg/chi + entitlements + styles.css + `wails build` 通过
- [x] Phase 0 脚手架（commit `3aefe39`）
- [x] Phase 1a 后端契约（commit `3aefe39`）
- [x] Phase 1b/c/d 后端实现（commit `e6c670e`，fixer-1）
- [x] Phase 2 前端骨架（commit `e6c670e`，fixer-2）
- [x] Oracle 评审 Phase 1+2 → APPROVE-WITH-FIXES（commit `2bac68e` 修复）
  - B1: usage.go sortInts O(n²) → sort.Ints
  - B2: StoreService 去掉 plaintext 方法，App.CreateAPIKey 合成 Encrypt+Ciphertext
  - N1: writer.go 文档化 ErrQueueFull → caller 视为 drop 而非 fail
- [ ] Phase 3 接口对接
- [ ] Phase 4 代理网关
- [ ] Phase 5 系统集成
- [ ] Phase 5.5 错误处理 + slog
- [ ] Phase 6 验证 + 打包

## Phase 1b/c/d + 2 验证结果
- `go build ./...` ✅ / `go test ./internal/...` ✅ (14 pass) / `go vet` ✅
- `npm run build` ✅ (45 modules, 196KB JS / 22KB CSS)
- `wails dev` ✅ Vite 5173 + Wails 34115 双服务起来，dev fixtures 自动 seed（1480 日志/30 天），app 打包自签名成功
- 注：fixer-1 把 Writer 做成同步 Submit（非异步 channel），理由是测试需要写后立即可读；接受此决策
- 注：fixer-1 用纯 Go crypto/rand 生成 UUID，避免显式引入 google/uuid；接受
- 注：fixer-2 用 hash history（createWebHashHistory），因 vite dev 无 SPA fallback；接受，Phase 6 评估是否切 web history

## fixer-1 设计决策记录（后续 oracle 评审点）
- Writer.Submit 同步阻塞（非 buffered channel）— 牺牲批量写吞吐换简单+测试可靠
- execTx() 所有 CRUD 走 Writer；读直通 *sql.DB
- master_password 表单行 CHECK(id=1)，argon2id 双 pass（一个验密码一个派生 AES key）
- CreateAPIKey 返回 ErrCryptoRequired，CreateAPIKeyCiphertext 接受密文（store 不碰 crypto）
- fixtures 用 `initDev` 变量 + `init()` 替换模式绕过 build tag 调用点问题
- 成本估算用硬编码 per-model 价格表，未知模型走默认价

## Phase 1a 交付的关键路径（给后续 fixer 用）
- DTO: `internal/model/model.go` — Provider/Model/ApiKey/Route/RouteCondition/RouteTarget/RequestLog/Stat/TokenTrendPoint/ProviderShare/ModelRanking/DashboardData/ServiceHealth/UsageStats/Settings(7 子区)/Endpoint/ApiKeyInput/ProviderInput/RouteInput/LogQuery/ExportFormat/ProviderTestResult/ExportResult/ProxyStatus
- Bind 入口: `internal/api/app.go` — `type App struct{ctx, deps Deps}`, `NewApp(Deps)`, `Startup/Shutdown`, 30+ 方法
- DI 接口: `StoreService` / `BusinessService` / `ProxyService`（在 `api/app.go` 顶部定义）
- 根 `app.go`: `NewApp()` 工厂（目前 deps 全 nil，注释指明 Phase 1b/1c/4 注入点）
- 生成的 TS: `frontend/wailsjs/go/api/App.{js,d.ts}` + `frontend/wailsjs/go/models.ts`
- 注意 Wails 把 internal/api 包当 "api" 暴露（不是 main），前端调用是 `window.go.api.App.*`
