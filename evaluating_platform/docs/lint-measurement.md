# Lint 测量口径：如何正确判断「我的改动有没有引入新 lint 问题」

> 适用：`evaluating_platform` Go 模块的 `golangci-lint`（v2.6.2，配置见 `.golangci.yml`）。
> 本文回答一个问题：**跑一次 lint 看到数字变了，到底是我的代码变差了，还是测量口径变了？**
>
> 这份判据的价值高于配置本身。历史上实施者被口径骗过：gofmt 删一个空行，
> 总数就从 183 变成 184，看起来像「新引入了一个问题」，实际代码一行没变。

---

## 0. 结论先行（TL;DR）

| 症状 | 真实原因 | 处置 |
|---|---|---|
| 总数 ±1 抖动，每个 linter 分项数都不变 | **行号位移** + `uniq-by-line` 按行合并 | 忽略，不是回归 |
| 总数上升，且**某一个 linter 分项**同步上升 | 该 linter 真的多报了 | 这才是真回归 |
| 两次跑同一份代码，数字不同 | **缓存**或行号位移 | 清缓存后重跑 |
| 清缓存后两侧数字同时变了 | 缓存导致绝对值不可信 | 见 §3 |
| goconst 逐条 finding 在文件间跳动，但条数不变 | goconst 取「某个」代表出现点，并发顺序不固定 | **噪声，忽略**，只比条数 |

**唯一可信的判据 = 逐 linter 分项对比，且两侧都清缓存、都显式 `--uniq-by-line=false`。**
其中 `errcheck`/`unused` 可精确到逐条内容，`goconst` **只能比条数**（见 §4）。

---

## 1. `uniq-by-line` 是什么，为什么它是陷阱

`uniq-by-line` 是 golangci-lint **自己**的一道跨 linter 去重，不是任何单个 linter 的行为。
默认值 `true`：**文本相同 + 行号相同**的两个 finding 只报一次。

问题在于各linter 的报告单位不同：

- **goconst 按「出现位置」逐个报** —— 同一个字面量出现 3 次就是 3 条 finding，这是它的语义。
- **golangci-lint 的去重按「行号」** —— 两处不同行的问题，只要因为位移落到同一行，就合并成一条。

两者叠加就制造出假回归：

```
1. gofmt 删掉第 100 行的一个空行
2. 该文件 100 行之后的所有行号 -1
3. 原本在 120 行和 121 行的两条 goconst finding，现在都在 120 行 → 被合并 → 总数 -1
   （反过来，若一条 finding 移进另一条的行号范围被拆开，就是 +1）
4. ⇒ 总数变化 ±1，代码质量零变化
```

**方向取决于你改了什么，与代码好坏无关。** 这就是「183 → 184 看起来像我引入了新问题」的机制。

## 2. 已实测的口径差（同一 commit `7f94dd1`，两次都清缓存）

```bash
export PATH="/Users/huangqixu/sdk/go/bin:$HOME/go/bin:$PATH"
cd evaluating_platform

# 侧 A：golangci-lint 默认（uniq-by-line=true）
golangci-lint cache clean
golangci-lint run --uniq-by-line=true --issues-exit-code=0 --output.text.path=/tmp/a.txt ./...

# 侧 B：不按行去重
golangci-lint cache clean
golangci-lint run --uniq-by-line=false --issues-exit-code=0 --output.text.path=/tmp/b.txt ./...
```

| 口径 | 总数 | errcheck | goconst | unused |
|---|---|---|---|---|
| `--uniq-by-line=true`（默认） | **184** | 91 | 87 | 6 |
| `--uniq-by-line=false` | **198** | 91 | **101** | 6 |

差值 **14 条，全部落在 goconst**。这14 条是被「同行合并」藏起来的真实 finding，
不是新增缺陷。`errcheck` / `unused` 不受影响，因为它们天然一行一问题，不会撞行。

**配置固化后**（`.golangci.yml` 已设 `uniq-by-line: false`），不传任何 flag 也是 198：

```bash
golangci-lint config verify          # 必须先过：v2 拒绝未知键，防止配置静默失效
golangci-lint cache clean
golangci-lint run --issues-exit-code=0 ./...
# => 198 issues: errcheck 91 / goconst 101 / unused 6
```

> ⚠️ **数字上升不是回归。** 加 `uniq-by-line: false` 之后基线从 184 变 198 是**预期且正确**的。
> 任何人看到 198 并试图"降回 184"，就是在把测量口径改回去，请直接拒绝。

## 3. 为什么缓存会让绝对值不可信

golangci-lint v2 把分析结果缓存在 `os.UserCacheDir()`（与 `GOCACHE` 并列，CI 里由 `setup-go` 的
`cache: true` 一并缓存）。命中缓存时它会**重放上次的 finding 集合**，而不是重算。

后果：

- 改了代码但没清缓存 → 数字是**旧代码的**，看起来"我的改动没效果"。
- 清了缓存再跑 → 数字变了，但你分不清是缓存造成的还是代码造成的。
- 更糟的是 A/B 两侧**缓存状态不一致**时，两侧数字会同时平移，看起来像 ±1 抖动。

**所以：任何一次「数字变了」的判断，都必须建立在两侧都 `cache clean` 之上。**
CI 里 `setup-go` 的缓存是**故意保留**的（重新类型检查整个模块很慢），
但本地做 A/B 对照时必须清掉 —— 两者不矛盾：**缓存用来提速，不用来做测量**。

## 4. 正确判据（照着做）

```bash
export PATH="/Users/huangqixu/sdk/go/bin:$HOME/go/bin:$PATH"
cd evaluating_platform

measure() {                      # $1 = 输出文件标签
  golangci-lint cache clean      # ① 必须清缓存
  golangci-lint run \
    --uniq-by-line=false \      # ② 必须显式关闭按行合并
    --issues-exit-code=0 \
    --output.text.path="/tmp/$1.txt" ./... 2>&1 | tail -4
}

measure before        # 改动前
git stash             # 或 git checkout -- <files>
measure after         # 改动后
diff <(grep -oE '\((errcheck|goconst|unused)\)$' /tmp/before.txt | sort | uniq -c) \
     <(grep -oE '\((errcheck|goconst|unused)\)$' /tmp/after.txt  | sort | uniq -c)
```

判定表：

| 观察 | 判定 |
|---|---|
| 分项数全部一致，总数差 ±1 | **行号位移**，忽略 |
| 某一个 linter 分项 +N | **真回归**，去修 |
| 某个 linter 分项 -N | 你修掉了一些，正常 |
| 两侧数字完全一致 | 你的改动对 lint 零影响 |

### 更稳的做法：比内容而不是比数字

⚠️ **实测修正（重要）**：`goconst` 的**计数稳定，但「归到哪个文件」不稳定**。
同一份代码、同一配置、两侧都 `cache clean`，连跑两次：

|指标 | 第 1 次 | 第 2 次 | 结论 |
|---|---|---|---|
| 总数 | 192 | 192 | ✅ 稳定 |
| `errcheck` 数| 91 | 91 | ✅ 稳定 |
| `unused` 数 | 0 | 0 | ✅ 稳定 |
| `goconst` 数 | 101 | 101 | ✅ **稳定** |
| `goconst` 的**逐条文件归属** | — | — | ❌ **不稳定**（同一字面量被归到 `_test.go` 或 `.go` 不定，norm diff 有 39 行差异） |
| `errcheck`+`unused` 的 norm diff | — | — | ✅ **0 行，完全确定** |

原因：goconst 对一个重复字面量只报**一条**，位置取「某个」代表出现点；
golangci-lint 内部的并发遍历顺序不固定，所以代表点会在同组的文件间跳动。
**这跟你改了什么代码无关。**

**因此判据必须按 linter 分开用：**

```bash
norm() { grep -E "\((errcheck|unused|goconst)\)$" "$1" \
         | sed -E 's/:[0-9]+:[0-9]+:/:/' | sort; }

# ① 确定性 linter（errcheck / unused）：比内容，diff 必须为空
diff <(norm /tmp/before.txt | grep -E "\((errcheck|unused)\)$") \
     <(norm /tmp/after.txt  | grep -E "\((errcheck|unused)\)$")

# ② goconst：只比【计数】，不要比逐条归属
g_before=$(grep -c 'goconst)$' /tmp/before.txt)
g_after=$(grep -c  'goconst)$' /tmp/after.txt)
echo "goconst $g_before -> $g_after"
```

判定：

| 观察 | 判定 |
|---|---|
| `errcheck`/`unused` 的 norm diff 为空 **且** goconst 计数不变 | **你的改动确实零影响**（无论总数怎么变） |
| `errcheck`/`unused` 的 norm diff 非空 | **真回归**，那几行就是 |
| goconst 计数变化 | 真的有新的重复字面量，去看 |
| goconst 逐条归属变化但计数不变 | **噪声，忽略** |

## 5. 反面案例（本条判据存在的理由）

前一批实施者（gofmt 清理 16 个文件）实测：

| 场景 | 观察到的数字 | 当时的结论 | 真相 |
|---|---|---|---|
| gofmt 前vs 后，默认口径 | 183 → 184 | "我的 gofmt 引入了一个新问题" | 行号位移，无问题 |
| 清缓存后重跑 | 185 / 187 | 更困惑了 | 缓存导致绝对值本来就不可信 |
| 改用 `--uniq-by-line=false` | 198 / 198 | **完全一致** | 改动确实零影响 |

结论：gofmt 那次改动**没有引入任何 lint 问题**。如果当时按默认口径的 183→184
去"修复"，就会去改一个根本不存在的问题，或者被迫放宽配置 —— 这正是
`uniq-by-line: false` 要防止的。

## 6. 相关文件

- `.golangci.yml` —— `issues.uniq-by-line: false` 及完整理由注释
- `scripts/lint_go.sh` —— CI 入口，**fail-closed**（区分「有告警」与「linter 没跑起来」）
- `.github/workflows/ci.yml` —— `golangci` job，phase 1 = warn-only