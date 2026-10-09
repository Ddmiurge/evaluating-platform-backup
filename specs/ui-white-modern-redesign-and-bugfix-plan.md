# UI 现代白色主题改造 + Bug 修复迭代设计方案

> 状态：审计完成，用户已确认方向（2026-05-28）
> 日期：2026-05-28
> 范围：`evaluating_platform/frontend`（主）+ `evaluating_platform/internal`（轻量扫描结论）
> 性质：本文档为 Phase 0 审计产出，未修改任何业务代码

### ✅ 用户已确认的决策

1. **Bug 修复范围**：只修高优先级 4 项——F1（轮询卡死）、F2（卸载后轮询不停）、F3（SSE 连接泄漏）、F5（ask_user 露 JSON）。中优（F4、F6-F9）、低优（F10-F19）、Go（G1-G4）**延后到后续迭代**（保留在第 3 章作为 backlog）。
2. **UI 改造顺序**：按方案默认——主题地基（三门户骨架+登录）→ 聊天工作台 → 专家/管理页。
3. **设计决策**：主色换现代蓝 **`#2563eb`**，侧栏用**白色**（更 SaaS 风）。
4. **额外范围**：无，按审计清单执行。

---

## 0. 摘要

- 前端目前是**黑底宝蓝"科技风"主题**，样式集中度较好：主题入口只有两处（`main.tsx` 的 ConfigProvider + `styles/theme.css` 的 CSS 变量），但**约 21 个页面组件中大量内联 style 硬编码暗色/品牌色**，白色化改造需触碰约 24 个文件。
- 主题改造**不涉及任何 API/数据层变更**，安全边界（payload/secret/evidence 不进浏览器）不受影响；已有 5 个纯逻辑测试文件（`*.test.ts`）不需要改动即可继续通过。
- Bug 审计发现 **3 个高优先级问题**（聊天轮询无异常保护导致会话永久卡"运行中"、SSE 连接泄漏、组件卸载后轮询不停），**6 个中优先级**，**10+ 个低优先级**。
- 建议迭代分 5 期：P0 清理 → P1 高优 Bug（F1/F2/F3/F5）→ P2 白色主题地基 → P3 聊天工作台 → P4 专家/管理页 → P5 回归收尾。中低优 Bug（F4、F6-F19）与 Go 项（G1-G4）延后为 backlog。

---

## 1. 前端 UI 现状盘点

### 1.1 技术栈与样式组织

| 项 | 现状 |
|---|---|
| 框架 | React 19 + Vite 7 + TypeScript 5.9（`tsc -b && vite build`） |
| 组件库 | antd ^6.3.2 + @ant-design/icons ^6.1.0 |
| 主题入口 1 | `src/main.tsx`：`ConfigProvider` + `theme.darkAlgorithm` + token 覆盖（Layout/Menu/Table/Card/Input/Select 组件级 token） |
| 主题入口 2 | `src/styles/theme.css`：`:root` CSS 变量（背景/文字/边框/状态色）+ 全局类（`.tech-card`、`.status-running` 脉冲、severity 色）+ **`.ant-btn-primary` 带 `!important` 的全局覆盖** |
| 页面样式 | **几乎全部内联 `style={{...}}`**，混用 CSS 变量与硬编码 hex/rgba；无 CSS Modules / styled-components / tailwind |
| 死代码 | `src/index.css`、`src/App.css` 未被任何文件 import（Vite 模板残留）；`package.json` 中 `reactflow` 从未 import；`@types/react-router-dom@5` 与实际使用的 v7 不匹配 |
| 死服务 | `services/enterprise.ts` 为空对象；`services/expert.ts` 的 `getCategories()`（指向 410 tombstone `/tools/categories`）与 `listEvalPackages()`（指向已删除的 `/eval-packages`）从未被调用 |

### 1.2 当前配色体系（"黑底宝蓝科技风"）

| Token | 当前值 | 用途 |
|---|---|---|
| `--bg-base` | `#0a0c10` | 页面底色（body、Layout） |
| `--bg-surface` | `#0f1117` | 侧栏/顶栏/表格容器 |
| `--bg-card` | `#141820` | 卡片 |
| `--color-primary` | `#1a6dff`（hover `#3d85ff` / active `#0052d9`） | 宝蓝主色 |
| `--text-primary` | `#e8eaf0` | 主文字 |
| `--text-secondary` / `--text-muted` | `#8890a4` / `#4a5268` | 次要/弱化文字 |
| `--border-color` | `#1e2535` | 边框 |
| 状态色 | critical `#ff4d4f` / high `#ff7a45` / medium `#ffc53d` / low `#52c41a` / info `#40a9ff` | |
| 发光 | `--glow-primary`、`--glow-card`、`pulse-glow`、`scan-line` | 暗色科技感效果 |
| 字体 | `Inter` + system stack（`fontFamily` token） | |
| 圆角 | token `borderRadius: 6`，卡片内联 8/10/12 混用 | 不统一 |

**内联硬编码颜色盘点**（白色化必须逐一处理）：

- `#4d96ff`（链接蓝/数字/图标）：Login、ChatPage、ChatCards、EnterprisePortal、BillingPage、UserManagement、AdminDashboard
- `#69db7c`（成功绿）：ChatCards（机器人头像、报告卡、进度卡）——白底上对比度不足
- `#ff7a45` / `rgba(255,122,69,...)`（专家橙）：ExpertPortal、BillingPage
- `#8b5cf6` / `rgba(139,92,246,...)`（管理紫）：AdminPortal、AdminDashboard
- `#ffd43b`、`#ff6b6b`：ChatCards capabilityVisual
- `rgba(26,109,255,...)`（蓝色系透明度）：几乎所有门户/卡片
- `rgba(105,219,124,...)`：ChatCards 头像边框
- `rgba(255,255,255,0.08)`：进度条 trailColor（白主题下会看不见）
- `rgba(0,0,0,0.24)`：markdown code block 背景（白主题下变脏灰）
- Login.tsx 内联重复实现了一遍网格背景（与 `.tech-grid-bg` 重复）

**AntD 侧暗色绑定点**：

- `main.tsx`: `algorithm: theme.darkAlgorithm`、`Layout siderBg/headerBg/bodyBg`、`Menu darkItemBg/darkSubMenuItemBg/darkItemSelectedBg/darkItemSelectedColor`、`Table headerBg/rowHoverBg`
- 三个门户（EnterprisePortal/ExpertPortal/AdminPortal）`<Menu theme="dark">`
- `theme.css`: 自定义滚动条（暗色）、`.ant-btn-primary !important` 全局覆盖（与 ConfigProvider token 叠加，改造时必须移除以免冲突）

### 1.3 布局结构现状

- **三门户同构**：固定 220/220/228px 左侧 Sider（logo + Menu + 底部余额/收益卡）+ sticky Header（日期/标题 + 铃铛 + 用户 Dropdown）+ Content。
- **企业门户**：`/enterprise` 聊天工作台（ChatPage：左侧 260px 会话列表 + 消息区 + 底部输入区 + 被测模型 Drawer），`billing`/`settings` 有 24px padding，聊天页无 padding 全高。
- **专家门户**：SampleManager（卡片栅格）、EngineManager（Tabs：模板分类卡片 + ComposedAttackManager）、MaclawSkillManager（搜索 + 卡片栅格 + 发布 Drawer）、MaclawMCPServerManager（表格 + Drawer）。
- **管理门户**：Dashboard（Statistic 卡）、UserManagement（服务端分页表格）、AccountTenantManagement（表格 + 模型配置 Drawer）、ResourceGovernance/JobGovernance（表格）、MaclawModelConfig（表单卡）、MaclawHubManagement（表单 + 状态表格）、SystemHealth（Descriptions）。
- 聊天工作台高度写死 `calc(100vh - 64px)`，与 Header 64px 隐式耦合（三个门户一致，可保留）。

### 1.4 已验证符合约束的行为（改造时不得回归）

1. **sending/running/loading 指示器按 session id 隔离**：`sendingSessionId`/`loadingSessionId`（单值）+ `runningSessionIds`（Set）+ `activeIdRef` 守卫，切走会话后异步回调不再写当前视图（`sendPrompt` 内 `getActiveSessionId() === sessionId` 判断）。`chatDisplaySessionIsolation.test.ts` 覆盖 `messagesForSession` 的孤儿消息过滤。
2. **ask_user 渲染为普通追问**：`toChatMessage`（maclawRuntime.ts:305-315）把 `response_source=ask_user` 的 JSON 转为普通气泡 + 选项按钮，不展示 JSON 原文（仅一个边缘缺陷见 F6）。
3. **只有 `response_source=plan_confirm` 渲染执行确认卡**：`maclawRuntimePlan.ts` + `sanitizeDirectMessage` 双重把关。
4. **BFF 对 SSE data 有敏感字段 redact**（`sanitizeRuntimeEventData`），前端改造不触碰数据面。
5. **nginx `/api/v1/` 650s 超时 + `proxy_buffering off`**；BFF ConfirmPlan 10min context / 前端 axios 600s 三层匹配。
6. 前端测试文件（`chatDisplay*.test.ts`、`chatMarkdown.test.ts`、`jobProgressMessages.test.ts`、`runtimeRunPolicy.test.ts`）均为纯断言脚本，由 `tsc -b` 类型检查 + `scripts/test-maclaw-runtime-adapters.mjs` 部分执行；UI 改造不修改这些文件的被测纯函数签名即可。

---

## 2. 白色现代风改造方案

### 2.1 设计目标

1. 白色为设计主基调（surface 为主，浅灰背景衬托层次），保留蓝色系主色延续品牌识别。
2. 去科技风元素：移除网格纹理、扫描线、发光阴影、暗色滚动条；改为**浅色柔和阴影 + 细边框**的现代 SaaS 质感（参考 Linear / Vercel / AntD 5 官方 light 风格）。
3. 样式仍然只用两处入口（ConfigProvider + CSS 变量），不引入新样式方案，降低对现有内联 style 的侵入面。
4. 语义色在白底上满足 WCAG AA 对比度。

### 2.2 设计 Token 新旧对照

`styles/theme.css` `:root` 变量重映射（变量名不变，仅改值 → 内联 `var(--xxx)` 引用大部分自动适配）：

| Token | 旧值（暗） | 新值（白） | 说明 |
|---|---|---|---|
| `--bg-base` | `#0a0c10` | `#f6f7f9` | 页面底 |
| `--bg-surface` | `#0f1117` | `#ffffff` | 侧栏/顶栏/表格容器 |
| `--bg-card` | `#141820` | `#ffffff` | 卡片 |
| `--bg-hover` | `#1a2030` | `#f2f6fd` | hover |
| `--bg-active` | `#1f2740` | `#e8f0fe` | active/选中 |
| `--color-primary` | `#1a6dff` | `#2563eb`（已确认：现代蓝） | 主色 |
| `--color-primary-hover` | `#3d85ff` | `#3b82f6` | |
| `--color-primary-active` | `#0052d9` | `#1d4ed8` | |
| `--color-primary-light` | `rgba(26,109,255,.15)` | `rgba(37,99,235,.10)` | 选中底色 |
| `--color-primary-border` | `rgba(26,109,255,.4)` | `rgba(37,99,235,.35)` | |
| `--text-primary` | `#e8eaf0` | `#0f172a` | |
| `--text-secondary` | `#8890a4` | `#475569` | |
| `--text-muted` | `#4a5268` | `#94a3b8` | |
| `--text-link` | `#4d96ff` | `#2563eb` | |
| `--border-color` | `#1e2535` | `#e2e8f0` | |
| `--border-light` | `#252d3d` | `#eef2f7` | |
| `--glow-primary` | 蓝色发光 | `0 1px 2px rgba(15,23,42,.05)` | 语义改为柔和阴影（变量名保留，值替换） |
| `--glow-card` | 黑色投影 | `0 1px 2px rgba(15,23,42,.04), 0 4px 12px rgba(15,23,42,.06)` | 同上 |
| 状态色 | `#ff4d4f/#ff7a45/#ffc53d/#52c41a/#40a9ff` | `#dc2626/#ea580c/#d97706/#16a34a/#0284c7` | 白底 AA 对比度 |
| 新增 `--radius-card: 12px`、`--radius-control: 8px` | — | — | 统一圆角（可选） |

**附带必须处理的暗色专用规则**（theme.css）：

- 删除/重写：`.tech-grid-bg`（或改为极淡蓝色网格可选保留）、`scan-line`、`data-flow`、`pulse-glow`（运行中状态改为柔和呼吸或小圆点动画）、`.ant-btn-primary` 的 `!important` 块（**必须删**，交给 ConfigProvider token）、暗色滚动条（改 `#e2e8f0` thumb / hover `#cbd5e1`）。
- body `background-color`/`color` 改为浅色。

### 2.3 AntD ConfigProvider（main.tsx）

```tsx
const brandBlue = '#2563eb' // 已确认：现代蓝

<ConfigProvider
  locale={zhCN}
  theme={{
    algorithm: theme.defaultAlgorithm,   // darkAlgorithm -> defaultAlgorithm
    token: {
      colorPrimary: brandBlue,
      colorBgBase: '#f6f7f9',
      colorBgContainer: '#ffffff',
      colorBgElevated: '#ffffff',
      colorBorder: '#e2e8f0',
      colorText: '#0f172a',
      colorTextSecondary: '#475569',
      borderRadius: 8,
      fontFamily: "'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",
    },
    components: {
      Layout: { siderBg: '#ffffff', headerBg: '#ffffff', bodyBg: '#f6f7f9' },
      Menu: {
        itemBg: 'transparent',
        itemSelectedBg: 'rgba(37,99,235,0.10)',
        itemSelectedColor: '#2563eb',
        itemColor: '#475569',
        itemHoverBg: '#f2f6fd',
      },
      Table: { headerBg: '#f8fafc', rowHoverBg: '#f1f5f9' },
      Card: { colorBgContainer: '#ffffff' },
    },
  }}
>
```

要点：

- `Menu` 组件 token 从 `darkItemBg` 系列**换成浅色系列**（antd 6 下 `dark*` token 只对 `theme="dark"` 生效）。
- 三个门户的 `<Menu theme="dark">` 改为默认（浅色）或显式 `theme="light"`，并给 Sider 补 `borderRight: 1px solid var(--border-color)`（已有）。
- 移除 `Button` 组件 token 中的主色覆盖（由 colorPrimary 派生即可），让 hover/active 走 AntD 默认派生或显式给 `colorPrimaryHover/Active`。

### 2.4 页面级改动清单

工作量：★少（仅变量自动适配）/ ★★（需改内联色）/ ★★★（布局/结构级调整）

| 文件 | 工作量 | 改动内容 |
|---|---|---|
| `main.tsx` | ★★★ | ConfigProvider 全量替换（2.3） |
| `styles/theme.css` | ★★★ | 变量重映射 + 删 !important 按钮/动画/滚动条重写 |
| `index.css` / `App.css` | 删除 | 死文件 |
| `pages/Login.tsx` | ★★ | 内联网格背景→浅色渐变或纯色；卡片白底浅边；图标底改 `--color-primary-light` |
| `pages/enterprise/EnterprisePortal.tsx` | ★★ | Menu theme、余额卡、头像 rgba 蓝 |
| `pages/enterprise/ChatPage.tsx` | ★★★ | 会话列表选中态 rgba 蓝→primary-light；输入区/边框；LoadingOutlined #4d96ff；目标 Drawer 表单随 token 自动适配；内联 bounce keyframes 可保留 |
| `pages/enterprise/ChatCards.tsx` | ★★★ | `RISK_COLORS` 换 AA 色；`#69db7c`→`#16a34a`；`#4d96ff`→主色；code block `rgba(0,0,0,0.24)`→`#f1f5f9`+`#e2e8f0` 边框；进度条 trailColor→`#eef2f7`；capabilityVisual 四色重配；气泡底色（用户蓝 tint / 助手白） |
| `pages/enterprise/BillingPage.tsx` | ★★ | 图标底/数字色（F4 分页已延后，不在本次修复） |
| `pages/enterprise/Settings.tsx` | ★ | 变量自动适配 + 提示框 tint |
| `pages/expert/ExpertPortal.tsx` | ★★ | Menu theme、收益卡橙色、头像 |
| `pages/expert/SampleManager.tsx` | ★ | 变量自动适配（卡片 style 用的是变量） |
| `pages/expert/EngineManager.tsx` | ★★ | 变量适配 + `bodyStyle`→`styles.body`（F13，同文件顺手替换） |
| `pages/expert/ComposedAttackManager.tsx` | ★ | 变量自动适配 |
| `pages/expert/MaclawSkillManager.tsx` | ★ | 变量自动适配 |
| `pages/expert/MaclawMCPServerManager.tsx` | ★ | 变量自动适配 |
| `pages/admin/AdminPortal.tsx` | ★★ | Menu theme、紫色头像 rgba |
| `pages/admin/AdminDashboard.tsx` | ★★ | 卡片色 `#4d96ff/#52c41a/#8b5cf6`→新语义色 |
| `pages/admin/UserManagement.tsx` | ★ | 变量适配 |
| `pages/admin/AccountTenantManagement.tsx` | ★ | 变量适配 + `destroyOnClose`→`destroyOnHidden`（F13 同文件顺手替换） |
| `pages/admin/ResourceGovernance.tsx` / `JobGovernance.tsx` | ★ | 变量适配（F7 错误处理已延后） |
| `pages/admin/MaclawModelConfig.tsx` / `MaclawHubManagement.tsx` / `SystemHealth.tsx` | ★ | 变量适配 |
| `package.json` | 清理 | 移除 `reactflow`、`@types/react-router-dom` |
| `services/enterprise.ts` | 删除 | 空模块 |
| `services/expert.ts` | 清理 | 删 `getCategories`/`listEvalPackages` 死方法 |

> 明确**不改**：`services/*.ts`（除死代码清理）、`components/ProtectedRoute.tsx`、`context/AuthContext.tsx`、5 个 `*.test.ts`、`nginx.conf`、`Dockerfile`、所有后端 API 行为。

### 2.5 分阶段实施顺序（与 Bug 修复合并，详见第 4 章）

P0 清死代码 → P1 高优 Bug（F1/F2/F3/F5，用户已确认）→ P2 主题地基 → P3 聊天工作台 → P4 专家/管理页 → P5 回归收尾。

### 2.6 决策点（已全部确认）

1. **主色**：✅ 换 `#2563eb` 现代蓝（更 SaaS 风）。
2. **侧栏形态**：✅ 白色侧栏 + 浅色菜单。
3. **登录页**：✅ 按默认方案 A（浅色渐变/纯色背景 + 白卡片）。

### 2.7 风险点

| 风险 | 影响 | 缓解 |
|---|---|---|
| antd 6 对废弃 prop（`Card bodyStyle`、`Drawer destroyOnClose`、`Statistic valueStyle`、`Space direction`）的兼容性 | 部分内联样式可能静默失效或升级即炸 | P4 统一替换为新 API（`styles.body`、`destroyOnHidden`、`styles.content`、`orientation`），替换前后 `npm run build` + 视觉对比 |
| `#69db7c` 等浅色在白底对比度不足 | 报告卡风险等级/成功态不可读 | 全部换 AA 语义色（第 2.2 表） |
| 硬编码色遗漏（尤其 rgba 透明度系列） | 残留暗色块/看不清的文字 | P2 完成后全局 grep `#[0-9a-f]{6}`、`rgba(` 复查清单 + 三门户人工过屏 |
| `.ant-btn-primary !important` 删除后按钮观感变化 | 全局按钮外观 | ConfigProvider token 显式配置主色三态，视觉回归对比 |
| 聊天页消息气泡/进度卡信息密度高，白底下视觉噪音 | 可读性下降 | 卡片用 `--bg-surface` + 细边框 + 12px 圆角，禁用大面积灰底 |
| 品牌深色 logo（qianxin png）在白色侧栏上的观感 | logo 可能发糊 | 保留原透明底 logo，视效果决定是否加浅色圆形底衬 |
| `npm run build` 必须通过（tsc 严格） | 类型错误 | 不改被测纯函数签名；分阶段提交，每阶段跑 build |

---

## 3. Bug 清单

> 优先级定义：P0 = 主链路不可用/卡死；P1 = 功能明显异常或资源泄漏；P2 = 体验/健壮性；P3 = 代码卫生。
> 前端共 19 项（F1-F19 内含合并项），Go 4 项（G1-G4）。
>
> **本次迭代范围（用户已确认）**：仅 F1、F2、F3、F5。其余项目（F4、F6-F9、F10-F19、G1-G4）保留为 backlog，延后到后续迭代处理。

### 3.1 前端高优先级

**F1｜评测任务轮询循环无异常保护，会话永久卡"运行中"** — P0
- 位置：`pages/enterprise/ChatPage.tsx:239-294`（`waitForEvaluationJob`）；调用点 `:317/:342/:394/:494/:500/:502` 均为 `void waitForEvaluationJob(...)` 无 `.catch`
- 现象：确认执行后，若轮询期间任一 `getEvaluationJob`/`getSession`/`getEvaluationJobRecovery` 请求失败（网络抖动、后端 429——注意后端限流是 100 次/分钟/用户，1Hz 轮询叠加其他请求可能触达），循环直接中断：① 控制台 unhandled rejection；② `markSessionRunning(sessionId,false)` 永不执行 → 该会话输入框永久禁用、placeholder 一直"评估任务执行中，请稍候..."，只能刷新页面恢复。
- 根因：async 轮询循环体内 `await chatService.getEvaluationJob(jobId)`（行 242）等调用没有 try/catch；调用方 fire-and-forget 且无兜底。
- 修复思路：循环体包 try/catch；单次失败容忍（连续失败 N 次才终止并渲染 failed 进度卡 + 恢复 running 标记）；终止路径统一 finally 中 `markSessionRunning(sessionId,false)`。回归用例：断网/500/429 模拟下会话可自动降级为失败卡而非卡死。

**F2｜ChatPage 卸载后轮询不停止（网络泄漏 + 跨页面全局 toast）** — P1
- 位置：`ChatPage.tsx:239`（`waitForEvaluationJob`）、`:355-383`（`waitForAssistantReply`）；卸载清理只有 `:627-629`（仅停 SSE，不停轮询）
- 现象：从聊天页切到计费页（组件卸载）后，`activeIdRef.current` 保持不变仍等于 sessionId，轮询继续最长 650 次 × 1s / 60 次 × 1.5s；期间 `message.error('评测任务排队超时...')` 等全局 toast 会弹在**其他页面**上。
- 根因：轮询循环的存活条件是 `activeIdRef.current !== sessionId`，而 ref 不随组件卸载失效；无 unmount 信号。
- 修复思路：增加 `mountedRef`（卸载置 false 并在循环条件中检查）；或用 AbortController 集中管理（stop 时 abort）。

**F3｜SSE 事件流连接泄漏 + 静默结束不回调** — P1
- 位置：`services/maclawRuntime.ts:627-665`（`streamRunEvents`）
- 现象（两个子问题）：
  a) 收到 terminal envelope 后 `break`（行 649-653），但**既不 `reader.cancel()` 也不 `controller.abort()`**——服务端连接保持打开，多次评测后浏览器累积悬挂连接（每个 run 一条）。
  b) 服务端直接关闭流（`done=true`）但未发 terminal envelope 时（行 657 `if (done) break`），`onDone`/`onError` 均不调用 → ChatPage 的 `streamStopRef.current` 保持非 null、`markSessionRunning(false)` 不执行 → 仅靠 SSE 无 job 轮询兜底的路径会卡"运行中"。
- 根因：terminal/自然结束两条退出路径都缺少资源回收与回调约定。
- 修复思路：terminal 后调用 `reader.cancel().catch(()=>{})`；`done` 无 terminal 时按完成处理调用 `onDone()`；catch 分支已正确处理 abort。回归用例：伪造提前断流，断言 onDone 被调且无连接残留（Chrome DevTools Network 观察）。

### 3.2 前端中优先级

**F4｜计费页分页完全失效** — P1
- 位置：`pages/enterprise/BillingPage.tsx:106`、`:119`；数据加载 `:33-37` 只拉 `(20, 0)` 首页
- 现象：Table `pagination={{ total, pageSize: 20 }}` 声明了服务端 total，但没有 `onChange` 拉取 offset 页 → 翻到第 2 页是空表。
- 修复思路：参照 `UserManagement.tsx`（已正确实现服务端分页）补 `current/onChange/offset` 状态；两个 Tab（records/transactions）各自维护页码。

**F5｜ask_user 无 question 字段时展示原始 JSON** — P1（约束冲突项）
- 位置：`services/maclawRuntime.ts:305-312`（`toChatMessage`）
- 现象：`runtimeJSON.response_source === 'ask_user'` 但 `question/message/content` 均缺失时，`content` 保持原始 JSON 字符串，用户直接看到 `{"response_source":"ask_user",...}`——违反"ask_user 必须渲染为普通问题而不是原始 JSON"约束。
- 修复思路：该分支下若取不到 question，content 兜底为固定文案（如"需要补充更多信息，请继续描述你的需求。"）；不要透出 JSON。
- 兜底校验：`ChatCards.tsx:635` 选项按钮已有 `askUserOptions` 空数组防护，不需改。

**F6｜表单 `validateFields()` 拒绝未捕获（全局 unhandled rejection 模式）** — P2
- 位置（均为 `await form.validateFields()` 在 try 之外）：
  - `pages/expert/MaclawSkillManager.tsx:213`（handleSearch）、`:243`（handleImport）
  - `pages/expert/MaclawMCPServerManager.tsx:131`（saveServer）
  - `pages/admin/MaclawModelConfig.tsx:64`（save）、`:91`（validate）、`:107`（test）
  - `pages/admin/MaclawHubManagement.tsx:57`（save）
- 现象：必填校验失败时 antd 表单显示错误，但 Promise 拒绝成为 unhandled rejection（控制台噪音；未来 React/Node 可能显式告警）。
- 修复思路：统一包 `try { const values = await form.validateFields() } catch { return }`，或封装 `safeValidate(form)` 工具。

**F7｜admin 三个列表加载无错误处理** — P2
- 位置：`pages/admin/AccountTenantManagement.tsx:22-30`、`ResourceGovernance.tsx:17-25`、`JobGovernance.tsx:18-26`（`try { … } finally` 无 `catch`）
- 现象：接口失败 → unhandled rejection + 页面静默空白（仅 loading 消失），用户无从得知。
- 修复思路：`catch { message.error('加载失败') }`。

**F8｜全局 30s axios 超时覆盖部分长请求** — P2
- 位置：`services/api.ts:5`（`timeout: 30000`）；受影响：`reportService.downloadMaclawReport`（PDF 导出）、`chatService.probeEvaluationTarget`（被测模型连通性检查，真实模型调用）
- 现象：慢模型健康检查或大报告导出 > 30s 被前端掐断，报"请求失败"误导用户。
- 修复思路：对这两个调用点单独传 `{ timeout: 120000 }`（与 `sendMessage` 180s / `confirmPlan` 600s 同模式）。

**F9｜同会话可并发确认多张计划卡** — P2
- 位置：`pages/enterprise/ChatCards.tsx:206-216`（确认按钮只看本卡 `confirmed`）+ `ChatPage.tsx:436`（`handleConfirmPlan` 未检查 `runningSessionIds`）
- 现象：同一会话内两张未过期计划卡可先后确认，产生两个并发 run/单 session 双任务，进度卡互相覆盖（`collapseProgressMessages` 按 assessment_id 折叠会闪烁）。
- 修复思路：`handleConfirmPlan` 入口增加 `if (runningSessionIds.has(sessionId)) return`，按钮态传入 `isRunning` 已有现成 prop。

### 3.3 前端低优先级

**F10｜Login 登录后二次读 localStorage 取角色（脆弱冗余）** — P3
- 位置：`pages/Login.tsx:35-38`
- 根因：`login()` 已返回/设置 AuthContext user，`onFinish` 又 `JSON.parse(localStorage.getItem('user'))`；解析失败时 fallback `/enterprise/dashboard`（对 expert/admin 也是错误路由，虽然 catch-all 会兜到聊天页）。
- 修复思路：让 `authService.login` 返回 user（已返回），直接 `roleRoute[res.user.role]`。

**F11｜SSE 401 未按认证失效处理** — P3
- 位置：`services/maclawRuntime.ts:631-634`（fetch 不走 axios 拦截器）
- 现象：token 过期时 SSE 报"事件流连接失败"，不清理 token/跳登录。
- 修复思路：fetch 401 时复用 `api.ts` 的清理 + 跳转逻辑（可抽 `handleUnauthorized()` 共用）。

**F12｜EngineManager 分类删除的翻页边界与并发** — P3
- 位置：`pages/expert/EngineManager.tsx:196-215`
- 现象：`loadAllTemplatesForSubTypes` 在 `total=0` 且每页恰好返回 pageSize 条时理论上死循环；`Promise.all` 全量并发删除无上限。
- 修复思路：加硬上限 offset/page 数；删除分批（如每批 20）。

**F13｜antd 废弃 API 混用** — P3（随 P4 一并处理）
- `EngineManager.tsx:331` `Card bodyStyle` → `styles={{ body: ... }}`
- `BillingPage.tsx:96` `Statistic valueStyle` → `styles={{ content: ... }}`（AdminDashboard 已是新写法）
- `AccountTenantManagement.tsx:113` `Drawer destroyOnClose` → `destroyOnHidden`（antd 6 验证）
- `Space direction` 与 `orientation` 混用 → 统一 `orientation`

**F14｜死代码与依赖清理** — P3
- `src/index.css`、`src/App.css`（未 import）；`package.json` `reactflow`（未 import）、`@types/react-router-dom@5`；`services/enterprise.ts`（空）；`services/expert.ts:40/:94`（指向 tombstone/不存在 API 的死方法）；`MaclawSkillManager.tsx:222`（引用不存在的 `values.top_n` 表单字段，恒为 10）。

**F15｜openSession 失败静默** — P3
- 位置：`ChatPage.tsx:386-388`（catch 空块）
- 现象：会话打开失败无提示，用户看到欢迎页误以为会话为空。
- 修复思路：catch 中 `message.error('打开会话失败，请重试')`。

**F16｜createSession/deleteSession 错误未处理** — P3
- 位置：`ChatPage.tsx:392-407`（createSession 无 catch，`submitPrompt` 链路上 `void` 吞掉）、`:410-424`（deleteSession 的 onClick 未处理拒绝）
- 修复思路：两处补 catch + message.error。

**F17｜报告下载失败提示写死"PDF"** — P3
- 位置：`ChatCards.tsx:170`（`message.error('PDF 下载失败...')`，但 downloadFormat 可能是 markdown/json）
- 修复思路：按 format 生成文案。

**F18｜用户角色下拉无二次确认** — P3
- 位置：`pages/admin/UserManagement.tsx:97-112`
- 现象：误触即改角色/启停（有 loadUsers 刷新但无确认）。
- 修复思路：Popconfirm 包裹（删除已有，可复用模式）。此项属于 UX 建议，可裁剪。

**F19｜Preview 展开状态跨预览残留** — P3
- 位置：`SampleManager.tsx:171`/`ComposedAttackManager.tsx:168`（打开新预览未重置 `expandedRows`）
- 修复思路：`handlePreview` 成功回调里 `setExpandedRows({})`。

### 3.4 Go 后端（轻量扫描结论）

**G1｜限流器 map 只增不清** — P3
- 位置：`internal/api/middleware/ratelimit.go:52-63`
- 现象：`counts[key]` 在 key 永不再来时保留空切片，长期运行缓慢内存增长（每用户/IP 一条）。
- 修复思路：清理后 `if len(valid)==0 { delete(rl.counts, key); return true }`。

**G2｜ConfirmPlan 乱码死赋值** — P3
- 位置：`internal/api/handler/maclaw_runtime.go` ConfirmPlan 内（`content = "纭鎵ц"` 后紧跟 `content = "确认执行"` 覆盖）
- 现象：源码中残留 GBK→UTF-8 mojibake 字符串；被下一行覆盖无功能影响，但属编码事故现场，建议清理为单一赋值。

**G3｜`r.Run()` 无显式 Server 超时** — 观察
- 位置：`cmd/server/main.go`（末尾 `r.Run(addr)`）
- 说明：当前无 WriteTimeout 是**必要的**（confirmed run 长请求 + SSE）；但也没有 `ReadHeaderTimeout`，存在 slowloris 加固空间。建议换显式 `http.Server{ReadHeaderTimeout: 30s}`（保持无 WriteTimeout）。非本次必改。

**G4｜CORS `Access-Control-Allow-Origin: *`** — 观察
- 位置：`cmd/server/main.go applyCORSHeaders`
- 说明：生产经 nginx 同源部署时建议收紧为具体 origin 或移除。非本次必改。

### 3.5 审计中确认"没有问题"的高风险点（存档）

- 会话隔离三件套（sending/running/loading by session id）：实现正确，见 1.4。
- `waitForAssistantReply` 有完整 try/catch/finally（`ChatPage.tsx:355-383`）。
- `confirmPlan` 三层超时匹配（axios 600s / BFF ctx 10min / nginx 650s）。
- `triggerBrowserDownload` URL 60s 后 revoke，无泄漏。
- `streamRunEvents` abort 路径（用户主动停止）正确。
- BFF SSE 转发把 `data:` 行以 `\n` 结尾重写（`maclaw_runtime.go` `copySanitizedRuntimeEventStream`），与前端 `\n\n` 边界解析一致。
- `prepareMessagesForDisplay` 的 plan_stale / 进度折叠 / 会话计划同步逻辑有 3 个测试文件覆盖，行为正确。

---

## 4. 建议迭代计划

> 每期结束必须通过：`cd evaluating_platform/frontend && npm run build`（tsc -b && vite build）+ `node scripts/test-*.mjs`；涉及后端时 `go test ./...`。

### P0 — 清理与准备（0.5 天）
- 内容：F14 全部（删死文件/死依赖/死方法，统一 Space orientation 之外的项留 P4）。
- 验收：build/lint 通过；`git diff --check` 干净；无任何行为变化。

### P1 — 高优 Bug 修复（1-2 天）✅ 用户已确认范围
- 内容：F1、F2、F3（聊天主链路健壮性）、F5（ask_user 兜底，约束项）。
- 说明：F4/F6-F9 中优项目用户已选择延后，不在本迭代实施。
- 验收：
  - 模拟 `getEvaluationJob` 500/429/断网：会话不永久卡运行中，出现可理解的失败卡，输入框恢复可用；
  - 离开聊天页后 Network 面板无 1s 间隔轮询残留、无跨页 toast；
  - SSE 正常结束/提前断开均触发 onDone，DevTools 无悬挂的 events 连接；
  - ask_user 缺 question 字段时不出现 JSON 原文（构造消息验证）。

### P2 — 白色主题地基（1-2 天）
- 内容：`main.tsx` ConfigProvider、`theme.css` token 重写（含删除 `!important` 按钮、科技风动画）、三门户 Sider/Header/Menu、`Login.tsx`。
- 验收：登录页 + 三门户骨架为白基调；无残留暗色大块（grep `#0a0c10|#0f1117|#141820|darkAlgorithm` 为 0 命中）；按钮 hover/active 三态正常；build 通过。

### P3 — 聊天工作台白色化（1-2 天）
- 内容：`ChatPage.tsx`、`ChatCards.tsx`（气泡、计划确认卡、进度卡、报告卡、欢迎面板、TypingIndicator）、markdown 代码块。
- 验收：
  - 消息气泡、卡片在白底可读；风险色/成功色满足 AA 对比度（抽样用对比度工具核验）；
  - 5 个既有 `*.test.ts` 文件零改动且类型检查通过；
  - 手工回归主链路：新建会话 → 快捷提问 → ask_user 追问 → plan_confirm 卡 → 确认执行 → 进度卡 → 报告卡 → 下载（行为与改造前一致）。

### P4 — 专家/管理页白色化（1-2 天）
- 内容：全部 expert/admin 页面变量与硬编码色替换；antd 废弃 API 顺手替换（F13，仅限本次触碰的文件内）。
- 验收：全部页面白基调无暗残留；antd 无 deprecated prop 控制台告警；build 通过。

### P5 — 回归收尾（0.5 天）
- 内容：全量回归 + 文档同步。延后项（F4/F6-F9/F10-F19/G1-G4）转入下迭代 backlog。
- 验收：build + node 测试脚本 + `go test ./...` + 主链路人工过屏 + `git diff --check`；更新 `PROJECT_GUIDE.md` / `AGENTS.md` / `docs/architecture/maclaw-current-state.md` 中受 UI 事实影响的描述（若有）。

### 延后 backlog（用户已确认不在本次迭代）
- 中优：F4（计费分页）、F6（validateFields）、F7（admin 静默失败）、F8（30s 超时）、F9（并发确认）
- 低优：F10-F19
- Go：G1（限流 map）、G2（乱码死赋值）、G3/G4（观察项）

### 明确不做（本次迭代）
- 状态管理库引入/重构（现有 useReducer-free 方案可维护）；
- 组件库更换、CSS-in-JS、tailwind 引入；
- 后端 API/数据层任何变更（G3/G4 仅记录）；
- 报告 PDF 模板（`redteam_report_pdf_layout_v2` 属后端渲染，不动）；
- `*-skill/`、`CC-BOS/` 目录（任务边界外）。

---

## 5. 附录：文件 × 改动类型矩阵（速查）

```
入口/主题
  main.tsx                    [主题重构]
  styles/theme.css            [主题重构]
  index.css / App.css         [删除]
  package.json                [依赖清理]

企业门户
  Login.tsx                   [重样式]
  EnterprisePortal.tsx        [重样式]
  ChatPage.tsx                [重样式 + F1 F2 F9 F15 F16]
  ChatCards.tsx               [重样式 + F17]
  BillingPage.tsx             [重样式 + F4 + F13]
  Settings.tsx                [轻样式]

专家门户
  ExpertPortal.tsx            [重样式]
  SampleManager.tsx           [轻样式 + F19]
  EngineManager.tsx           [轻样式 + F12 + F13]
  ComposedAttackManager.tsx   [轻样式 + F19]
  MaclawSkillManager.tsx      [轻样式 + F6 + F14]
  MaclawMCPServerManager.tsx  [轻样式 + F6]

管理门户
  AdminPortal.tsx             [重样式]
  AdminDashboard.tsx          [重样式]
  UserManagement.tsx          [轻样式 + F18(可选)]
  AccountTenantManagement.tsx [轻样式 + F7 + F13]
  ResourceGovernance.tsx      [轻样式 + F7]
  JobGovernance.tsx           [轻样式 + F7]
  MaclawModelConfig.tsx       [轻样式 + F6]
  MaclawHubManagement.tsx     [轻样式 + F6]
  SystemHealth.tsx            [轻样式]

服务层（仅死代码与超时）
  api.ts                      [F8 观察，超时改法见方案]
  maclawRuntime.ts            [F3 F5 F11]
  expert.ts / enterprise.ts   [删除死方法/空模块]

不改
  *.test.ts (5 个) / chatDisplay.ts / chatMarkdown.ts / jobProgressMessages.ts
  maclawRuntimePlan.ts / runtimeRunPolicy.ts / ProtectedRoute.tsx / AuthContext.tsx
  nginx.conf / Dockerfile / 后端全部业务逻辑
```