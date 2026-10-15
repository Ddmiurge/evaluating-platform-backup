#!/usr/bin/env bash
# 22-部署与验证-阶段三.sh — 在 ai02 上部署 P3-1 ~ P3-5 并逐项验证 P3-6
#
# 用法（在服务器上，任意目录）：
#   cd /da2/docker-files/evaluating-platform-backup/evaluating_platform
#   ENTERPRISE_EMAIL='enterprise@demo.com' ENTERPRISE_PASSWORD='...' \
#     bash audit-2026-10-09/22-部署与验证-阶段三.sh 2>&1 | tee /tmp/p3-verify-$(date +%F).log
#
# 可选环境变量：
#   BFF=http://localhost:8088       # backend 直连端口（compose 里 8088:8080）
#   DEPLOY_DIR=...                  # 部署目录，默认见下
#   EXPECT_COMMIT=...               # 你推送并部署的那个 commit（留空则只打印不判）
#   SKIP_DEPLOY=1                   # 只跑验证，不做 git/构建/重启
#   MCP_SECRET=...                  # 判据 10 需要；等于 .env 的 MACLAW_REDTEAM_MCP_SECRET
#
# 判据来源：`21-阶段三实施计划.md` §11 的 10 条。编号与本脚本的 hr 标题一一对应。
#
# 泄漏纪律：本脚本只打印状态、计数、错误码与**已脱敏的** error_brief。
# 任何一步的输出若要贴回来，先确认里面没有 prompt / 响应正文 / 凭据 / 本地路径。
#
# 本脚本**刻意不做**的事：不改被测目标配置（target 凭据是 write-only，改了无法还原）。

set -uo pipefail

BFF=${BFF:-http://localhost:8088}
DEPLOY_DIR=${DEPLOY_DIR:-/da2/docker-files/evaluating-platform-backup/evaluating_platform}
EXPECT_COMMIT=${EXPECT_COMMIT:-}
SKIP_DEPLOY=${SKIP_DEPLOY:-0}
MCP_SECRET=${MCP_SECRET:-}

PASS=0; FAIL=0; WARN=0
ok()   { printf '✅ %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '❌ %s\n' "$*"; FAIL=$((FAIL+1)); }
warn() { printf '⚠️  %s\n' "$*"; WARN=$((WARN+1)); }
hr()   { printf '\n──────── %s ────────\n' "$*"; }

# check <描述> <期望> <实际>
check() {
  if [ "$2" = "$3" ]; then ok "$1（$3）"; else bad "$1：期望 [$2]，实际 [$3]"; fi
}

# json_field <json> <key> —— jq 优先，其次 python3，最后 grep（grep 只够取简单字符串）
json_field() {
  local body=$1 key=$2
  if command -v jq >/dev/null 2>&1; then
    printf '%s' "$body" | jq -r --arg k "$key" 'if type=="object" then (.[$k] // "") else "" end' 2>/dev/null
  elif command -v python3 >/dev/null 2>&1; then
    printf '%s' "$body" | python3 -c "import json,sys
try:
    d=json.load(sys.stdin)
    v=d.get('$key','') if isinstance(d,dict) else ''
    print('' if v is None else v)
except Exception:
    print('')" 2>/dev/null
  else
    printf '%s' "$body" | grep -o "\"$key\":[^,}]*" | head -1 | sed 's/.*: *"\?//; s/"\?$//'
  fi
}

# grep_field <json> <正则> —— 直接抓原始 JSON 片段（数组等 json_field 取不到的字段用）
grep_field() { printf '%s' "$1" | tr -d '\n' | grep -o "$2" | head -1; }

psql_run() { docker compose exec -T postgres psql -U "${PG_USER:-postgres}" -d "${PG_DB:-evaluating_platform}" -At -c "$1" 2>/dev/null; }

# ─────────────────────────────────────────────────────────────────────────────
hr "0 · 部署"
# ─────────────────────────────────────────────────────────────────────────────

if [ "$SKIP_DEPLOY" = "1" ]; then
  warn "SKIP_DEPLOY=1，跳过 git/构建/重启"
else
  cd "$DEPLOY_DIR" 2>/dev/null || { bad "部署目录不存在：$DEPLOY_DIR"; exit 1; }

  git fetch -q origin 2>&1 || bad "git fetch 失败（检查网络/凭据）"
  git checkout -q main 2>&1 || bad "切到 main 失败"
  git pull -q --ff-only 2>&1 || bad "git pull --ff-only 失败（本地可能有改动）"

  head_commit=$(git rev-parse --short HEAD)
  if [ -n "$EXPECT_COMMIT" ]; then
    check "代码版本" "$EXPECT_COMMIT" "$head_commit"
    if [ "$head_commit" != "$EXPECT_COMMIT" ]; then
      bad "版本不符，先解决再继续；后续验证结果不能归因到本批改动"
    fi
  else
    warn "未提供 EXPECT_COMMIT，只打印当前版本：$head_commit"
  fi

  # P3-3 删了引擎文件、改了 env 契约；P3-2/P3-1 改了前端选择器与默认值；
  # backend 的护栏与 MCP 工具描述也变了 —— 三个镜像都要重建。
  echo "→ 重建镜像（promptfoo-engine / backend / frontend）…"
  docker compose build promptfoo-engine backend frontend 2>&1 | tail -5 || bad "镜像构建失败"
  echo "→ 启动…"
  docker compose up -d 2>&1 | tail -5 || bad "compose up 失败"
fi

docker compose ps 2>&1 | sed -n '1,12p'

echo "→ 等引擎起来（20s）…"
sleep 20
engine_log=$(docker compose logs --tail=200 promptfoo-engine 2>&1 || true)
printf '%s\n' "$engine_log" | grep -E "\[engine\]" | tail -8

# ─────────────────────────────────────────────────────────────────────────────
hr "1 · 判据 1/2/3 —— 引擎启动与 env 契约"
# ─────────────────────────────────────────────────────────────────────────────

# 判据 1：能起来，且 workers=1（P3-3 之后 worker 恒为 1，由构造保证）
if printf '%s' "$engine_log" | grep -q "listening on :8090 (workers=1)"; then
  ok "判据1 引擎以 workers=1 启动（串行化生效）"
else
  bad "判据1 引擎未以 workers=1 启动（或根本没起来）"
fi
if printf '%s' "$engine_log" | grep -qiE "\[engine\] fatal|Failed to start"; then
  bad "判据1 引擎启动报错，见上面的日志"
else
  ok "判据1 引擎无 fatal"
fi
if printf '%s' "$engine_log" | grep -q "PROMPTFOO_ENGINE_NATIVE_GENERATION"; then
  warn "判据1/9 日志里出现了 PROMPTFOO_ENGINE_NATIVE_GENERATION —— P3-3 之后它应当不再被任何代码读取"
fi

# 判据 2：.env 里配了 >1 个 worker 时，应有一行「被忽略」说明
if printf '%s' "$engine_log" | grep -q "serialized by construction: PROMPTFOO_ENGINE_WORKERS="; then
  ok "判据2 有「配置值被忽略，恒跑 1 个 worker」的说明行"
else
  warn "判据2 无该说明行：若 .env 的 PROMPTFOO_ENGINE_WORKERS 本来就是 1 或未设置则正常"
fi

# 判据 3：PROMPTFOO_CONFIG_DIR 里若有 auth.json 必须拒绝启动（Promptfoo Cloud 登录是危害）
cfg_dir=$(docker exec ep_promptfoo_engine printenv PROMPTFOO_CONFIG_DIR 2>/dev/null | tr -d '\r')
if [ -z "${cfg_dir:-}" ]; then
  ok "判据3 容器内未设置 PROMPTFOO_CONFIG_DIR（无需检查 auth.json）"
else
  if docker exec ep_promptfoo_engine sh -c "test -f '$cfg_dir/auth.json'" 2>/dev/null; then
    bad "判据3 $cfg_dir 里存在 auth.json，但引擎仍然起来了 —— fail-closed 没生效"
  else
    ok "判据3 $cfg_dir 内无 auth.json（符合启动条件）"
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
hr "2 · 登录并确认目标已配置"
# ─────────────────────────────────────────────────────────────────────────────

if [ -z "${ENTERPRISE_EMAIL:-}" ] || [ -z "${ENTERPRISE_PASSWORD:-}" ]; then
  bad "未提供 ENTERPRISE_EMAIL / ENTERPRISE_PASSWORD，无法继续"
  echo "   用法：ENTERPRISE_EMAIL='...' ENTERPRISE_PASSWORD='...' bash $0"
  exit 1
fi

login=$(curl -sS -X POST "$BFF/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$ENTERPRISE_EMAIL\",\"password\":\"$ENTERPRISE_PASSWORD\"}" 2>/dev/null)
JWT=$(json_field "$login" token)
[ -n "$JWT" ] && ok "登录成功（企业账号）" || { bad "登录失败：$(printf '%s' "$login" | head -c 120)"; exit 1; }

auth=(-H "Authorization: Bearer $JWT")

targets=$(curl -sS "${auth[@]}" "$BFF/api/v1/maclaw/evaluation/targets" 2>/dev/null | head -c 2000)
if printf '%s' "$targets" | grep -qiE '"enabled":\s*true|"id"'; then
  ok "被测目标已配置"
else
  warn "未读到已启用的被测目标：请在企业门户「被测对象连接」里配置后再跑 §3 之后"
fi

create_run() { # create_run <json-body>
  curl -sS -X POST "$BFF/api/v1/maclaw/engine/runs" "${auth[@]}" \
    -H 'Content-Type: application/json' -d "$1" 2>/dev/null
}

wait_run() { # wait_run <run-id> [最大秒数]
  local id=$1 limit=${2:-600} waited=0 body phase
  while [ "$waited" -lt "$limit" ]; do
    body=$(curl -sS "${auth[@]}" "$BFF/api/v1/maclaw/engine/runs/$id" 2>/dev/null)
    phase=$(json_field "$body" phase)
    case "$phase" in succeeded|failed|canceled) printf '%s' "$body"; return 0 ;; esac
    sleep 5; waited=$((waited+5))
  done
  printf '%s' "$body"
  return 1
}

# ─────────────────────────────────────────────────────────────────────────────
hr "3 · 判据 4 —— 被下线的策略提交即拒，且不产生 engine_runs 行"
# ─────────────────────────────────────────────────────────────────────────────

# 逐个测 9 个已下线的 id。判据是：409 + strategy_id_invalid + 带替代引导。
for sid in direct role-play encoding multi-turn assumed-knowledge few-shot many-shot translate otherEncodings; do
  before=$(psql_run "SELECT count(*) FROM engine_runs;")
  rejected=$(create_run "{\"purpose\":\"P3-6 retired $sid\",\"plugins\":[{\"id\":\"contracts\"}],\"strategies\":[{\"id\":\"$sid\"}],\"num_tests\":3}")
  code=$(json_field "$rejected" code)
  after=$(psql_run "SELECT count(*) FROM engine_runs;")

  if [ "$code" = "strategy_id_invalid" ]; then
    ok "判据4 $sid → strategy_id_invalid"
  else
    bad "判据4 $sid：期望 strategy_id_invalid，实际 [${code:-<无>}]"
  fi
  if printf '%s' "$rejected" | grep -q '"guidance"'; then
    ok "判据4 $sid 带替代引导"
  else
    bad "判据4 $sid 的响应里没有 guidance（用户无从知道该改选什么）"
  fi
  if [ -z "$before" ] || [ -z "$after" ]; then
    warn "判据4 $sid 读不到 engine_runs 行数（psql 不可用？）—— 无法判定是否留痕"
  else
    check "判据4 $sid 未产生 engine_runs 行" "$before" "$after"
  fi
done

# ─────────────────────────────────────────────────────────────────────────────
hr "4 · 判据 5a/5b —— 空策略合法；选中的策略真的被施加"
# ─────────────────────────────────────────────────────────────────────────────

# 5a：向导默认值现在是**空**（P3-1/P3-2 改的）。此时 strategy_stats 为空是**正确**的。
echo "→ 5a. 不传 strategies（等于向导默认）"
created=$(create_run '{"purpose":"P3-6 default empty strategies","plugins":[{"id":"contracts"}],"num_tests":3}')
id=$(json_field "$created" id)
if [ -z "$id" ]; then
  bad "判据5a 空策略被拒了：$(json_field "$created" code) / $(printf '%s' "$created" | head -c 200)"
else
  ok "判据5a 空策略被接受：$id"
  final=$(wait_run "$id" 600)
  check "判据5a 空策略 run 终态" "succeeded" "$(json_field "$final" phase)"
  stats=$(grep_field "$final" '"strategy_stats":\[[^]]*\]')
  if [ -z "$stats" ]; then
    warn "判据5a 响应里没有 strategy_stats（字段缺失 ≠ 空数组，值得看一眼）"
  else
    ok "判据5a 空策略的 strategy_stats 是空数组（本来就没选策略，正确）：$stats"
  fi
fi

# 5b：这才是 P3-1 真正要消灭的形态 —— 选了却被静默丢弃。
echo "→ 5b. 显式选 base64"
created=$(create_run '{"purpose":"P3-6 explicit base64","plugins":[{"id":"contracts"}],"strategies":[{"id":"base64"}],"num_tests":3}')
id=$(json_field "$created" id)
if [ -z "$id" ]; then
  bad "判据5b 创建失败：$(json_field "$created" code) / $(printf '%s' "$created" | head -c 200)"
else
  final=$(wait_run "$id" 600)
  check "判据5b run 终态" "succeeded" "$(json_field "$final" phase)"
  if printf '%s' "$final" | tr -d '\n' | grep -q '"strategy_stats":\[\]'; then
    bad "判据5b strategy_stats 是空的 —— 选了 base64 却被静默丢弃，正是 P3-1 要消灭的形态"
  elif printf '%s' "$final" | tr -d '\n' | grep -q 'base64'; then
    ok "判据5b strategy_stats 里出现 base64（选中项真的被施加）"
  else
    bad "判据5b strategy_stats 里没有 base64：$(grep_field "$final" '"strategy_stats":\[[^]]*\]')"
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
hr "5 · 判据 8 —— 未知策略由引擎显式拒绝（不再静默丢弃）"
# ─────────────────────────────────────────────────────────────────────────────

# 走 BFF 而不是直连引擎：引擎不在宿主端口上（compose 只 expose 8090），
# 而 `no-such-strategy` 既不在目录里也不在退役表里，所以 BFF 两道闸都放行，
# 请求会真的到达引擎 —— 这正是要验的那条路径。
created=$(create_run '{"purpose":"P3-6 unknown strategy","plugins":[{"id":"contracts"}],"strategies":[{"id":"no-such-strategy"}],"num_tests":3}')
id=$(json_field "$created" id)
if [ -z "$id" ]; then
  bad "判据8 提交阶段就被拒了：$(json_field "$created" code)（预期是引擎侧失败，不是提交闸）"
else
  final=$(wait_run "$id" 600)
  phase=$(json_field "$final" phase)
  code=$(json_field "$final" error_code)
  check "判据8 未知策略 run 终态" "failed" "$phase"
  check "判据8 未知策略的 error_code" "native_generation_strategy_unknown" "${code:-<无>}"
fi

# ─────────────────────────────────────────────────────────────────────────────
hr "6 · 判据 9 —— 回滚路径（旧开关已无作用）"
# ─────────────────────────────────────────────────────────────────────────────

cat <<'NOTE'
P3-3 删掉了 `PROMPTFOO_ENGINE_NATIVE_GENERATION` 与它的路径选择，所以：
  · .env 里若还留着这个变量，它**不产生任何效果**（可以删掉）
  · 回滚只能靠**回退镜像/commit**，不能再「翻开关」
  · `production-cutover-checklist.md` 里已按这个口径写明

脚本能自动核的是「它确实没被读取」：引擎镜像里不应再有这个符号。
NOTE
if docker compose exec -T promptfoo-engine sh -c 'grep -rq "PROMPTFOO_ENGINE_NATIVE_GENERATION" /app/dist 2>/dev/null' 2>/dev/null; then
  bad "判据9 引擎 dist 里仍有 PROMPTFOO_ENGINE_NATIVE_GENERATION —— Plan A 开关没删干净"
else
  ok "判据9 引擎 dist 里已无 PROMPTFOO_ENGINE_NATIVE_GENERATION 符号"
fi

# ─────────────────────────────────────────────────────────────────────────────
hr "7 · 判据 10 —— MCP 工具描述不再推荐失效 id"
# ─────────────────────────────────────────────────────────────────────────────

if [ -z "$MCP_SECRET" ]; then
  warn "判据10 未提供 MCP_SECRET（.env 的 MACLAW_REDTEAM_MCP_SECRET），跳过"
else
  tools=$(curl -sS -X POST "$BFF/api/v1/internal/maclaw/redteam-mcp" \
    -H "Authorization: Bearer $MCP_SECRET" -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' 2>/dev/null)
  if [ -z "$tools" ]; then
    bad "判据10 tools/list 无响应（检查 MACLAW_REDTEAM_MCP_SECRET 与路由前缀）"
  else
    # 只扫 id 位：run_promptfoo_redteam_evaluation 的 plugins/strategies 描述。
    idbearing=$(printf '%s' "$tools" | tr -d '\n' | grep -o '"plugins":{"description":"[^"]*"' | head -1)
    idbearing="$idbearing $(printf '%s' "$tools" | tr -d '\n' | grep -o '"strategies":{"description":"[^"]*"' | head -1)"

    for dead in prompt-injection jailbreak direct role-play encoding multi-turn; do
      # 词边界匹配：`jailbreak-templates` 不应被当成非法的 `jailbreak`
      if printf '%s' "$idbearing" | grep -qE "(^|[^a-z0-9:_-])$dead([^a-z0-9:_-]|$)"; then
        bad "判据10 id 位描述里仍出现失效 id：$dead"
      else
        ok "判据10 id 位描述不含 $dead"
      fi
    done

    if printf '%s' "$idbearing" | grep -q "harmful, pii, bias, contracts"; then
      ok "判据10 插件例子已更新为可运行集合"
    else
      bad "判据10 插件例子不是预期集合：$(printf '%s' "$idbearing" | head -c 200)"
    fi
    if printf '%s' "$idbearing" | grep -q "base64, crescendo, jailbreak-templates, iterative:tree"; then
      ok "判据10 策略例子已更新为可运行集合"
    else
      bad "判据10 策略例子不是预期集合：$(printf '%s' "$idbearing" | head -c 200)"
    fi
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
hr "8 · 判据 6/7 —— native run 端到端与报告口径（需要你从浏览器发起一次对话评测）"
# ─────────────────────────────────────────────────────────────────────────────

cat <<'NOTE'
判据 6（端到端）与 7（报告口径）只在 **chat confirm 路径**产生报告
（报告由 WaitPromptfooRedteamEvaluation 编译，wizard 路径不编译报告）。

请手动做一次：
  1. 企业门户 → AI 对话 → 描述一次评测（例如「对当前被测模型做合规安全评测」）
  2. 等计划卡 → 确认执行 → 等完成卡
  3. 打开报告卡：应看到「生成路径：promptfoo 原生生成（plan_c）·实际配置探针数 N·
     与平台合成用例（plan_a）的报告不可直接比较…」
  4. 下载 PDF：「报告基本信息」段落应有同一句
  5. 历史报告（本批之前的）应显示「生成路径：未记录…」，**不是空白**
  6. 若该次对话里选过策略，报告卡的策略统计应列出**你选的那个 id**（判据 5b 的人工版）

然后跑下面这句核对落库值：
  docker compose exec -T postgres psql -U ${PG_USER:-postgres} -d ${PG_DB:-evaluating_platform} -c \
    "SELECT metadata->>'generation_path' AS path,
            metadata->>'num_tests_effective' AS effective,
            left(title,40) AS title
     FROM maclaw_redteam_reports ORDER BY created_at DESC LIMIT 5;"
期望：最新那条 path = plan_c 且 effective 有值。
NOTE

echo "→ 最近 5 份报告的落库口径："
psql_run "SELECT coalesce(metadata->>'generation_path','<空>') || ' | ' || coalesce(metadata->>'num_tests_effective','<空>') || ' | ' || left(coalesce(title,''),30) FROM maclaw_redteam_reports ORDER BY created_at DESC LIMIT 5;"

echo "→ engine_runs 错误码分布（本次验证产生的 run）："
psql_run "SELECT coalesce(error_code,'<none>') || ' | ' || count(*) FROM engine_runs WHERE purpose LIKE 'P3-6 %' GROUP BY 1 ORDER BY 2 DESC;"

# ─────────────────────────────────────────────────────────────────────────────
hr "汇总"
# ─────────────────────────────────────────────────────────────────────────────
printf '通过 %d / 失败 %d / 警告 %d\n' "$PASS" "$FAIL" "$WARN"
echo
echo "接下来："
echo "  · 有 ❌ 的项 → 按行内提示定位；失败 run 的 error_brief 是第一次排查入口"
echo "  · 判据 6/7（§8）需要你手动跑一次对话评测后回填"
echo "  · 全绿之后，P3-1 ~ P3-5 才算在服务器上验收完成；P3-7b/c/d/e 仍是未做的后续项"
echo "  · 本日志：/tmp/p3-verify-*.log（贴回来之前先确认无 payload / 响应正文 / 凭据 / 本地路径）"
