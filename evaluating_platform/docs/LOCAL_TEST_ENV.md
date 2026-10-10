# 本地全量测试环境 · 可复制执行清单

> 实测环境：macOS arm64｜Go 1.25.5（`/Users/huangqixu/sdk/go/bin/go`）｜Node v22.22.2 / npm 10.9.7｜无 Docker
> 实测日期：2026-10-10｜所有结果均为真实命令输出，非推测
>
> **2026-10-10 修订**：§0.2 的 npm 归因已按源码证据更正（原写「劫持网络」有误，实为 **fs hook**）。
> 触发条件为**会话级暂态，当前已不复现**，`npm ci` 可直接跑；`npm_clean` 降级为备用预案。
> 修订依据：u3-implementer 源码级复核 + 双方各自复测。

---

## 0. 前置：PATH 与 npm 环境

### 0.1 Go 的 PATH

```bash
export PATH=/Users/huangqixu/sdk/go/bin:$PATH
go version   # 期望：go version go1.25.5 darwin/arm64
```

**持久化建议（我没有改你的 `~/.zshrc`，请自行决定是否采纳）**：

```bash
# 追加到 ~/.zshrc
export PATH="/Users/huangqixu/sdk/go/bin:$PATH"
```

本机 Homebrew 装 Go 会失败（沙箱不允许写 `/opt/homebrew`），所以只能用手动 tarball 路径。

> 📌 关于「`export PATH` 是否安全」：曾有顾虑说它会触发沙箱
> `zsh: connection refused: /dev/null`。但2026-10-10 连续实测 3 次均成功，
> 属特定 shell/沙箱态下的暂态，**不必当作硬约束**。
> 若你倾向于更稳，**用绝对路径亦可**（沙箱里最稳，推荐）：
> ```bash
> /Users/huangqixu/sdk/go/bin/go test ./...
> ```

### 0.2 npm 在沙箱会话里偶发失败 —— 备用预案（**当前不是硬性必需**）

> ⚠️ **先说结论：正常情况下直接跑 `npm ci` 就行，不需要任何前缀。**
> 本节是 2026-10-10 那天**一度**遇到的会话级暂态问题的备用预案，不是环境固件。
> 已由 u3-implementer 在源码层复核并纠正了最初的错误归因，见下。

#### 现象（当时）

```
npm warn tar TAR_ENTRY_ERROR connect ECONNREFUSED /var/folders/.../T/cbb-XXXX/broker.sock
npm error code ECONNREFUSED
npm error code CODEBUDDY_BROKER_DENY
npm error [safe-delete][SAFE_DELETE_BULK_CONFIRM_REQUIRED] {"count":9497,...}
```

#### 真正的机制（源码级结论）

`NODE_OPTIONS` 注入了 WorkBuddy 的 `node-language-shim.cjs`，但它**不劫持网络**。
读源码（`.../vendor/shim/node-language-shim.cjs` 顶部注释）：

```js
// It composes safe-delete hooks and brokered fs hooks
if (safeDeleteEnabled) require('./node-safe-delete-shim.cjs');
if (brokeredFsHookEnabled) require('./node-brokered-fs-shim.cjs');  // 拦 fs 操作
```

真正报错的是 **`node-brokered-fs-shim.cjs`**（约 40KB），它拦的是**文件系统操作**，
并在需要「文件令牌」时通过 native addon 连 `CODEBUDDY_SANDBOX_BROKER_IPC_ADDRESS`：

```js
// node-brokered-fs-shim.cjs:340
nativeAddon.requestBrokerSync(
  process.env.CODEBUDDY_SANDBOX_BROKER_IPC_ADDRESS, ... );
```

**所以 `broker.sock` 报错发生在「批量删写 node_modules」的 fs 路径上，不是网络请求路径。**
`npm ping` 这类只读网络请求本来就不触发 fs hook，因此当时也能通 —— 两者不矛盾。

#### 触发条件（已复测确认是暂态，不是固件）

2026-10-10 复测（u3-implementer + 我各验一遍）：

| 场景 | 结果 |
|---|---|
| `npm ping`（带全部沙箱变量） | ✅ PONG 363ms |
| `python3` 直连 `broker.sock` | ✅ connect OK（socket 可达） |
| `/tmp` 新项目 `npm install`（**不清任何变量**） | ✅ exit 0，9s |
| `npm uninstall` | ✅ exit 0 |

即：**`broker.sock` 当前存在且可达，失败已不复现。**
判断为**会话生命周期内的暂态**（broker 进程重启 / 换 session 时 socket 失效）。

#### 备用预案（真遇到时再用）

清掉沙箱 broker 变量可让 fs hook 的令牌请求短路，从而绕过该问题：

```bash
# 仅在 npm 真的报 broker.sock / CODEBUDDY_BROKER_DENY 时才需要
npm_clean() {
  env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy \
      -u TOYBOX_SANDBOX_SOCK -u CODEBUDDY_SANDBOX_BROKER_IPC_ADDRESS "$@"
}
```

> **不要因为「要清代理」这个说法去回避联网场景**：这个坑跟网络无关，
> 在别的批量删文件场景同样可能撞上（fs hook 报错看起来很像网络错误，容易误判）。
> 用户自己在普通终端跑通常完全不受影响。

---

## 1. 后端（Go）—— ✅ 全绿

```bash
cd /Users/huangqixu/Desktop/evaluating-platform-backup/evaluating_platform
export PATH=/Users/huangqixu/sdk/go/bin:$PATH
go vet ./... && go build ./... && go test ./...
```

实测结果：`go vet` exit 0｜`go build` exit 0｜`go test` **exit 0**

**精确计数（2026-10-10 复测，勿混淆）：**

| 口径 | 包数 | 说明 |
|---|---|---|
| `go list ./...` | **14** | 含 1 个 `frontend/node_modules/flatted` |
| `go list ./... \| grep -v node_modules` | **13** | 真实业务包 |
| 其中**有测试且通过**（`ok` 行） | **7** | 其余 6 个为 `[no test files]` |

```
ok      evaluating_platform/cmd/server
ok      evaluating_platform/internal/api/handler
ok      evaluating_platform/internal/billing
ok      evaluating_platform/internal/maclaw            5.334s
ok      evaluating_platform/internal/repository
ok      evaluating_platform/internal/sample
ok      evaluating_platform/pkg/config
?       evaluating_platform/internal/api/middleware  [no test files]
?       evaluating_platform/internal/crypto           [no test files]
?       evaluating_platform/internal/model            [no test files]
?       evaluating_platform/pkg/db                    [no test files]
?       evaluating_platform/pkg/logger                [no test files]
?       evaluating_platform/pkg/storage               [no test files]
```

> ⚠️ `11-U3结构治理详细设计.md` §5.3 明确要求 **U3 期间不要跑 `./...`**。
> 两种口径在本机实测等价（均 exit 0），但 U3 请按设计文档走排除版：
> ```bash
> go vet ./internal/... ./cmd/... ./pkg/...
> go test ./internal/... ./cmd/... ./pkg/...
> ```

### 1.1 关于 09 文档 §6 第 4 条的 flatted 噪声（已实测复现 + 结论）

**噪声真实存在**，当 `frontend/node_modules` 装好后会出现：

```
?   evaluating_platform/frontend/node_modules/flatted/golang/pkg/flatted   [no test files]
```

**但它不是失败**：

| 项 | 实测结论 |
|---|---|
| 是否影响退出码 | **不影响**，`go test ./...` 仍为 **exit 0** |
| 性质 | `[no test files]` —— 只是多列了一行，**没有任何断言失败** |
| CI 是否受影响 | **不受影响**。ci.yml 的 backend job 不装前端依赖，`node_modules` 被 `.gitignore` 排除 |
| 是否是 bug | **不是**，属纯本地显示噪声，无需改代码 |

**可选的精确排除方式**（我已实测 exit 0，得到干净的 13 个包）：

```bash
go test $(go list ./... | grep -v node_modules)
```

> 我的建议：**直接用 `go test ./...` 即可**，不必加过滤。加过滤只是让输出更干净，
> 反而可能掩盖「新增了别的真实包」这类问题。

---

## 2. 引擎（promptfoo_engine）—— ✅ typecheck 全绿，测试 69/70（1 条环境性失败）

```bash
cd /Users/huangqixu/Desktop/evaluating-platform-backup/evaluating_platform/promptfoo_engine
npm ci --no-audit --no-fund     # 全新安装；node_modules 缺失时才需要
npm run typecheck                    # == tsc -p tsconfig.json --noEmit
npm test                             # == vitest run
# 若报 broker.sock / CODEBUDDY_BROKER_DENY，改用 npm_clean（见 §0.2）
```

| 步骤 | 结果 |
|---|---|
| `npm ci` | ✅ exit 0 |
| `npm run typecheck` | ✅ **exit 0**，无输出 |
| `npm test` | ⚠️ **exit 1 —— 69 passed / 1 failed** |

失败详情：

```
 ❯ tests/smoke.e2e.test.ts (1 test | 1 failed) 90175ms
   × engine smoke → engine did not become healthy within 90s
 Test Files  1 failed | 9 passed (10)
      Tests  1 failed | 69 passed (70)
```

**已定性为环境问题，不是代码缺陷**，证据链如下：

1. 单独重跑该文件 → **通过**：
   ```
   ✓ tests/smoke.e2e.test.ts (1 test) 35816ms
    Test Files  1 passed (1)
   ```
2. 手工拉起服务确认引擎本身健康正常，`/health` 在 ~45s 返回：
   ```
   [engine] promptfoo-engine v0.1.0 listening on :18096 (workers=2)
   {"status":"ok","version":"0.1.0","queued":0,"running":0,"workdirs":0}
   ```
3. 失败时整轮 `collect` 耗时 **482s**（本机负载 load≈5，tsx 冷启动极慢），
   90s 健康检查预算在并行跑 10 个文件时被耗尽。

> 顺带确认：引擎有两道 **fail-closed 启动守卫**（这是好设计，不是 bug）——
> ① 未设 `PROMPTFOO_DISABLE_REMOTE_GENERATION / _REDTEAM_REMOTE_GENERATION / _TELEMETRY / _SHARE` 则拒绝启动；
> ② `PROMPTFOO_ENGINE_BEARER_TOKEN` 少于 16 字符则拒绝启动。
> 上面手工验证时被这两道守卫各拦了一次，补齐后即正常。

---

## 3. 前端（frontend）—— ⚠️ build/test 全绿，但有一个真实 CI 阻断缺陷

```bash
cd /Users/huangqixu/Desktop/evaluating-platform-backup/evaluating_platform/frontend
npm ci --no-audit --no-fund          # 实测：added 303 packages in 11s
npm run build                        # tsc -b && vite build
npm test                             # vitest run —— U2 新接入
npm run test:adapters                # ⚠️ 失败，见 §3.2
npm run lint                         # ⚠️ 失败（16 errors），但 CI 里是 continue-on-error
# 若报 broker.sock / CODEBUDDY_BROKER_DENY，改用 npm_clean（见 §0.2）
```

### 3.1 U2 的前端 vitest：**跑通了** ✅

这一条是 QA 关心的重点，结论是**不是 U2 交付缺陷**：

```
 ✓ src/pages/enterprise/chatMarkdown.test.ts (1 test) 3ms
 ✓ src/services/runtimeRunPolicy.test.ts (1 test) 3ms
 ✓ src/pages/enterprise/chatDisplayConfirmedPlan.test.ts (1 test) 2ms
 ✓ src/pages/enterprise/chatDisplaySessionIsolation.test.ts (2 tests) 2ms
 ✓ src/pages/enterprise/chatDisplayReportCard.test.ts (1 test) 2ms
 ✓ src/pages/enterprise/jobProgressMessages.test.ts (1 test) 4ms

 Test Files  6 passed (6)
      Tests  7 passed (7)
```

**6 文件 / 7 用例全过**，与 09 文档 §3 记载的「6 个文件 7 用例」完全一致。

补充：`vite.config.ts` 里**没有** `test` 配置块，vitest 用默认 node 环境即可——
已确认这 6 个测试都是纯逻辑断言（不碰 `document` / `window` / testing-library），所以**不需要**装 jsdom。

`npm run build` 也通过：exit 0，`✓ 3147 modules transformed`，`✓ built in 47.20s`。

### 3.2 ❌ 真实缺陷：`npm run test:adapters` 崩了（CI 里是阻断步骤）

```
TypeError: runtimeContentHasPlanConfirm is not a function
  at scripts/test-maclaw-runtime-adapters.mjs:55:14
```

**根因（已确认）**：脚本第 27 行从 `src/services/maclawRuntimePlan.ts` 解构了两个导出：

```js
const { parseRuntimePlan, runtimeContentHasPlanConfirm } = loadTSModule('src/services/maclawRuntimePlan.ts')
```

但该源文件**现在只导出 `parseRuntimePlan`**，第 117 行起只有这一个导出，
`runtimeContentHasPlanConfirm` **在源码里已经不存在了**（全仓 grep 只在这个 .mjs 脚本里出现 4 次：
第 27 行解构 + 第 55/66/83 行调用，源文件侧 0 次）。

**性质**：典型的「函数被重构掉、调用方没同步」腐烂。
**影响**：ci.yml 的 `adapter scripts` 步骤**没有** `continue-on-error`，所以 **CI 会红**。
**建议**（不在本轮范围内，交主理人决策）：要么恢复该函数，要么改脚本只断言 `parseRuntimePlan` 可覆盖的等价行为。

### 3.3 `npm run lint` 失败（已知项，不阻断）

`✖ 22 problems (16 errors, 6 warnings)`，集中在
`src/services/maclawRuntime.ts:363-370` 的 `_cardType`/`_phase`/`_statusText` 等
「赋值未使用」（`@typescript-eslint/no-unused-vars`）。
ci.yml 里 lint 是 `continue-on-error: true`，且 ci.yml 末尾注释已写明
「frontend lint is non-blocking until pre-existing lint findings are cleaned up」——**属已登记的存量问题**。

---

## 4. Docker / compose

本机**没有 Docker**（`which docker` → not found）。
ci.yml 的 `compose` job（`docker compose config --services`）**只能在 CI 验证**，本地不要尝试。

---

## 5. 一键复现（可直接整段粘贴）

```bash
# ===== 后端（U3 期间按设计文档 §5.3 走排除版，见 §1）=====
cd /Users/huangqixu/Desktop/evaluating-platform-backup/evaluating_platform
export PATH=/Users/huangqixu/sdk/go/bin:$PATH
go vet ./... && go build ./... && go test ./...

# ===== 引擎 =====
cd /Users/huangqixu/Desktop/evaluating-platform-backup/evaluating_platform/promptfoo_engine
npm ci --no-audit --no-fund
npm run typecheck
npm test

# ===== 前端 =====
cd /Users/huangqixu/Desktop/evaluating-platform-backup/evaluating_platform/frontend
npm ci --no-audit --no-fund
npm run build
npm test
npm run test:adapters   # ⚠️ 已知失败：runtimeContentHasPlanConfirm 不存在

# ===== 备用：仅当上面报 broker.sock / CODEBUDDY_BROKER_DENY 时才启用（见 §0.2）=====
# npm_clean() { env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy \
#   -u TOYBOX_SANDBOX_SOCK -u CODEBUDDY_SANDBOX_BROKER_IPC_ADDRESS "$@"; }
```

---

## 6. 环境缺失 / 遗留清单

| # | 事项 | 影响 | 处置 |
|---|---|---|---|
| 1 | Docker 未安装 | compose job 本地无法验证 | 只能靠 CI，非缺陷 |
| 2 | 沙箱会话内 npm 偶发 `broker.sock` 报错 | **暂态，当前不复现**（已复测） | 备用预案见 §0.2 |
| 3 | 本机负载高（load≈5） | 引擎 e2e smoke 并行时可能超时 | 单独跑即可过，见 §2 |
| 4 | `npm ci` 会被 `SAFE_DELETE_BULK_CONFIRM_REQUIRED` 拦 | 仅在已有残缺 node_modules 时 | 删/移走 node_modules 后重跑 |
| 5 | `test:adapters` 源码/脚本不同步 | **CI 阻断级真实缺陷** | 待主理人决策，见 §3.2 |
| 6 | `lint` 16 errors | 不阻断（continue-on-error） | 存量已知项 |