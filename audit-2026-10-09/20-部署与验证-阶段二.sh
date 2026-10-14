#!/usr/bin/env bash
# 20-部署与验证-阶段二.sh — 在 ai02 上部署 P2-2 ~ P2-6 并逐项验证
#
# 用法（在服务器上，任意目录）：
#   cd /da2/docker-files/evaluating-platform-backup/evaluating_platform
#   ENTERPRISE_EMAIL='enterprise@demo.com' ENTERPRISE_PASSWORD='...' \
#     bash audit-2026-10-09/20-部署与验证-阶段二.sh 2>&1 | tee /tmp/p2-verify-$(date +%F).log
#
# 可选环境变量：
#   BFF=http://localhost:8088       # backend 直连端口（compose 里 8088:8080）
#   DEPLOY_DIR=...                  # 部署目录，默认见下
#   SKIP_DEPLOY=1                   # 只跑验证，不做 git/构建/重启
#   RUN_MATRIX=1                    # 额外跑 P2-8 的 26 个 id 能力矩阵（慢，20~40 分钟）
#
# 泄漏纪律：本脚本只打印状态、计数、错误码与**已脱敏的** error_brief。
# 任何一步的输出若要贴回来，先确认里面没有 prompt / 响应正文 / 凭据 / 本地路径。
#
# 本脚本**刻意不做**的事：不改被测目标配置。原因见 §4 的说明 —— target 凭据是
# write-only，改了之后无法还原。

set -uo pipefail

BFF=${BFF:-http://localhost:8088}
DEPLOY_DIR=${DEPLOY_DIR:-/da2/docker-files/evaluating-platform-backup/evaluating_platform}
EXPECT_COMMIT=${EXPECT_COMMIT:-b63636e}
SKIP_DEPLOY=${SKIP_DEPLOY:-0}
RUN_MATRIX=${RUN_MATRIX:-0}

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
  check "代码版本" "$EXPECT_COMMIT" "$head_commit"
  if [ "$head_commit" != "$EXPECT_COMMIT" ]; then
    bad "版本不符，先解决再继续；后续验证结果不能归因到本批改动"
  fi

  # P2-3 改了 promptfoo_engine/package.json（新增 drizzle-orm / @libsql/client 显式依赖），
  # 引擎镜像**必须重建**，否则启动时会因缺依赖失败。
  echo "→ 重建镜像（promptfoo-engine / backend / frontend）…"
  docker compose build promptfoo-engine backend frontend 2>&1 | tail -5 || bad "镜像构建失败"
  echo "→ 启动…"
  docker compose up -d 2>&1 | tail -5 || bad "compose up 失败"
fi

docker compose ps 2>&1 | sed -n '1,12p'

echo "→ 等引擎起来（20s）…"
sleep 20
engine_log=$(docker compose logs --tail=200 promptfoo-engine 2>&1 || true)
printf '%s\n' "$engine_log" | grep -E "\[engine\]" | tail -6

# P2-3 串行化证据：workers=1，且在 .env 里配了 >1 时还有一行 clamp 说明
if printf '%s' "$engine_log" | grep -q "listening on :8090 (workers=1)"; then
  ok "引擎 workers=1（P2-3 串行化生效）"
else
  bad "引擎未以 workers=1 启动；P2-3 的串行化没生效（并发会互删 SQLite）"
fi
if printf '%s' "$engine_log" | grep -q "clamping workers"; then
  ok "日志有 clamp 说明（.env 里配了 >1 个 worker）"
else
  warn "无 clamp 日志：若 .env 的 PROMPTFOO_ENGINE_WORKERS 本来就是 1 则正常"
fi
if printf '%s' "$engine_log" | grep -qiE "\[engine\] fatal|Failed to start"; then
  bad "引擎启动报错，见上面的日志"
else
  ok "引擎无 fatal"
fi

# 记录 tmpfs 基线，供 P2-3 的「占用回落」判据用
baseline_tmpfs=$(docker exec ep_promptfoo_engine df -k /home/node/.promptfoo 2>/dev/null | awk 'NR==2{print $3}')
[ -n "${baseline_tmpfs:-}" ] && ok "记录 tmpfs 已用基线：${baseline_tmpfs} KB" || warn "无法读取 tmpfs 基线（容器名不是 ep_promptfoo_engine？）"

# ─────────────────────────────────────────────────────────────────────────────
hr "1 · 登录并确认目标已配置"
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
  warn "未读到已启用的被测目标：请在企业门户「被测对象连接」里配置后再跑 P2-4 之后的步骤"
fi

# 便捷函数：发一个 engine run，回显新建 id
create_run() { # create_run <json-body>
  curl -sS -X POST "$BFF/api/v1/maclaw/engine/runs" "${auth[@]}" \
    -H 'Content-Type: application/json' -d "$1" 2>/dev/null
}

# 轮询到终态，回显最终 status JSON
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
hr "2 · P2-3 —— 零 docker exec 的 native run（R-3-e）"
# ─────────────────────────────────────────────────────────────────────────────

echo "→ 发起 run（不预先迁移、不 exec 进容器）…"
created=$(create_run '{"purpose":"P2-3 R-3-e","plugins":[{"id":"contracts"}],"strategies":[{"id":"base64"}],"num_tests":3}')
run_id=$(json_field "$created" id)
if [ -z "$run_id" ]; then
  bad "创建失败：$(printf '%s' "$created" | head -c 200)"
else
  ok "run 已创建：$run_id"
  final=$(wait_run "$run_id" 600)
  phase=$(json_field "$final" phase)
  code=$(json_field "$final" error_code)
  check "P2-3 run 终态" "succeeded" "$phase"
  if [ "$phase" != "succeeded" ]; then
    warn "失败码：${code:-<空>}（若为 native_generation_promptfoo_failed，读下面的 error_brief）"
    brief=$(json_field "$final" error_message)
    [ -n "$brief" ] && echo "   brief：$brief"
  fi
fi

echo "→ 容器内状态（终态后应为空目录，且无 unlinked 残留）…"
docker exec ep_promptfoo_engine sh -c 'ls -A /home/node/.promptfoo/ 2>/dev/null | head' > /tmp/p2_pfhome.txt 2>/dev/null
if [ -s /tmp/p2_pfhome.txt ]; then
  bad "终态后仍有残留：$(tr '\n' ' ' < /tmp/p2_pfhome.txt)"
  echo "   （P2-3 期望 /home/node/.promptfoo 为空，包含 promptfoo.yaml）"
else
  ok "终态后 /home/node/.promptfoo 为空（含 promptfoo.yaml 的清理清单生效）"
fi

if docker exec ep_promptfoo_engine sh -c 'command -v lsof >/dev/null && lsof +L1 2>/dev/null | grep -c promptfoo || echo 0' 2>/dev/null | grep -qx '0'; then
  ok "无「已删除但仍被进程持有」的 promptfoo 文件（lsof +L1）"
else
  warn "无法用 lsof 判定 unlinked inode（容器内可能没装 lsof）——见下面的 tmpfs 占用对比"
fi

after_tmpfs=$(docker exec ep_promptfoo_engine df -k /home/node/.promptfoo 2>/dev/null | awk 'NR==2{print $3}')
if [ -n "${baseline_tmpfs:-}" ] && [ -n "${after_tmpfs:-}" ]; then
  echo "   tmpfs 已用：基线 ${baseline_tmpfs} KB → 现在 ${after_tmpfs} KB"
  # 允许少量元数据波动；判据是「不随 run 数单调增长」
  if [ "$after_tmpfs" -le $((baseline_tmpfs + 512)) ]; then
    ok "tmpfs 占用回到基线附近（R-3-h：不随 run 数累积）"
  else
    bad "tmpfs 占用明显高于基线，可能存在未释放的 inode"
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
hr "3 · P2-4 —— num_tests 是探针总数上限"
# ─────────────────────────────────────────────────────────────────────────────

echo "→ 3a. 只选 harmful(26) + num_tests=50 → 期望实际 26（不是 1300）"
created=$(create_run '{"purpose":"P2-4 harmful cap","plugins":[{"id":"harmful"}],"num_tests":50}')
id=$(json_field "$created" id)
if [ -n "$id" ]; then
  final=$(wait_run "$id" 900)
  eff=$(printf '%s' "$final" | tr -d '\n' | grep -o '"num_tests_effective":[0-9]*' | head -1 | cut -d: -f2)
  probes=$(printf '%s' "$final" | tr -d '\n' | grep -o '"probes":[0-9]*' | head -1 | cut -d: -f2)
  check "harmful + 50 的实际配置探针数" "26" "${eff:-<缺失>}"
  [ -n "$probes" ] && echo "   实际执行探针（totals.probes）：$probes"
  gp=$(printf '%s' "$final" | tr -d '\n' | grep -o '"generation_path":"[^"]*"' | head -1 | cut -d'"' -f4)
  check "P2-6 引擎侧 generation_path" "plan_c" "${gp:-<缺失>}"
else
  bad "3a 创建失败：$(printf '%s' "$created" | head -c 200)"
fi

echo "→ 3b. harmful+pii+contracts(31) + num_tests=31 → 期望 31"
created=$(create_run '{"purpose":"P2-4 exact","plugins":[{"id":"harmful"},{"id":"pii"},{"id":"contracts"}],"num_tests":31}')
id=$(json_field "$created" id)
if [ -n "$id" ]; then
  final=$(wait_run "$id" 900)
  eff=$(printf '%s' "$final" | tr -d '\n' | grep -o '"num_tests_effective":[0-9]*' | head -1 | cut -d: -f2)
  check "三插件 + 31 的实际配置探针数" "31" "${eff:-<缺失>}"
fi

echo "→ 3c. harmful + num_tests=10 → 期望提交即拒（num_tests_too_small），且不产生 run 行"
rejected=$(create_run '{"purpose":"P2-4 below floor","plugins":[{"id":"harmful"}],"num_tests":10}')
rcode=$(json_field "$rejected" code)
check "低于下限的拒绝码" "num_tests_too_small" "${rcode:-<无>}"
msg=$(json_field "$rejected" error)
[ -n "$msg" ] && printf '   提示：%s\n' "$msg" | head -c 400 && echo
if printf '%s' "$rejected" | grep -q '"expanded_plugins"'; then
  ok "拒绝响应带 expanded_plugins（前端可据此提示）"
else
  warn "拒绝响应没有 expanded_plugins 字段"
fi

echo "→ 3d. harmful + 不填 num_tests → 期望被接受（解析为 26，不是 below-floor）"
created=$(create_run '{"purpose":"P2-4 omitted count","plugins":[{"id":"harmful"}]}')
rcode=$(json_field "$created" code)
if [ "$rcode" = "num_tests_too_small" ]; then
  bad "未填轮次被自己的下限闸拒了 —— 平台默认选择无法提交"
else
  ok "未填轮次被接受（解析为 max(默认值, 下限)）"
  id=$(json_field "$created" id)
  [ -n "$id" ] && wait_run "$id" 900 >/dev/null
fi

echo "→ 3e. 上限仍是 50"
over=$(create_run '{"purpose":"P2-4 cap","plugins":[{"id":"contracts"}],"num_tests":51}')
rcode=$(json_field "$over" code)
check "超过上限的拒绝码" "num_tests_too_large" "${rcode:-<无>}"

# ─────────────────────────────────────────────────────────────────────────────
hr "4 · P2-5 —— 失败可诊断性（error_brief / 错误码）"
# ─────────────────────────────────────────────────────────────────────────────

cat <<'NOTE'
说明：本脚本**不修改被测目标配置**来人为造失败 —— target 的 credential_secret 是
write-only，改了无法还原。这里改为「在真实场景里产生的失败上验证链路」，以及
用 item8 的两个策略场景制造一次确定性失败（iterative:tree 若失败即为真实失败）。

若你确实要一条**保证失败**的 run：在企业门户「被测对象连接」里把目标端点临时改成
一个不可达地址（如 http://127.0.0.1:1/v1）并保存，跑一条 run，看完 error_brief 后
**重新填回原端点并重新输入密钥**。该操作由你手动完成，脚本不代劳。
NOTE

echo "→ 4a. item8：iterative:meta（P2-2 已闸）应提交即拒"
m=$(create_run '{"purpose":"P2-7 item8 meta","plugins":[{"id":"contracts"}],"strategies":[{"id":"iterative:meta"}],"num_tests":3}')
check "iterative:meta 的拒绝码" "strategy_requires_remote_generation" "$(json_field "$m" code)"

echo "→ 4b. item8：jailbreak（旧别名，已闸）应提交即拒"
j=$(create_run '{"purpose":"P2-7 item8 alias","plugins":[{"id":"contracts"}],"strategies":[{"id":"jailbreak"}],"num_tests":3}')
check "jailbreak 策略的拒绝码" "strategy_requires_remote_generation" "$(json_field "$j" code)"

echo "→ 4c. item8：iterative:tree（实测不需远程）应先被接受"
t=$(create_run '{"purpose":"P2-7 item8 tree","plugins":[{"id":"contracts"}],"strategies":[{"id":"iterative:tree"}],"num_tests":3}')
if [ -n "$(json_field "$t" id)" ]; then
  ok "iterative:tree 被接受（若随后失败，用下面的 brief 定位）"
  wait_run "$(json_field "$t" id)" 900 >/dev/null
else
  warn "iterative:tree 未被接受：$(json_field "$t" code) / $(json_field "$t" error | head -c 200)"
fi

echo "→ 4d. 检查最近失败 run 的 error_brief 与错误码"
psql_run() { docker compose exec -T postgres psql -U "${PG_USER:-postgres}" -d "${PG_DB:-evaluating_platform}" -At -c "$1" 2>/dev/null; }
fails=$(psql_run "SELECT id || '|' || coalesce(error_code,'') || '|' || coalesce(error_brief,'') FROM engine_runs WHERE status='failed' ORDER BY created_at DESC LIMIT 5;")
if [ -z "$fails" ]; then
  warn "没有失败 run 可供检查 —— 用上面的方法造一个失败后重跑本节"
else
  printf '%s\n' "$fails" | while IFS='|' read -r rid rcode rbrief; do
    [ -z "$rid" ] && continue
    echo "   run=$rid code=$(printf '%s' "$rcode" | head -c 60)"
    if [ -z "$rbrief" ]; then
      bad "   ↑ 该失败 run 的 error_brief 为空（P2-5 的写入者没生效）"
    else
      ok "   ↑ error_brief 非空（$(printf '%s' "$rbrief" | wc -c) 字符）"
      printf '     %s\n' "$(printf '%s' "$rbrief" | head -c 200)"
      # 脱敏判据
      printf '%s' "$rbrief" | grep -qE 'sk-[A-Za-z0-9_-]{6,}' && bad "   ↑ brief 含 sk- 形式密钥" || ok "   ↑ 无 sk- 密钥"
      printf '%s' "$rbrief" | grep -qE 'https?://[^ /]+:[0-9]+' && bad "   ↑ brief 含 host:port" || ok "   ↑ 无 host:port"
      printf '%s' "$rbrief" | grep -qE '(/[A-Za-z0-9._-]+){3,}' && bad "   ↑ brief 含绝对路径" || ok "   ↑ 无绝对路径"
    fi
    [ "$rcode" = "engine_error" ] && bad "   ↑ 错误码仍是 engine_error（P2-5 的拆码未生效）" || ok "   ↑ 错误码具体"
  done
fi

echo "→ 4e. 通用码应已消失"
oldcode=$(psql_run "SELECT count(*) FROM engine_runs WHERE error_code='native_generation_config_invalid';")
if [ "${oldcode:-0}" = "0" ]; then
  ok "没有新的 native_generation_config_invalid（通用码已拆）"
else
  warn "仍有 $oldcode 条 native_generation_config_invalid（若都是本批之前的历史行则正常）"
fi

echo "→ 4f. 成功 run 的 error_brief 应为空串"
empty_brief=$(psql_run "SELECT count(*) FROM engine_runs WHERE status='succeeded' AND coalesce(error_brief,'') <> '';")
check "成功 run 中带 brief 的行数" "0" "${empty_brief:-?}"

# ─────────────────────────────────────────────────────────────────────────────
hr "5 · P2-6 —— 报告口径（需要你从浏览器发起一次对话评测）"
# ─────────────────────────────────────────────────────────────────────────────

cat <<'NOTE'
引擎侧的 generation_path / num_tests_effective 已在上面的 §3 验证（走 wizard API）。
**报告 metadata 与 PDF 里的那一行只在 chat confirm 路径产生**（报告由
WaitPromptfooRedteamEvaluation 编译，wizard 路径不编译报告）。

请手动做一次：
  1. 企业门户 → AI 对话 → 描述一次评测（例如「对当前被测模型做合规安全评测」）
  2. 等计划卡 → 确认执行 → 等完成卡
  3. 打开报告卡：应看到「生成路径：promptfoo 原生生成（plan_c）·实际配置探针数 N·
     与平台合成用例（plan_a）的报告不可直接比较…」，且它**单独一行**在口径说明之后
  4. 下载 PDF：第「报告基本信息」段落应有同一句
  5. 历史报告（本批之前的）应显示「生成路径：未记录…」，**不是空白**

然后跑下面这句核对落库值：
  docker compose exec -T postgres psql -U ${PG_USER:-postgres} -d ${PG_DB:-evaluating_platform} -c \
    "SELECT metadata->>'generation_path' AS path,
            metadata->>'num_tests_effective' AS effective,
            metadata->>'judge_track' AS track,
            left(title,40) AS title
     FROM maclaw_redteam_reports ORDER BY created_at DESC LIMIT 5;"
期望：最新的两条 path = plan_c 且 effective 有值（G-4 要求至少 2 份）。
NOTE

# ─────────────────────────────────────────────────────────────────────────────
if [ "$RUN_MATRIX" = "1" ]; then
hr "6 · P2-8 —— 26 个合法本地 id 能力矩阵"
# ─────────────────────────────────────────────────────────────────────────────
IDS="beavertails bias contracts cross-session-leak cyberseceval divergent-repetition \
donotanswer excessive-agency hallucination harmbench harmful harmful:intellectual-property \
harmful:privacy imitation overreliance pii pii:api-db pii:direct pii:session pii:social \
pliny politics prompt-extraction teen-safety:age-restricted-goods-and-services xstest"
FANOUT_JSON="$DEPLOY_DIR/internal/maclaw/testdata/promptfoo_plugin_legality.json"
for id in $IDS; do
  fanout=$(python3 -c "import json;print(json.load(open('$FANOUT_JSON'))['fanout']['$id'])" 2>/dev/null)
  [ -z "$fanout" ] && { warn "读不到 $id 的 fanout，跳过"; continue; }
  created=$(create_run "{\"purpose\":\"P2-8 matrix $id\",\"plugins\":[{\"id\":\"$id\"}],\"num_tests\":$fanout}")
  rid=$(json_field "$created" id)
  if [ -z "$rid" ]; then
    printf '   %-46s 提交被拒 code=%s\n' "$id" "$(json_field "$created" code)"
    continue
  fi
  final=$(wait_run "$rid" 900)
  printf '   %-46s phase=%-9s probes=%s\n' "$id" "$(json_field "$final" phase)" \
    "$(printf '%s' "$final" | tr -d '\n' | grep -o '"probes":[0-9]*' | head -1 | cut -d: -f2)"
done
echo "→ 结果汇总（回填 14-Phase2实施计划.md §3）："
psql_run "SELECT error_code, count(*) FROM engine_runs WHERE purpose LIKE 'P2-8 matrix %' GROUP BY error_code ORDER BY 2 DESC;"
else
  hr "6 · P2-8 —— 能力矩阵已跳过（用 RUN_MATRIX=1 重跑本节）"
fi

# ─────────────────────────────────────────────────────────────────────────────
hr "汇总"
# ─────────────────────────────────────────────────────────────────────────────
printf '通过 %d / 失败 %d / 警告 %d\n' "$PASS" "$FAIL" "$WARN"
echo
echo "接下来："
echo "  · 有 ❌ 的项 → 按行内提示定位；失败 run 的 error_brief 是这次的第一次排查入口"
echo "  · 报告口径（§5）与能力矩阵（§6）跑完后，再判 18 §7.2 的 5 条 G 条件"
echo "  · G-5（ALLOW_PLAN_A 反向开关）本轮未做，阶段三不应启动"
echo "  · 本日志：/tmp/p2-verify-*.log（贴回来之前先确认无 payload / 响应正文 / 凭据 / 本地路径）"
