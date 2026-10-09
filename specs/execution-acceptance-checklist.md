# UI 白色主题改造 + Bug 修复 — 执行验收清单

> 目标：完整执行 `specs/ui-white-modern-redesign-and-bugfix-plan.md` 中用户已确认的迭代计划（P0-P5）。
> 状态：**全部完成**（2026-09-15）。证据见各项备注。

## Acceptance Criteria

### P0 — 死代码清理（F14）✅
- [x] `frontend/src/index.css`、`frontend/src/App.css` 已删除（git status: D；删除前 grep 确认 0 import 引用）
- [x] `frontend/src/services/enterprise.ts` 已删除（git status: D；grep 确认 `enterpriseService` 0 引用）
- [x] `services/expert.ts` 中 `getCategories` / `listEvalPackages` 死方法已删除（最终 grep 0 命中）
- [x] `package.json` 已移除 `reactflow` 与 `@types/react-router-dom`；`npm install` 成功（269 包，lockfile 已同步）
- [x] `MaclawSkillManager.tsx` 不再引用 `values.top_n`（改为固定 `top_n: 10`，grep 0 命中）

### P1 — 高优 Bug 修复（F1/F2/F3/F5）✅
- [x] F1: `waitForEvaluationJob` 循环体 try/catch，连续 5 次失败才终止并渲染失败进度卡（`buildEvaluationJobProgressMessage` phase='failed'）+ `markSessionRunning(false)`；已读回代码复核
- [x] F2: `mountedRef` 已添加；卸载 cleanup 置 false；两处轮询循环与所有全局 toast 均有 mountedRef 守卫
- [x] F3: `streamRunEvents` terminal 后 `void reader.cancel().catch(() => {})`；`done` 且未收到 terminal 时调用 `onDone()`（`if (!stopped) onDone()`）
- [x] F5: `toChatMessage` ask_user 分支 question/message/content 均缺失时 content 兜底为"需要补充更多信息，请继续描述你的需求。"

### P2 — 白色主题地基 ✅
- [x] `main.tsx` ConfigProvider：`theme.defaultAlgorithm`、主色 `#2563eb`、白色 Layout/Menu/Table/Card token（已读回复核）
- [x] `theme.css` `:root` 全量重映射为白主题；`.ant-btn-primary !important` 块、tech-grid-bg/scan-line/data-flow/pulse-glow/status-running/tech-card（均为零引用死类）已删除；滚动条浅色化（grep 确认 0 残留）
- [x] 三门户 Menu 移除 `theme="dark"`（全局 grep `theme="dark"` 0 命中）；头像 rgba 换新主题色
- [x] Login.tsx 浅色渐变背景 + 白卡片 + 主色图标块
- [x] 全局 grep `#0a0c10|#0f1117|#141820|#1a2030|#1f2740|darkAlgorithm|theme="dark"` = 0 命中

### P3 — 聊天工作台白色化 ✅
- [x] ChatPage（会话选中态/Loading）、ChatCards（RISK_COLORS 换 AA 色 `#dc2626/#ea580c/#d97706/#16a34a`、`#69db7c`→`#16a34a`、`#4d96ff`→主色变量、代码块 `#f1f5f9` 浅底、进度条 trailColor `#eef2f7`）、BillingPage、Settings 全部完成
- [x] 5 个既有 `*.test.ts` 文件零改动（git status 变更列表不含任何 test 文件）

### P4 — 专家/管理页白色化 ✅
- [x] AdminDashboard 卡片色（`#2563eb/#16a34a/#7c3aed`）、UserManagement 余额色替换；专家页全部使用 CSS 变量无需改色（除已改的 ExpertPortal）
- [x] F13 顺手替换：`valueStyle`→`styles.content`（BillingPage）、`bodyStyle`→`styles.body`（EngineManager）、`destroyOnClose`→`destroyOnHidden`（AccountTenantManagement）、`Space direction`→`orientation`（5 个专家文件 11 处）；全局 grep 0 残留

### P5 — 回归收尾 ✅（一项环境限制，见备注）
- [x] `npm run build` 通过（tsc -b && vite build，P0/P1/P2/P4 四轮均通过）
- [x] `node scripts/test-maclaw-runtime-adapters.mjs` 通过（"maclaw runtime adapter tests passed"）
- [~] `go test ./...` **未执行——本机（备份机）无 Go 工具链**。替代证据：`git status --porcelain` 证明 0 个 `.go` 文件变更（本次迭代纯前端），后端代码与基线完全一致，无回归对象
- [x] `git diff --check` 干净（无空白错误）
- [x] 文档同步：经检查 PROJECT_GUIDE.md / AGENTS.md / maclaw-current-state.md 均不含 UI 主题/样式事实描述，本次改动不触发文档同步条件（未影响 BFF API/治理能力/安全边界等九类事实）

### 边界约束 ✅
- [x] 不触碰 API/数据层、安全边界、旧 API tombstone、后端业务逻辑、`*-skill/`、`CC-BOS/`（git status 证明仅前端 + specs/ 文档）
- [x] 保护文件零改动：5 个 `*.test.ts`、`chatDisplay.ts`、`chatMarkdown.ts`、`jobProgressMessages.ts`、`maclawRuntimePlan.ts`、`runtimeRunPolicy.ts`、`ProtectedRoute.tsx`、`AuthContext.tsx`、`nginx.conf`、`Dockerfile` 均不在变更列表

### 变更清单（git status 实证）
- 修改 17 tsx + 3 ts（expert.ts / maclawRuntime.ts / services 其一）+ 3 css + 2 json
- 删除 App.css / index.css / enterprise.ts
- 新增 specs/（设计文档 + 本清单）

### 遗留 backlog（用户已确认延后）
F4、F6-F19、G1-G4 — 详见 `specs/ui-white-modern-redesign-and-bugfix-plan.md` 第 3、4 章
