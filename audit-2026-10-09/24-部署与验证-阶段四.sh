#!/usr/bin/env bash
# 24-部署与验证-阶段四.sh — 部署「报告原文 + promptfoo 对齐」并验证
#
# 用法（在服务器上，任意目录）：
#   cd /da2/docker-files/evaluating-platform-backup/evaluating_platform
#   ENTERPRISE_EMAIL='enterprise@demo.com' ENTERPRISE_PASSWORD='...' \
#     bash audit-2026-10-09/24-部署与验证-阶段四.sh 2>&1 | tee /tmp/p4-verify-$(date +%F).log
#
# 可选环境变量：
#   BFF=http://localhost:8088
#   DEPLOY_DIR=...          # 部署目录
#   EXPECT_COMMIT=...       # 你推送并部署的 commit（留空则只打印不判）
#   SKIP_DEPLOY=1           # 只跑验证，不做 git/构建/重启
#
# 判据来源：`23-聊天链路报告原文与promptfoo对齐.md` §9 的 12 条。
#
# 本脚本自动跑的：引擎逐条原文（判据 1 的引擎侧）、P1 生效（判据 12 的一半）、
#   凭据仍被清除（判据 4）、进度元数据干净（判据 5）、平台判定无分数（判据 6a）、
#   报告体积（判据 8）。
# 需要你人眼看的（脚本会打印要做什么）：PDF 里的攻击原文与目标回答（判据 2/3）、
#   分数分档（判据 6b/6c）、逐条块形状（判据 9a/9b/9c）。
#
# 泄漏纪律：本脚本只打印状态、计数、错误码。任何输出贴回来之前，先确认里面没有
# prompt / 响应正文 / 凭据 / 本地路径。

set -uo pipefail

BFF=${BFF:-http://localhost:8088}
DEPLOY_DIR=${DEPLOY_DIR:-/da2/docker-files/evaluating-platform-backup/evaluating_platform}
EXPECT_COMMIT=${EXPECT_COMMIT:-}
SKIP_DEPLOY=${SKIP_DEPLOY:-0}

PASS=0; FAIL=0; WARN=0
ok()   { printf '✅ %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '❌ %s\n' "$*"; FAIL=$((FAIL+1)); }
warn() { printf '⚠️  %s\n' "$*"; WARN=$((WARN+1)); }
hr()   { printf '\n──────── %s ────────\n' "$*"; }

check() { if [ "$2" = "$3" ]; then ok "$1（$3）"; else bad "$1：期望 [$2]，实际 [$3]"; fi; }

json_field() {
  local body=$1 key=$2
  if command -v jq >/dev/null 2>&1; then
    printf '%s' "$body" | jq -r --arg k "$key" 'if type=="object" then (.[$k] // "") else "" end' 2>/dev/null
  elif command -v python3 >/dev/null 2>&1; then
    printf '%s' "$body" | python3 -c "import json,sys
try:
    d=json.load(sys.stdin); v=d.get('$key','') if isinstance(d,dict) else ''
    print('' if v is None else v)
except Exception: print('')" 2>/dev/null
  else
    printf '%s' "$body" | grep -o "\"$key\":[^,}]*" | head -1 | sed 's/.*: *"\?//; s/"\?$//'
  fi
}

psql_run() { docker compose exec -T postgres psql -U "${PG_USER:-postgres}" -d "${PG_DB:-evaluating_platform}" -At -c "$1" 2>/dev/null; }

# ─────────────────────────────────────────────────────────────────────────────
hr "0 · 部署"
# ─────────────────────────────────────────────────────────────────────────────
if [ "$SKIP_DEPLOY" = "1" ]; then
  warn "SKIP_DEPLOY=1，跳过 git/构建/重启"
else
  cd "$DEPLOY_DIR" 2>/dev/null || { bad "部署目录不存在：$DEPLOY_DIR"; exit 1; }
  git fetch -q origin 2>&1 || bad "git fetch 失败"
  git checkout -q main 2>&1 || bad "切到 main 失败"
  git pull -q --ff-only 2>&1 || bad "git pull --ff-only 失败"
  head_commit=$(git rev-parse --short HEAD)
  if [ -n "$EXPECT_COMMIT" ]; then
    check "代码版本" "$EXPECT_COMMIT" "$head_commit"
  else
    warn "未提供 EXPECT_COMMIT，只打印当前版本：$head_commit"
  fi
  echo "→ 重建镜像（引擎改了 SafeRunResult 与 redact 白名单；backend 改了 findings 与 PDF；前端改了文案）…"
  docker compose build promptfoo-engine backend frontend 2>&1 | tail -5 || bad "镜像构建失败"
  docker compose up -d 2>&1 | tail -5 || bad "compose up 失败"
fi

docker compose ps 2>&1 | sed -n '1,10p'
sleep 15
engine_log=$(docker compose logs --tail=120 promptfoo-engine 2>&1 || true)
printf '%s\n' "$engine_log" | grep -E "\[engine\]" | tail -5
if printf '%s' "$engine_log" | grep -qiE "\[engine\] fatal"; then
  bad "引擎启动报错"
else
  ok "引擎无 fatal"
fi

# ─────────────────────────────────────────────────────────────────────────────
hr "1 · 登录"
# ─────────────────────────────────────────────────────────────────────────────
if [ -z "${ENTERPRISE_EMAIL:-}" ] || [ -z "${ENTERPRISE_PASSWORD:-}" ]; then
  bad "未提供 ENTERPRISE_EMAIL / ENTERPRISE_PASSWORD，无法继续"
  exit 1
fi
login=$(curl -sS -X POST "$BFF/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$ENTERPRISE_EMAIL\",\"password\":\"$ENTERPRISE_PASSWORD\"}" 2>/dev/null)
JWT=$(json_field "$login" token)
[ -n "$JWT" ] && ok "登录成功" || { bad "登录失败：$(printf '%s' "$login" | head -c 120)"; exit 1; }
auth=(-H "Authorization: Bearer $JWT")

create_run() {
  curl -sS -X POST "$BFF/api/v1/maclaw/engine/runs" "${auth[@]}" \
    -H 'Content-Type: application/json' -d "$1" 2>/dev/null
}
wait_run() {
  local id=$1 limit=${2:-600} waited=0 body phase
  while [ "$waited" -lt "$limit" ]; do
    body=$(curl -sS "${auth[@]}" "$BFF/api/v1/maclaw/engine/runs/$id" 2>/dev/null)
    phase=$(json_field "$body" phase)
    case "$phase" in succeeded|failed|canceled) printf '%s' "$body"; return 0 ;; esac
    sleep 5; waited=$((waited+5))
  done
  printf '%s' "$body"; return 1
}

# ─────────────────────────────────────────────────────────────────────────────
hr "2 · 判据 1（引擎侧）—— 逐条原文真的出来了"
# ─────────────────────────────────────────────────────────────────────────────
echo "→ 发起一次引擎 run（contracts + base64，3 条探针）…"
created=$(create_run '{"purpose":"P4 case_results","plugins":[{"id":"contracts"}],"strategies":[{"id":"base64"}],"num_tests":3}')
id=$(json_field "$created" id)
if [ -z "$id" ]; then
  bad "创建失败：$(json_field "$created" code)"
else
  final=$(wait_run "$id" 600)
  check "引擎 run 终态" "succeeded" "$(json_field "$final" phase)"

  # case_results 必须存在且条数与 probes 一致
  cases=$(printf '%s' "$final" | tr -d '\n' | grep -o '"case_results":\[.*\]' | head -1 | grep -o '"index":' | wc -l | tr -d ' ')
  probes=$(printf '%s' "$final" | tr -d '\n' | grep -o '"probes":[0-9]*' | head -1 | cut -d: -f2)
  if [ "${cases:-0}" -eq 0 ]; then
    bad "判据1 case_results 为空 —— 引擎没有把逐条原文带出来"
  else
    ok "判据1 case_results 有 $cases 条（probes=$probes）"
  fi

  # 每条都得有非空的 prompt 与 response —— 这才是「原文」，不是空壳
  for field in '"prompt":"' '"response":"' '"strategy_id":"'; do
    if printf '%s' "$final" | grep -q "$field"; then
      ok "判据1 逐条字段存在：$field"
    else
      bad "判据1 逐条字段缺失：$field"
    fi
  done
  if printf '%s' "$final" | tr -d '\n' | grep -q '"prompt":""'; then
    bad "判据1 有探针的攻击载荷是空串"
  fi

  # 策略真的被施加（判据 12 的一半；另一半在报告里 —— 自选评测链路一直是对的，
  # 这条只有在确认链路才有意义，所以这里只做提示）
  strat=$(printf '%s' "$final" | tr -d '\n' | grep -o '"strategy_stats":\[[^]]*\]' | head -1)
  if printf '%s' "$strat" | grep -q '"base64"'; then
    ok "判据12（引擎侧）strategy_stats 里有 base64"
  else
    bad "判据12（引擎侧）strategy_stats 里没有 base64：$strat"
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
hr "3 · 判据 4/5/6a —— 数据边界（SQL 自动核对）"
# ─────────────────────────────────────────────────────────────────────────────

echo "→ 判据4：报告 findings 里不得出现凭据形状（sk-… / bearer …）"
leak=$(psql_run "SELECT count(*) FROM maclaw_redteam_reports WHERE findings::text ~* '(sk-[a-z0-9]{6,}|bearer [a-z0-9._-]{8,})';")
if [ -z "$leak" ]; then
  warn "判据4 读不到（psql 不可用或表名不同）"
else
  check "含凭据形状的报告数" "0" "$leak"
fi

echo "→ 判据4b：报告 findings 里不得出现本部署自己的路径"
pathleak=$(psql_run "SELECT count(*) FROM maclaw_redteam_reports WHERE findings::text ~ '(\.promptfoo/|/data/runs/|/app/dist/)';")
if [ -n "$pathleak" ]; then
  check "含本部署路径的报告数" "0" "$pathleak"
fi

echo "→ 判据5：进度元数据不得含 prompt / payload / response 正文"
dirty=$(psql_run "SELECT count(*) FROM engine_runs WHERE stage_durations::text ~* '(prompt|payload|response)[\"_][^,}]*[^\"]';")
if [ -n "$dirty" ]; then
  check "进度元数据可疑行数" "0" "$dirty"
else
  warn "判据5 读不到"
fi

echo "→ 判据6a：平台判定（judge_track=platform）的报告不得出现任何分数"
platformScored=$(psql_run "SELECT count(*) FROM maclaw_redteam_reports WHERE metadata->>'judge_track' = 'platform' AND findings::text LIKE '%grading_score%';")
if [ -n "$platformScored" ]; then
  check "平台判定报告里带分数的行数" "0" "$platformScored"
else
  warn "判据6a 读不到（可能还没有 platform 判定的报告）"
fi

echo "→ 判据8：报告体积（原文进来之后，一份报告有多大）"
psql_run "SELECT '最新报告 findings 字节数: ' || length(findings::text) FROM maclaw_redteam_reports ORDER BY created_at DESC LIMIT 1;"

# ─────────────────────────────────────────────────────────────────────────────
hr "4 · 判据 12 —— 确认链路的策略（需要你从浏览器发起一次对话评测）"
# ─────────────────────────────────────────────────────────────────────────────
cat <<'NOTE'
这一段验的是**上一批的 P1**，但只有它生效，报告里的策略信息才有意义。

请手动做一次对话评测（计划卡里要**选上策略**，例如 base64）：
  企业门户 → AI 对话 → 描述评测需求 → 等 plan 卡 → 确认执行

然后跑下面这句，看 confirm 产生的 run 里 strategies 是否为空：

  docker compose exec -T postgres psql -U ${PG_USER:-postgres} -d ${PG_DB:-evaluating_platform} -c \
    "SELECT id, source, strategies, jsonb_array_length(coalesce(result->'case_results','[]'::jsonb)) AS cases
     FROM engine_runs WHERE source = 'chat' ORDER BY created_at DESC LIMIT 3;"

期望：**strategies 非空**（P1 修好了）。若为空，说明 confirm 仍然在丢策略 —— 那就是 P1 没生效，
报告里的策略统计也会是空的。
NOTE
psql_run "SELECT id || ' | strategies=' || strategies::text || ' | cases=' || coalesce(jsonb_array_length(result->'case_results'),0) FROM engine_runs WHERE source = 'chat' ORDER BY created_at DESC LIMIT 3;"

# ─────────────────────────────────────────────────────────────────────────────
hr "5 · 需要你人眼看的（判据 2/3/6b/6c/9a/9b/9c）"
# ─────────────────────────────────────────────────────────────────────────────
cat <<'NOTE'
下载上面那次对话评测的 PDF，逐条核对：

判据 2（**用户原话的验收标准**）—— 用挖系统提示词的用例跑一次
  → PDF 第 3 段必须能看到**目标模型实际吐出的系统提示词正文**，不是「模型给出了实质性回答」这类总结。

判据 3 —— 批量链路（专家样本 / Skill）没被破坏
  → 第 3 段仍有 `攻击载荷：` / `目标回答：` 的原文。注意标签从「测试问题/模型回答」
    改成了「攻击载荷/目标回答」，这是本批的刻意对齐，不是回归。

判据 6b/6c —— 分数分档
  → 引擎判定的报告：逐条能看到通过/不通过 + 分数 + 判定理由；
  → 平台判定的报告：只有通过/不通过 + 理由，**没有任何数字**；
  → 任何一份报告里都不应同时出现两种分数。

判据 9a —— 逐条块的形状
  → `攻击载荷：` 一段、`目标回答：` 一段，多行内容**保留换行**（不是挤成一坨）。

判据 9b —— 取值链一致
  → 同一个用例在 promptfoo 自己的报告与我们的 PDF 里，显示的载荷应一致。

判据 9c —— 第 5 段「评估指标」
  → 应有「按攻击类型（共 N 类）」与「按攻击策略（共 N 项）」两块；
  → 无策略的探针显示为「无（直接提问）」，不应出现一个叫 `none` 的手法。
NOTE

# ─────────────────────────────────────────────────────────────────────────────
hr "汇总"
# ─────────────────────────────────────────────────────────────────────────────
printf '通过 %d / 失败 %d / 警告 %d\n' "$PASS" "$FAIL" "$WARN"
echo
echo "接下来："
echo "  · 有 ❌ 的项按行内提示定位；「判据12」为 ❌ 说明 P1 没生效，报告里的策略信息也就无从谈起"
echo "  · §5 的人眼判据跑完，这批才算验收完成"
echo "  · 本日志：/tmp/p4-verify-*.log（贴回来之前先确认无 payload / 响应正文 / 凭据 / 本地路径）"
