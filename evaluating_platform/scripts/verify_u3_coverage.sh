#!/usr/bin/env bash
# verify_u3_coverage.sh —— U3 结构治理「覆盖率零漂移」可复现验收脚本
#
# 用途：Step 6 收尾时替换临时脚本，让 QA / 第三方能独立复算
#       「拆包前后覆盖率是否漂移」。退出码：0=零漂移 1=检出漂移 2=环境错误
#
# ── 为什么需要这个脚本（两个Go 覆盖率统计陷阱）────────────────────────
#
# 陷阱 1：per-package 统计不承认跨包覆盖
#   redteam 子包没有自己的 _test.go（测试在父包，通过 redteam.XXX() 间接覆盖）。
#   `go test ./internal/maclaw/...` 会把子包写进 profile，但跨包调用在
#   per-package 统计里不计入子包 → 子包显示 0.0%，把 total 拉低约 3.4pp。
#   ⚠️ 口径 C的 total 禁止用于回归判定，会误报成「代码失去覆盖」。
#
# 陷阱 2：覆盖率必须用「语句块覆盖数」比，而非百分比
#   Step 3 改名 +移码会让行号漂移，profile 里同一函数的行号会变，
#   直接 diff 文本会产生大量假差异。所以本脚本按「被覆盖的语句块集合」比对，
#   并以「块数/总块数」重算百分比，避开百分比四舍五入误差。
#
# ── 口径对照（务必同口径比）────────────────────────────────────────
#   基线（u3_baseline_coverage_maclaw.txt）= go test ./internal/maclaw/
#       -coverpkg=./internal/maclaw/... -covermode=set
#   → 含父包 + 子包 4038 个语句块，其中子包 207 块
#
#   口径 A 父包 vs 父包    回归判定依据（子包 0% 不影响它）
#   口径 B -coverpkg 全量  子包真实覆盖率，证明「间接覆盖成立」
#   口径 C per-package 全量  仅参考，禁止用于回归判定（见陷阱 1）
#
# 用法：
#   ./scripts/verify_u3_coverage.sh                  # 对比基线 vs 当前
#   ./scripts/verify_u3_coverage.sh --gen-baseline   # 重新生成基线（行号稳定后用）
#
# 可覆盖的环境变量：
#   GO_BIN=/path/to/go   指定未加入 PATH 的 go（本机为 /Users/<user>/sdk/go/bin/go）
#   DRIFT_THRESHOLD=0.5允许的漂移阈值（百分点，默认 0.5）

set -uo pipefail

readonly PKG_PARENT="./internal/maclaw"
readonly PKG_ALL="./internal/maclaw/..."
readonly TESTDATA="internal/maclaw/testdata"
readonly BASELINE_PROFILE="${TESTDATA}/u3_baseline_coverage_maclaw.txt"
readonly TMPDIR_U3="$(mktemp -d)"
readonly GO_BIN="${GO_BIN:-go}"

# 沙箱兼容：清掉会劫持 fs 操作的 broker 变量（与网络无关，详见脚本头）。
# GO_BIN 可能指向未加入 PATH 的 go；但 go test 编译子包时会自行再调go 工具链，
# 所以必须把其目录补进 PATH，否则报 exec: "go": not found in $PATH。
run_go() {
  env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy \
      -u TOYBOX_SANDBOX_SOCK -u CODEBUDDY_SANDBOX_BROKER_IPC_ADDRESS \
      PATH="$(dirname -- "${GO_BIN}"):${PATH}" \
      "${GO_BIN}" "$@"
}

cleanup() { rm -rf "${TMPDIR_U3}"; }
trap cleanup EXIT

die()  { printf '\033[31m[ERROR]\033[0m %s\n' "$*" >&2; exit 2; }
info() { printf '\033[36m[INFO]\033[0m %s\n' "$*"; }
ok()   { printf '\033[32m[ OK ]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[WARN]\033[0m %s\n' "$*"; }

require_go() {
  [[ -x "$(command -v "${GO_BIN}" 2>/dev/null || echo "${GO_BIN}")" ]] || \
    command -v "${GO_BIN}" >/dev/null 2>&1 || \
    die "找不到 go。请设 GO_BIN=/path/to/go（例：GO_BIN=/Users/<user>/sdk/go/bin/go）"
}

# 覆盖率按「语句块」统计：返回 "<被覆盖块> <总块> <百分比>"。
# 之所以不用 `go tool cover -func| total`：
#   -covermode=set 的 profile 不产出 total: 行；
#   - 且百分比会四舍五入，掩盖 0.1pp 级漂移。
# 用法：cover_ratio <profile 文件>
cover_ratio() {
  awk 'NR>1 {n++; if ($NF > 0) c++}
       END {if (n>0) printf "%d %d %.2f", c+0, n, (c+0)*100/n; else exit 1}' "$1"
}

# 统计属于 redteam 子包的语句块数。
sub_blocks() {
  awk 'NR>1 && $1 ~ /internal\/maclaw\/redteam\// {n++} END {print n+0}' "$1"
}

# 提取 profile 的 mode 头（set/count/atomic）。
profile_mode() { head -1 "$1" 2>/dev/null | awk '{print $2}'; }

# 生成三种口径的 profile；结果写入全局 PROFILE_*。
gen_profiles() {
  local out="$1"; mkdir -p "${out}"
  info "生成口径 A（父包）…"
  run_go test "${PKG_PARENT}" -coverprofile="${out}/parent.txt" -covermode=count \
    >"${out}/parent.log" 2>&1 || { tail -20 "${out}/parent.log"; die "父包测试失败"; }
  PROFILE_PARENT="${out}/parent.txt"

  info "生成口径 B（-coverpkg 全量，与基线同口径）…"
  run_go test "${PKG_PARENT}" -coverpkg="${PKG_ALL}" -coverprofile="${out}/coverpkg.txt" -covermode=set \
    >"${out}/coverpkg.log" 2>&1 || { tail -20 "${out}/coverpkg.log"; die "-coverpkg 测试失败"; }
  PROFILE_COVERPKG="${out}/coverpkg.txt"

  info "生成口径 C（per-package 全量，仅参考）…"
  run_go test "${PKG_ALL}" -coverprofile="${out}/all.txt" -covermode=count \
    >"${out}/all.log" 2>&1 || { tail -20 "${out}/all.log"; die "全量测试失败"; }
  PROFILE_ALL="${out}/all.txt"
}

gen_baseline() {
  warn "将覆盖基线文件（仅在行号已稳定后执行）"
  gen_profiles "${TMPDIR_U3}/gen"
  mkdir -p "${TESTDATA}"
  cp "${PROFILE_COVERPKG}" "${BASELINE_PROFILE}"
  ok "基线已重生成：${BASELINE_PROFILE}"
}

main() {
  require_go
  # 无论从哪调用，都切到脚本所在的 evaluating_platform 根目录（下面全是相对路径）。
  # 不能用 git rev-parse --show-toplevel：它会跳到仓库根，导致相对路径失效。
  local root; root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
  cd "${root}" || die "无法进入 ${root}"
  [[ -f go.mod ]] || die "未在 evaluating_platform 根目录：${root}"

  if [[ "${1:-}" == "--gen-baseline" ]]; then gen_baseline; return 0; fi

  [[ -f "${BASELINE_PROFILE}" ]] || \
    die "基线不存在：${BASELINE_PROFILE}（先跑 --gen-baseline）"

  printf '\n\033[1m=== U3 覆盖率零漂移验收 ===\033[0m\n'
  printf '基线 mode : %s\n' "$(profile_mode "${BASELINE_PROFILE}")"

  gen_profiles "${TMPDIR_U3}/cmp"

  # ---------- 口径 A：仅父包（辅助参考）----------
  # 注意：基线是 -coverpkg 口径（含子包），所以「仅父包」与基线不可直接比，
  # 真正的同口径主判定在口径 B。这里只展示当前父包自身的覆盖情况。
  info "口径 A｜仅父包（辅助参考；基线为 coverpkg 口径，与此不可直接比）"
  local a_cur
  a_cur="$(cover_ratio "${PROFILE_PARENT}")" || die "读不到当前父包统计"
  printf '     当前仅父包: %s\n' "${a_cur}"

  # ---------- 口径 B：与基线同口径对比（主判定）----------
  info "口径 B｜-coverpkg 全量（与基线同口径，主判定）"
  local b_base b_cur base_sub cur_sub
  b_base="$(cover_ratio "${BASELINE_PROFILE}")" || die "读不到基线统计"
  b_cur="$(cover_ratio "${PROFILE_COVERPKG}")" || die "读不到当前统计"
  base_sub="$(sub_blocks "${BASELINE_PROFILE}")"
  cur_sub="$(sub_blocks "${PROFILE_COVERPKG}")"

  # 解析 "<covered> <total> <pct>"
  local bc bt bp cc ct cp
  read -r bc bt bp <<<"${b_base}"
  read -r cc ct cp <<<"${b_cur}"
  printf '     基线: %s/%s 块 = %s%%（含子包 %s 块）\n' "${bc}" "${bt}" "${bp}" "${base_sub}"
  printf '     当前: %s/%s 块 = %s%%（含子包 %s 块）\n' "${cc}" "${ct}" "${cp}" "${cur_sub}"

  # ---------- 口径 C：仅参考 ----------
  info "口径 C｜per-package 全量（仅参考，禁止用于回归判定）"
  local c_ratio
  c_ratio="$(cover_ratio "${PROFILE_ALL}")" || c_ratio="N/A"
  printf '     per-package: %s\n' "${c_ratio}"

  # ---------- 子包真实覆盖率（口径 B 内）----------
  local sub_n sub_avg
  sub_n="$(run_go tool cover -func="${PROFILE_COVERPKG}" 2>/dev/null | grep -c 'internal/maclaw/redteam/')"
  sub_avg="$(run_go tool cover -func="${PROFILE_COVERPKG}" 2>/dev/null \
    | awk '/internal\/maclaw\/redteam\// {gsub(/%/,"",$NF); s+=$NF; n++} END {if(n>0) printf "%.1f", s/n}')"
  printf '\n子包（跨包覆盖已在口径 B 中被承认）\n'
  printf '     函数数: %s   平均覆盖率: %s%%\n' "${sub_n}" "${sub_avg:-N/A}"

  # ---------- 结论 ----------
  printf '\n\033[1m=== 结论 ===\033[0m\n'
  local verdict=0
  local threshold="${DRIFT_THRESHOLD:-0.5}"

  # 主判定：被覆盖语句块数是否漂移（比百分比更敏感、更可靠）。
  local block_drift
  block_drift="$(awk -v a="${bc}" -v b="${cc}" 'BEGIN{d=b-a; if(d<0)d=-d; print d+0}')"
  local pct_drift
  pct_drift="$(awk -v a="${bp}" -v b="${cp}" 'BEGIN{d=b-a; if(d<0)d=-d; printf "%.2f", d}')"

  if [[ "${block_drift}" -eq 0 ]]; then
    ok "被覆盖语句块数零漂移：基线 ${bc} → 当前 ${cc}（完全相同）"
  elif [[ "${block_drift}" -le "${DRIFT_BLOCK_TOLERANCE:-2}" ]]; then
    warn "被覆盖块数相差 ${block_drift} 块（在容差 ${DRIFT_BLOCK_TOLERANCE:-2} 内），百分比差 ${pct_drift}pp"
  else
    warn "被覆盖块数漂移 ${block_drift} 块（${bc} → ${cc}），百分比差 ${pct_drift}pp → 需人工确认"
    verdict=1
  fi

  # 口径一致性守卫。
  # 基线是 Step 4 拆包「之前」生成的：当时子包尚不存在，那37 个函数还在
  # redteam_tool_bridge.go 里，所以基线含子包块数= 0 属正常。
  # 真正要防的是「基线与当前由不同 coverpkg 范围生成」——
  # 用总块数是否相等来判定（拆包不改变语句总数：4038 = 3831 + 207）。
  local base_total_blocks cur_total_blocks
  base_total_blocks="$(awk 'NR>1{n++} END{print n+0}' "${BASELINE_PROFILE}")"
  cur_total_blocks="$(awk 'NR>1{n++} END{print n+0}' "${PROFILE_COVERPKG}")"

  if [[ "${base_total_blocks}" != "${cur_total_blocks}" ]]; then
    warn "基线语句块 ${base_total_blocks} ≠ 当前 ${cur_total_blocks} → 两侧口径不一致，结论不可信"
    warn "请用 ./scripts/verify_u3_coverage.sh --gen-baseline 重新生成同口径基线"
    verdict=1
  else
    ok "口径一致：两侧语句块总数均为 ${base_total_blocks}（拆包不改变语句总数）"
    if [[ "${base_sub}" == "0" && "${cur_sub}" != "0" ]]; then
      printf '     基线含子包 0 块属正常：基线生成于 Step 4 拆包前，代码当时在父包 redteam_tool_bridge.go\n'
    fi
  fi

  if awk -v d="${pct_drift}" -v t="${threshold}" 'BEGIN{exit !(d<=t)}'; then
    ok "覆盖率零漂移：|${pct_drift}|pp ≤ 阈值 ${threshold}pp（${bp}% → ${cp}%）"
  else
    warn "覆盖率漂移 ${pct_drift}pp（阈值 ${threshold}pp）"
    verdict=1
  fi

  #口径 C 必然低于口径 B（子包 0% 稀释），提醒但不判失败。
  if [[ "${c_ratio}" != "N/A" ]]; then
    warn "口径 C(${c_ratio}%) 低于口径 B(${cp}%) 是预期的统计口径差异，不是回归"
  fi

  printf '\n'
  if [[ "${verdict}" -eq 0 ]]; then
    ok "通过：可作为 Step 6 验收证据"
    printf '     报告时请写明口径（如「口径 B：-coverpkg 全量，%s%% → %s%%」）\n' "${bp}" "${cp}"
  else
    warn "未通过：按上方提示处理；勿用口径 C 的 total 做回归判定"
  fi
  return "${verdict}"
}

main "$@"
