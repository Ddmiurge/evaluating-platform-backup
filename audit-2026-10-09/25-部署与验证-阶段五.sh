#!/usr/bin/env bash
# 25-部署与验证-阶段五.sh — 部署 P4 收尾三件（探针数不再翻倍 / 报告卡进会话 / 计划卡分轴与移除）
#
# 用法（在服务器上，任意目录）：
#   cd /da2/docker-files/evaluating-platform-backup/evaluating_platform
#   ENTERPRISE_EMAIL='enterprise@demo.com' ENTERPRISE_PASSWORD='...' \
#     bash audit-2026-10-09/25-部署与验证-阶段五.sh 2>&1 | tee /tmp/p5-verify-$(date +%F).log
#
# 可选环境变量：
#   BFF=http://localhost:8088
#   DEPLOY_DIR=...
#   EXPECT_COMMIT=abbe9f3   # 你推送并部署的 commit（留空则只打印不判）
#   SKIP_DEPLOY=1           # 只跑验证
#   BUILD_SERVICES="backend"  # 重建哪些镜像，默认只重建 backend
#                             # （重建 promptfoo-engine 需要联网跑 npm install ——
#                             #   本部署因临时网络窗口没开失败过两次；引擎未改时不必重建）
#
# 这批验的是三个**行为变化**，本地单测只能证明代码自洽：
#   1. 选了策略不再额外跑一遍「无策略」（§1–§3，全自动）
#   2. 引擎链路的报告以可下载卡片出现在会话里（§4，需要你跑一次对话评测，然后自动核）
#   3. 计划卡分轴显示 + × 移除（§5，人眼 + 一处服务端判据）
#
# 泄漏纪律：脚本只打印状态、计数、错误码。输出贴回来之前确认没有载荷正文、凭据、路径。

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

# 从 status JSON 里取一个数字字段（totals.probes 之类先压平再取）
grep_num() { printf '%s' "$1" | tr -d '\n' | grep -o "\"$2\":[0-9]*" | head -1 | cut -d: -f2; }
grep_str() { printf '%s' "$1" | tr -d '\n' | grep -o "\"$2\":\"[^\"]*\"" | head -1 | cut -d'"' -f4; }

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
  build_services=${BUILD_SERVICES:-backend}
  echo "→ 重建镜像：$build_services"
  # 只重建变了的服务。后端这轮改了 PDF 字体与 confirm 拒绝逻辑；引擎与前端自
  # 4e02072 起未改，且引擎构建需要联网拉 npm 依赖。
  docker compose build $build_services 2>&1 | tail -5 || bad "镜像构建失败"
  docker compose up -d 2>&1 | tail -5 || bad "compose up 失败"
fi

docker compose ps 2>&1 | sed -n '1,10p'
sleep 15
# 字体内嵌到二进制里，容器不需要装字体。这里只确认镜像里不是空壳：
if docker compose exec -T backend sh -c 'test -x /app/server' 2>/dev/null; then
  ok "后端二进制存在（字体经 go:embed 编入，无需容器内字体）"
else
  warn "未能在容器内确认 /app/server（路径或容器名不同？）"
fi
docker compose logs --tail=80 promptfoo-engine 2>&1 | grep -E "\[engine\]" | tail -4

# ─────────────────────────────────────────────────────────────────────────────
hr "1 · 登录"
# ─────────────────────────────────────────────────────────────────────────────
if [ -z "${ENTERPRISE_EMAIL:-}" ] || [ -z "${ENTERPRISE_PASSWORD:-}" ]; then
  bad "未提供 ENTERPRISE_EMAIL / ENTERPRISE_PASSWORD"
  exit 1
fi
login=$(curl -sS -X POST "$BFF/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$ENTERPRISE_EMAIL\",\"password\":\"$ENTERPRISE_PASSWORD\"}" 2>/dev/null)
JWT=$(json_field "$login" token)
[ -n "$JWT" ] && ok "登录成功" || { bad "登录失败"; exit 1; }
auth=(-H "Authorization: Bearer $JWT")

create_run() {
  curl -sS -X POST "$BFF/api/v1/maclaw/engine/runs" "${auth[@]}" \
    -H 'Content-Type: application/json' -d "$1" 2>/dev/null
}
wait_run() {
  local id=$1 limit=${2:-900} waited=0 body phase
  while [ "$waited" -lt "$limit" ]; do
    body=$(curl -sS "${auth[@]}" "$BFF/api/v1/maclaw/engine/runs/$id" 2>/dev/null)
    phase=$(json_field "$body" phase)
    case "$phase" in succeeded|failed|canceled) printf '%s' "$body"; return 0 ;; esac
    sleep 5; waited=$((waited+5))
  done
  printf '%s' "$body"; return 1
}

# ─────────────────────────────────────────────────────────────────────────────
hr "2 · 判据 1（核心）—— 选了策略不再多跑一遍「无策略」"
# ─────────────────────────────────────────────────────────────────────────────
echo "→ contracts + base64，num_tests=6。修复前会跑 12 条（none 6 + base64 6）。"
created=$(create_run '{"purpose":"P5 策略不翻倍","plugins":[{"id":"contracts"}],"strategies":[{"id":"base64"}],"num_tests":6}')
id=$(json_field "$created" id)
if [ -z "$id" ]; then
  bad "创建失败：$(json_field "$created" code)"
else
  final=$(wait_run "$id" 900)
  check "run 终态" "succeeded" "$(json_field "$final" phase)"

  probes=$(grep_num "$final" probes)
  nte=$(grep_num "$final" num_tests_effective)
  check "实际探针数（totals.probes）" "6" "${probes:-<缺失>}"
  check "配置探针数（num_tests_effective）" "6" "${nte:-<缺失>}"

  stats=$(printf '%s' "$final" | tr -d '\n' | grep -o '"strategy_stats":\[[^]]*\]' | head -1)
  if printf '%s' "$stats" | grep -q '"base64"'; then
    ok "strategy_stats 里有 base64（策略真的施加了）"
  else
    bad "strategy_stats 里没有 base64：$stats"
  fi
  if printf '%s' "$stats" | grep -q '"id":"none"'; then
    bad "strategy_stats 里仍有 none 条目 —— 对照组没关掉，探针还在翻倍：$stats"
  else
    ok "strategy_stats 里没有 none 条目（对照组已关）"
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
hr "3 · 判据 2 —— 不选策略时行为不变"
# ─────────────────────────────────────────────────────────────────────────────
echo "→ contracts，不选策略，num_tests=3"
created=$(create_run '{"purpose":"P5 无策略","plugins":[{"id":"contracts"}],"strategies":[],"num_tests":3}')
id=$(json_field "$created" id)
if [ -z "$id" ]; then
  bad "创建失败：$(json_field "$created" code)"
else
  final=$(wait_run "$id" 600)
  check "run 终态" "succeeded" "$(json_field "$final" phase)"
  check "实际探针数" "3" "$(grep_num "$final" probes)"
  stats=$(printf '%s' "$final" | tr -d '\n' | grep -o '"strategy_stats":\[[^]]*\]' | head -1)
  if printf '%s' "$stats" | grep -q '"id":"none"'; then
    ok "无策略的 run 仍记在 none 名下（与修复前一致）"
  else
    bad "无策略的 run 没有 none 条目：$stats"
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
hr "4 · 判据 3 —— 报告以可下载卡片出现在会话里"
# ─────────────────────────────────────────────────────────────────────────────
cat <<'NOTE'
这一步需要你**手动跑一次对话评测**（引擎链路：计划卡里带 promptfoo 插件）：
  企业门户 → AI 对话 → 描述评测需求 → 等 plan 卡 → 确认执行 → 等完成

跑完回来按 Enter，脚本会自动核这三件事：
  1. 会话快照里存在一条报告消息（card_type=report + report_id）
  2. 它带 downloadable=true / download_format=pdf（下载按钮据此渲染）
  3. 用该 report_id 调导出接口，能拿到真正的 PDF（%PDF- 开头 + 中文字体）

**注意**：卡片由后端重启前的内存作业表驱动，所以**必须在本轮部署之后跑的那次评测**才会命中。
NOTE
read -r -p "按 Enter 继续（或 Ctrl-C 跳过本节）…" _

sessions=$(curl -sS "${auth[@]}" "$BFF/api/v1/maclaw/evaluation/sessions?limit=10" 2>/dev/null)
session_id=$(printf '%s' "$sessions" | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
    items=d if isinstance(d,list) else (d.get('sessions') or d.get('items') or [])
    print(items[0].get('id','') if items else '')
except Exception: print('')" 2>/dev/null)
if [ -z "$session_id" ]; then
  bad "读不到会话列表，跳过本节"
else
  ok "最近会话：$session_id"
  snapshot=$(curl -sS "${auth[@]}" "$BFF/api/v1/maclaw/evaluation/sessions/$session_id" 2>/dev/null)

  report_id=$(printf '%s' "$snapshot" | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
    for m in d.get('messages') or []:
        md=m.get('metadata') or {}
        is_report = md.get('card_type')=='report' or md.get('evaluation_event_type')=='report' or md.get('response_source')=='report'
        if is_report and md.get('report_id'):
            print(md['report_id']); break
except Exception: pass" 2>/dev/null)

  if [ -z "$report_id" ]; then
    bad "会话快照里没有报告卡（card_type / evaluation_event_type / response_source 任一为 report，且带 report_id）"
    echo "   检查：这次评测走的是引擎链路吗？后端重启过吗（重启后卡片不再合成）？"
  else
    card_id=$(printf '%s' "$snapshot" | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
    for m in d.get('messages') or []:
        md=m.get('metadata') or {}
        if md.get('report_id')=='$report_id':
            print(m.get('id','')); break
except Exception: pass" 2>/dev/null)
    ok "会话里有报告卡：$report_id（消息 id=$card_id）"
    case "$card_id" in
      engine-report:*) ok "该卡由平台合成（engine-report:），即本批新增的路径" ;;
      *) warn "该卡的消息 id 不是 engine-report: 形状 —— 它可能来自 maclaw 自己的报告消息（运行时路径）。若本轮评测是引擎链路，请核对为什么平台合成没有生效" ;;
    esac

    dl=$(printf '%s' "$snapshot" | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
    for m in d.get('messages') or []:
        md=m.get('metadata') or {}
        if md.get('report_id')=='$report_id':
            print(md.get('downloadable',''), md.get('download_format','')); break
except Exception: pass" 2>/dev/null)
    check "下载开关（downloadable / format）" "true pdf" "$dl"

    # 真正的判据：这个 id 导出的字节是不是一个 PDF
    tmp=$(mktemp)
    curl -sS "${auth[@]}" -o "$tmp" "$BFF/api/v1/maclaw/evaluation/reports/$report_id/export?format=pdf" 2>/dev/null
    if head -c 5 "$tmp" | grep -q '%PDF-'; then
      ok "导出接口返回了 PDF（$(wc -c < "$tmp" | tr -d ' ') 字节）"
    else
      bad "导出接口返回的不是 PDF"
    fi
    rm -f "$tmp"
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
hr "5 · 判据 4 —— 计划卡分轴显示与移除（人眼 + 一处服务端判据）"
# ─────────────────────────────────────────────────────────────────────────────
cat <<'NOTE'
在**下一张**计划卡上人眼核：
  1. 有「攻击插件」与「攻击策略」两行，显示的是中文名（例如「系统提示词窃取」「Base64 编码」），
     不是 `promptfoo_plugin:` / `promptfoo_strategy:` 这种内部标记
  2. 原来的「选择策略」一行已改名为「资源选取方式」
  3. 两行的每一项都能点 ×，点了变删除线 + ↺（可撤销）
  4. 点 × 去掉一个插件后确认 → 该次 run 的插件集合里没有它
     （可查：docker compose exec -T postgres psql ... -c "SELECT plugins FROM engine_runs ORDER BY created_at DESC LIMIT 1;"）
  5. 把所有插件都点 × → 确认应被拒绝（promptfoo_plan_has_no_plugins），且不产生 run 行
  6. 重新加载页面后，已确认的卡会恢复成计划里的原选择 —— **已知限制**（移除状态未持久化），
     以 run 记录为准，不算失败
NOTE

echo "→ 移除只做减法的信任边界（服务端侧）：查最近一次 confirm 的 run 是否只含计划内的插件"
psql_run "SELECT coalesce(plugins::text,'') FROM engine_runs WHERE source='chat' ORDER BY created_at DESC LIMIT 1;"

# ─────────────────────────────────────────────────────────────────────────────
hr "汇总"
# ─────────────────────────────────────────────────────────────────────────────
printf '通过 %d / 失败 %d / 警告 %d\n' "$PASS" "$FAIL" "$WARN"
echo
echo "接下来："
echo "  · §2 若报「仍有 none 条目」，说明基础对照组没关掉 —— 那是这批的核心修复，先看引擎镜像是否真的重建了"
echo "  · §4 若报「没有报告卡」，先确认评测走的是引擎链路、且后端在本轮部署后没有重启过"
echo "  · §5 的第 6 条是已知限制，不计入失败"
echo "  · 本日志：/tmp/p5-verify-*.log（贴回前确认无载荷正文 / 凭据 / 本地路径）"
