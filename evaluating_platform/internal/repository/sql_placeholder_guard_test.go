package repository

// sql_placeholder_guard_test.go —— 「SQL 占位符编号 vs 实参个数」静态护栏
//
// # 这条护栏在防什么（连续两次线上故障的共同类别）
//
//	2026-10-12  027 迁移漏登记      → 代码与数据库契约漂移
//	2026-10-13  LinkJob 参数编号笔误 → 代码与自身 SQL 契约漂移
//
// 后者（SQLSTATE 42P18）的形态是：SQL 文本引用了 $4，但调用只传了 3 个实参。
// pgx 按位置绑定，于是 $3 在语句里没有任何出现处，PostgreSQL 在 Parse 阶段
// 就无法推断它的类型，直接拒绝整条语句 —— 一次都不会执行，引擎已经消耗掉的
// 目标调用全部作废。
//
// 关键点：**这类错误在内存 fake 上完全不可见。** fakeEngineRunStore 只实现
// Go 接口，根本没有 SQL 解析器；参数编号错误要等到真 PostgreSQL 才暴露。
// 所以这里不再依赖 fake，而是对源码做 AST 静态分析，在 go test 阶段就拦下。
//
// # 为什么必须是 AST 而不是正则
//
// SQL 在本仓库里的主流写法是「先定义，再传参」：
//
//	const query = `UPDATE … SET job_id = $3 …` + engineRunColumns
//	record, err := scanEngineRun(r.pool.QueryRow(ctx, query, runID, userID, jobID))
//
// 正则扫 `pool.QueryRow(ctx, query, ...)` 这行时**看不到 SQL 内容**，因为
// $N 全在被引用的常量里。这就是纯文本扫描会给出「0 条问题」这种假阴性的原因。
// 本文件用 go/ast 做符号解析：先建立「标识符 → 字符串常量」表，再回到调用点
// 把 SQL 文本拼回来，最后才数占位符。
//
// # 判定规则
//
// 设 SQL 中出现的最大占位符编号为 maxN，实参个数为 argCount：
//
//	1. gap（缺口）：1..maxN 中有编号没出现 → 必然 42P18，与实参个数无关
//	2. maxN > argCount → 42P18（LinkJob 的原始形态）
//	3. maxN < argCount → pgx 报 "supplies N parameters but statement requires M"
//	4. argCount > 0 且 maxN == 0 → 同上，反向形态
//
// # 已知边界（如实登记，不假装覆盖）
//
// 以下写法在静态分析下**无法确定** SQL 全文，会被归入 unverifiable 而**不会**
// 被当成通过 —— 这是刻意的：宁可少报，也不能给假绿灯。
//
//   - fmt.Sprintf("… LIMIT $%d …", n)：%d 可能注入新的 $N，上界不由源码决定
//   - "SELECT … " + where，而 where 由 strings.Join(conds) 拼出：
//     条件片段与实参是同步增长的，静态无法线性化
//   - args... 展开的实参个数：除非能解析被调函数返回的切片字面量长度，
//     否则未知（此时仍会执行规则 1 的 gap 检查）
//
// 这些位置由 scan 报告单独列出，供人工审计。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ───────────────────────── 词法层 ─────────────────────────

var (
	// sqlgPlaceholderRe 匹配 PostgreSQL 的 $N 占位符。$ 后面必须紧跟数字，
	// 因此 `$$`（dollar-quoting 定界符）与 `$%d`（Sprintf 动词）都不会命中。
	sqlgPlaceholderRe = regexp.MustCompile(`\$(\d+)`)

	// sqlgSQLShapeRe 用于把 Gin's c.Query("limit") / url.Values.Query() 这类
	// 同名但与 SQL 无关的方法排除掉。只有长得像 SQL 的调用才进入分析。
	sqlgSQLShapeRe = regexp.MustCompile(`(?is)\b(select|insert\s+into|update|delete\s+from|with)\b`)
)

// sqlgStripNoise 把 SQL 中的字符串字面量、quoted identifier、行注释、
// 块注释与 dollar-quoted 段替换成等长空格。
//
// 为什么必须做：占位符在语法上不可能出现在上述区域里，若不抹掉，注释里一句
// `-- TODO: bump to $5` 或 JSON 字面量里的 "$1" 就会被误判成真实占位符，
// 直接制造假阳性、污染 CI。抹掉之后 $N 计数与 PostgreSQL 的词法认知一致。
//
// 状态机单趟扫描，等长替换保证后续用字节下标切注释片段时语义不变。
func sqlgStripNoise(s string) string {
	const (
		sqlgCode       = iota
		sqlgSQuote     // '...'
		sqlgDQuote     // "..."
		sqlgLineCmt    // -- ...
		sqlgBlockCmt   // /* ... */
		sqlgDollarBody // $tag$ ... $tag$
	)

	out := []byte(s)
	state := sqlgCode
	// dollarTag 保存进入 dollar-quoted 段时的定界符（含两侧 $），用于找闭合。
	dollarTag := ""

	for i := 0; i < len(out); {
		switch state {
		case sqlgCode:
			switch {
			case out[i] == '\'':
				out[i] = ' '
				state = sqlgSQuote
				i++
			case out[i] == '"':
				out[i] = ' '
				state = sqlgDQuote
				i++
			case out[i] == '-' && i+1 < len(out) && out[i+1] == '-':
				out[i] = ' '
				i++
				state = sqlgLineCmt
			case out[i] == '/' && i+1 < len(out) && out[i+1] == '*':
				out[i] = ' '
				i++
				state = sqlgBlockCmt
			case out[i] == '$':
				tag := sqlgDollarTag(out, i)
				if tag == "" {
					i++
					continue
				}
				for j := 0; j < len(tag); j++ {
					out[i+j] = ' '
				}
				i += len(tag)
				dollarTag = tag
				state = sqlgDollarBody
			default:
				i++
			}

		case sqlgSQuote:
			// '' 是 SQL 标准的转义写法（一个转义单引号），不结束字符串。
			if out[i] == '\'' {
				if i+1 < len(out) && out[i+1] == '\'' {
					out[i] = ' '
					out[i+1] = ' '
					i += 2
					continue
				}
				out[i] = ' '
				state = sqlgCode
				i++
				continue
			}
			// 字面量**内部**必须整体抹掉：只抹分隔符的话 '$7' 会变成 " 7 "，
			// $7 仍被当成真占位符 —— 这正是本文件词法单测最初抓到的自身缺陷。
			if out[i] != '\n' {
				out[i] = ' '
			}
			i++

		case sqlgDQuote:
			if out[i] == '"' {
				if i+1 < len(out) && out[i+1] == '"' {
					out[i] = ' '
					out[i+1] = ' '
					i += 2
					continue
				}
				out[i] = ' '
				state = sqlgCode
				i++
				continue
			}
			if out[i] != '\n' {
				out[i] = ' '
			}
			i++

		case sqlgLineCmt:
			if out[i] == '\n' {
				state = sqlgCode
				i++
				continue
			}
			out[i] = ' '
			i++

		case sqlgBlockCmt:
			if out[i] == '*' && i+1 < len(out) && out[i+1] == '/' {
				out[i] = ' '
				out[i+1] = ' '
				i += 2
				state = sqlgCode
				continue
			}
			if out[i] != '\n' {
				out[i] = ' '
			}
			i++

		case sqlgDollarBody:
			if out[i] == '$' && strings.HasPrefix(string(out[i:]), dollarTag) {
				for j := 0; j < len(dollarTag); j++ {
					out[i+j] = ' '
				}
				i += len(dollarTag)
				dollarTag = ""
				state = sqlgCode
				continue
			}
			if out[i] != '\n' {
				out[i] = ' '
			}
			i++
		}
	}
	return string(out)
}

// sqlgDollarTag 在 out[i] == '$' 处尝试匹配 dollar-quoting 定界符 $tag$，
// 成功返回含两侧 $ 的完整定界符，否则返回 ""（说明这是普通占位符）。
func sqlgDollarTag(out []byte, i int) string {
	j := i + 1
	for j < len(out) && (isTagByte(out[j])) {
		j++
	}
	if j < len(out) && out[j] == '$' {
		return string(out[i : j+1])
	}
	return ""
}

func isTagByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

// sqlgPlaceholderNums 返回 SQL 中出现过的全部 $N 编号（已去重）。
func sqlgPlaceholderNums(sql string) map[int]bool {
	nums := map[int]bool{}
	for _, m := range sqlgPlaceholderRe.FindAllStringSubmatch(sqlgStripNoise(sql), -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		nums[n] = true
	}
	return nums
}

// ───────────────────────── 符号解析层 ─────────────────────────

// sqlgBinding 是一个可静态求值的「名字 → 字符串」绑定。
type sqlgBinding struct {
	name string
	val  ast.Expr
}

// sqlgScope 是求值 SQL 表达式时可见的两层符号表：函数局部优先于包级。
type sqlgScope struct {
	locals map[string]string
	pkg    map[string]string
}

func (s sqlgScope) lookup(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.Ident:
		if val, ok := s.locals[v.Name]; ok {
			return val, true
		}
		if val, ok := s.pkg[v.Name]; ok {
			return val, true
		}
	case *ast.SelectorExpr:
		// 跨包常量（如 somepkg.Columns）：本包作用域按裸名再找一次。
		// 找不到就返回 false —— 宁可漏报也不猜。
		if val, ok := s.pkg[v.Sel.Name]; ok {
			return val, true
		}
	}
	return "", false
}

// sqlgText 是把一个 Go 表达式静态求值成 SQL 文本的结果。
type sqlgText struct {
	text string // 已解析出的片段；当 complete=false 时可能只是前缀
	// complete 表示整个表达式都被解析出来了。false 时不得据此判定「通过」，
	// 因为缺失的部分可能引入新的占位符。
	complete bool
}

func (t sqlgText) merge(other sqlgText) sqlgText {
	return sqlgText{text: t.text + other.text, complete: t.complete && other.complete}
}

// sqlgResolveSQL 递归求值 SQL 表达式，支持：
// 字面量、字符串拼接（+）、括号、Sprintf 调用、以及上述形式的任意嵌套。
func sqlgResolveSQL(e ast.Expr, sc sqlgScope) sqlgText {
	switch v := e.(type) {
	case *ast.ParenExpr:
		return sqlgResolveSQL(v.X, sc)

	case *ast.BasicLit:
		if v.Kind == token.STRING {
			if unq, err := strconv.Unquote(v.Value); err == nil {
				return sqlgText{text: unq, complete: true}
			}
		}
		return sqlgText{}

	case *ast.BinaryExpr:
		if v.Op == token.ADD {
			return sqlgResolveSQL(v.X, sc).merge(sqlgResolveSQL(v.Y, sc))
		}
		return sqlgText{}

	case *ast.Ident, *ast.SelectorExpr:
		if val, ok := sc.lookup(e); ok {
			return sqlgText{text: val, complete: true}
		}
		return sqlgText{}

	case *ast.CallExpr:
		// Sprintf 的格式串本身可读，但 %d 可能注入新的 $N，占位符上界不再
		// 由源码字面量决定 —— 因此降级为「不可静态验证」。
		if len(v.Args) > 0 {
			if inner := sqlgResolveSQL(v.Args[0], sc); inner.complete {
				return sqlgText{text: inner.text, complete: false}
			}
		}
		return sqlgText{}
	}
	return sqlgText{}
}

// sqlgCollectPkgScope 收集包级 const/var 的字符串值。
//
// 两趟解析：第一趟只收字面量，第二趟才解析依赖其它常量的拼接。顺序很关键 ——
// `engineRunWriteColumns` 拼接了先声明的 `engineRunColumns`，单趟会漏掉它。
func sqlgCollectPkgScope(f *ast.File) map[string]string {
	out := map[string]string{}

	var bindings []sqlgBinding
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i < len(vs.Values) && name.Name != "_" {
					bindings = append(bindings, sqlgBinding{name: name.Name, val: vs.Values[i]})
				}
			}
		}
	}

	pass := func(literalsOnly bool) {
		for _, b := range bindings {
			if _, exists := out[b.name]; exists {
				continue // 先到先得，避免后到的拼接覆盖已确定的字面量
			}
			if _, isLit := b.val.(*ast.BasicLit); literalsOnly != isLit {
				continue
			}
			if t := sqlgResolveSQL(b.val, sqlgScope{pkg: out}); t.complete {
				out[b.name] = t.text
			}
		}
	}
	pass(true)
	pass(false)
	return out
}

// sqlgCollectLocalScope 收集函数体内的 const/var 与 := 短声明。
// 只收能静态求值成字符串的那些；`where := "WHERE " + strings.Join(...)`
// 这类求不出完整值的**不会**进表，于是引用它的拼接会被正确标为不可验证。
//
// pkg 必须传入而非用全局：包级作用域先于函数体收集，是单向依赖，
// 用全局可变状态既会在 -race 下不安全，也会让两个包的扫描互相污染。
func sqlgCollectLocalScope(fn *ast.FuncDecl, pkg map[string]string) map[string]string {
	out := map[string]string{}
	if fn.Body == nil {
		return out
	}

	var bindings []sqlgBinding
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.GenDecl:
			if st.Tok != token.CONST && st.Tok != token.VAR {
				return true
			}
			for _, spec := range st.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i < len(vs.Values) && name.Name != "_" {
						bindings = append(bindings, sqlgBinding{name: name.Name, val: vs.Values[i]})
					}
				}
			}
		case *ast.AssignStmt:
			if st.Tok != token.DEFINE {
				return true
			}
			for i, lhs := range st.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name == "_" || i >= len(st.Rhs) {
					continue
				}
				bindings = append(bindings, sqlgBinding{name: id.Name, val: st.Rhs[i]})
			}
		}
		return true
	})

	pass := func(literalsOnly bool) {
		for _, b := range bindings {
			if _, exists := out[b.name]; exists {
				continue
			}
			if _, isLit := b.val.(*ast.BasicLit); literalsOnly != isLit {
				continue
			}
			if t := sqlgResolveSQL(b.val, sqlgScope{pkg: pkg, locals: out}); t.complete {
				out[b.name] = t.text
			}
		}
	}
	pass(true)
	pass(false)
	return out
}

// ───────────────────────── 调用点扫描 ─────────────────────────

var sqlgQueryMethods = map[string]bool{
	"QueryRow": true,
	"Query":    true,
	"Exec":     true,
}

// sqlgCallSite 是一个数据库调用点的静态画像。
type sqlgCallSite struct {
	pos       token.Position
	receiver  string // 形如 r.pool / tx / pool
	method    string // QueryRow / Query / Exec
	enclosing string

	sql        sqlgText
	argCount   int
	variadic   bool // 形如 args...，实参个数静态未知
	sqlShaped  bool // 通过 SQL 形状启发式过滤（Gin 的 c.Query 会被排除）
	argSpelled []string
}

// sqlgScanFile 扫描单个文件的所有数据库调用点。
func sqlgScanFile(fset *token.FileSet, f *ast.File) []sqlgCallSite {
	pkg := sqlgCollectPkgScope(f)

	var sites []sqlgCallSite
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		sc := sqlgScope{locals: sqlgCollectLocalScope(fn, pkg), pkg: pkg}

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !sqlgQueryMethods[sel.Sel.Name] {
				return true
			}
			// pgx 的签名是 (ctx, sql, args...)：第二个实参才是 SQL。
			// Gin 的 c.Query("limit") 只有一个实参、parsed.Query() 没有实参，
			// 这里一并排除。
			if len(call.Args) < 2 {
				return true
			}

			sql := sqlgResolveSQL(call.Args[1], sc)
			site := sqlgCallSite{
				pos:       fset.Position(call.Args[1].Pos()),
				receiver:  sqlgExprString(sel.X),
				method:    sel.Sel.Name,
				enclosing: fn.Name.Name,
				sql:       sql,
				sqlShaped: sqlgSQLShapeRe.MatchString(sqlgStripNoise(sql.text)),
				// CallExpr.Ellipsis 是 `args...` 里 "..." 的位置；未展开时为 NoPos。
				// 展开时实参个数取决于被展开切片的长度，静态不可知。
				variadic: call.Ellipsis.IsValid(),
			}
			for _, a := range call.Args[2:] {
				site.argSpelled = append(site.argSpelled, sqlgExprString(a))
			}
			site.argCount = len(call.Args) - 2
			sites = append(sites, site)
			return true
		})
	}
	return sites
}

// sqlgExprString 把表达式还原成可读的源码片段，用于报告。
func sqlgExprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return sqlgExprString(v.X) + "." + v.Sel.Name
	case *ast.CallExpr:
		return sqlgExprString(v.Fun) + "(…)"
	case *ast.BasicLit:
		if len(v.Value) > 24 {
			return v.Value[:24] + "…"
		}
		return v.Value
	}
	return "…"
}

// ───────────────────────── 判定 ─────────────────────────

// sqlgVerdict 是一次检查的结论。
type sqlgVerdict string

const (
	// sqlgOK：SQL 完整可解析，且占位符与实参一致。
	sqlgOK sqlgVerdict = "ok"
	// sqlgSkipped：不是 SQL 调用（同名方法，如 Gin 的 c.Query）。
	sqlgSkipped sqlgVerdict = "skipped-not-sql"
	// sqlgUnverifiable：SQL 无法完整静态解析，需人工审计。
	sqlgUnverifiable sqlgVerdict = "unverifiable-dynamic-sql"
	// sqlgFail：确证不一致。
	sqlgFail sqlgVerdict = "fail"
)

// sqlgFinding 是一条确证的问题。
type sqlgFinding struct {
	site   sqlgCallSite
	kind   string
	detail string
}

// sqlgCheckSite 对单个调用点出结论。
//
// 规则 1（gap）不要求 SQL 完整：只要解析出的部分已经能证明「1..maxN 中有缺口」，
// 那么无论未解析部分是什么，这条语句都会在 Parse 阶段失败 —— 这是本文件里
// 唯一一个可以在 SQL 不完整时依然做出**拒绝**判断的规则。
func sqlgCheckSite(site sqlgCallSite) (sqlgVerdict, *sqlgFinding) {
	if !site.sqlShaped {
		return sqlgSkipped, nil
	}

	nums := sqlgPlaceholderNums(site.sql.text)
	maxN := 0
	for n := range nums {
		if n > maxN {
			maxN = n
		}
	}

	// 规则 1：编号缺口（42P18 的直接成因）。
	var missing []int
	for i := 1; i <= maxN; i++ {
		if !nums[i] {
			missing = append(missing, i)
		}
	}
	if len(missing) > 0 {
		sort.Ints(missing)
		return sqlgFail, &sqlgFinding{
			site: site,
			kind: "placeholder-gap",
			detail: fmt.Sprintf(
				"SQL 出现最大编号 $%d，但 %s 从未被引用；PostgreSQL 无法推断其类型，"+
					"Parse 阶段即报 SQLSTATE 42P18，整条语句不会执行",
				maxN, joinInts(missing)),
		}
	}

	if !site.sql.complete {
		return sqlgUnverifiable, nil
	}
	if site.variadic {
		// 实参个数未知，只完成了 gap 检查（上一步已做）。
		return sqlgUnverifiable, nil
	}

	// 规则 2：占位符上界超过实参个数（LinkJob 的原始形态）。
	if maxN > site.argCount {
		return sqlgFail, &sqlgFinding{
			site: site,
			kind: "placeholder-above-args",
			detail: fmt.Sprintf(
				"SQL 引用到 $%d，但只传了 %d 个实参；$%d 无绑定 → SQLSTATE 42P18",
				maxN, site.argCount, site.argCount+1),
		}
	}

	// 规则 3：实参多于占位符。
	if site.argCount > maxN {
		return sqlgFail, &sqlgFinding{
			site: site,
			kind: "args-above-placeholders",
			detail: fmt.Sprintf(
				"传了 %d 个实参，但 SQL 只用到 $%d；pgx 报 \"supplies %d parameters "+
					"but statement requires %d\"",
				site.argCount, maxN, site.argCount, maxN),
		}
	}

	return sqlgOK, nil
}

func joinInts(ns []int) string {
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		parts = append(parts, "$"+strconv.Itoa(n))
	}
	return strings.Join(parts, ", ")
}

// ───────────────────────── 驱动 ─────────────────────────

// sqlgRepoRoot 从测试工作目录（包源码目录）向上找到含 go.mod 的目录。
func sqlgRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("未找到 go.mod（已从 %s 向上到文件系统根）", dir)
		}
		dir = parent
	}
}

// sqlgScanTree 遍历 root/internal 下所有 .go 文件，返回全部调用点。
func sqlgScanTree(t *testing.T, root string) []sqlgCallSite {
	t.Helper()
	base := filepath.Join(root, "internal")
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("internal 目录不存在: %v", err)
	}

	var sites []sqlgCallSite
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// node_modules 等第三方目录里可能有 .go 文件，与本仓无关。
			if name := d.Name(); name == "node_modules" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		sites = append(sites, sqlgScanFile(fset, f)...)
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 internal/ 失败: %v", err)
	}
	return sites
}

// sqlgRelPath 把绝对路径压成相对仓库根的短路径，报告更易读。
//
// 只取路径部分：token.Position.String() 形如 "file:line:col"，直接 RelPath 会
// 把 ":行:列" 当成文件名的一部分粘在后面，产出 "engine_run.go:247:52:247"
// 这种读不懂的定位（行号出现两次）。行号与列号由调用方单独格式化。
func sqlgRelPath(root string, pos token.Position) string {
	rel, err := filepath.Rel(root, pos.Filename)
	if err != nil {
		return pos.Filename
	}
	return rel
}

// TestSQLPlaceholderArity 是全仓护栏：任何确证的不一致都让 CI 变红。
//
// 报告（t.Logf）始终输出，可用 `go test -v` 审计扫描覆盖率 ——
// 尤其是 unverifiable 一档，它诚实地暴露了本护栏覆盖不到的位置。
func TestSQLPlaceholderArity(t *testing.T) {
	root := sqlgRepoRoot(t)
	sites := sqlgScanTree(t, root)

	counts := map[sqlgVerdict]int{}
	var findings []sqlgFinding
	var unverifiable []sqlgCallSite
	var skipped int

	for _, s := range sites {
		v, f := sqlgCheckSite(s)
		counts[v]++
		switch v {
		case sqlgFail:
			findings = append(findings, *f)
		case sqlgUnverifiable:
			unverifiable = append(unverifiable, s)
		case sqlgSkipped:
			skipped++
		}
	}

	t.Logf("扫描调用点：共 %d 个调用点 / 形状像 SQL 的 %d 个",
		len(sites), len(sites)-skipped)
	t.Logf("  ok=%d  unverifiable=%d  skipped(非SQL)=%d  fail=%d",
		counts[sqlgOK], counts[sqlgUnverifiable], skipped, counts[sqlgFail])

	if len(unverifiable) > 0 {
		t.Logf("以下 %d 处 SQL 由 Sprintf/动态拼接生成，静态无法确定占位符上界，"+
			"需人工审计（本护栏对其不做通过判定）：", len(unverifiable))
		for _, s := range unverifiable {
			t.Logf("    %s:%d:%d  %s.%s  args=%d variadic=%v",
				sqlgRelPath(root, s.pos), s.pos.Line, s.pos.Column,
				s.receiver, s.method, s.argCount, s.variadic)
		}
	}

	if len(findings) == 0 {
		return
	}

	for _, f := range findings {
		t.Errorf("SQL 占位符与实参不一致 [%s]\n  位置: %s:%d:%d\n  调用: %s.%s（函数 %s）\n  实参: (%s)\n  原因: %s",
			f.kind, sqlgRelPath(root, f.site.pos), f.site.pos.Line, f.site.pos.Column,
			f.site.receiver, f.site.method, f.site.enclosing,
			strings.Join(f.site.argSpelled, ", "), f.detail)
	}
}

// TestLinkJobSQLPlaceholderArity 是 LinkJob 的定点反证测试。
//
// 为什么它与上面的全仓扫描并存：全仓扫描给出「仓库当前是干净的」这一位信息，
// 而本测试把 LinkJob 的契约钉死成显式断言 —— 三参、$1/$2/$3 连续、实参恰好 3 个。
// 任何人把 $3 改回 $4（2026-10-13 线上故障的原始形态），两个测试同时变红。
func TestLinkJobSQLPlaceholderArity(t *testing.T) {
	root := sqlgRepoRoot(t)
	fset := token.NewFileSet()
	path := filepath.Join(root, "internal", "repository", "engine_run.go")

	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}

	var linkJob []sqlgCallSite
	for _, s := range sqlgScanFile(fset, f) {
		if s.enclosing == "LinkJob" {
			linkJob = append(linkJob, s)
		}
	}
	if len(linkJob) != 1 {
		t.Fatalf("期望在 LinkJob 中找到恰好 1 个数据库调用，实际 %d 个；"+
			"若 LinkJob 重构了，请同步更新本测试的契约", len(linkJob))
	}
	site := linkJob[0]

	if !site.sql.complete {
		t.Fatalf("LinkJob 的 SQL 无法静态解析（可能被改成了动态拼接）；" +
			"本测试依赖它是字面量常量，请同步更新契约")
	}

	nums := sqlgPlaceholderNums(site.sql.text)
	if len(nums) != 3 {
		t.Errorf("LinkJob 的 SQL 应恰好引用 3 个占位符，实际 %d 个: %v"+
			"\n  提示：参数编号笔误会让 $N 在语句中无绑定，"+
			"PostgreSQL 报 SQLSTATE 42P18（2026-10-13 线上故障）",
			len(nums), nums)
	}
	for _, want := range []int{1, 2, 3} {
		if !nums[want] {
			t.Errorf("LinkJob 的 SQL 缺少 $%d（实际出现: %v）", want, nums)
		}
	}
	if site.variadic {
		t.Errorf("LinkJob 的实参不应以 `...` 展开，否则个数无法与占位符对齐")
	}
	if site.argCount != 3 {
		t.Errorf("LinkJob 应传 3 个实参 (runID, userID, jobID)，实际 %d 个: (%s)",
			site.argCount, strings.Join(site.argSpelled, ", "))
	}

	// 幂等语义不许被"顺手优化"掉：守卫子句必须保留 job_id = '' 分支，
	// 否则重放同一 job 会直接 ErrNoRows，而不是写成相同值。
	if !strings.Contains(site.sql.text, "job_id = ''") {
		t.Errorf("LinkJob 的幂等守卫 `(job_id = '' OR job_id = $3)` 丢失了空值分支；" +
			"重放将返回 ErrNoRows，破坏 U5 的幂等契约")
	}
	if strings.Contains(site.sql.text, "$4") {
		t.Errorf("LinkJob 的 SQL 仍引用 $4，但只有 3 个实参 —— 这正是 2026-10-13 的故障形态")
	}
}

// TestSQLStripNoiseLexer 单测词法层：注释与字面量里的 $N 必须被抹掉，
// 否则护栏会因假阳性而失去可信度（假阳性 → 被忽略 → 护栏报废）。
func TestSQLStripNoiseLexer(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []int
	}{
		{
			name: "真实占位符",
			sql:  "UPDATE t SET job_id = $3 WHERE id = $1 AND u = $2",
			want: []int{1, 2, 3},
		},
		{
			name: "同一占位符复用只算一个",
			sql:  "DELETE FROM a WHERE user_id = $1 OR expert_id = $1",
			want: []int{1},
		},
		{
			name: "行注释里的编号不算",
			sql:  "SELECT 1 -- TODO: bump to $5\nWHERE id = $1",
			want: []int{1},
		},
		{
			name: "块注释里的编号不算",
			sql:  "SELECT /* $9 is wrong */ 1 WHERE id = $2",
			want: []int{2},
		},
		{
			name: "字符串字面量里的编号不算",
			sql:  "SELECT '$7' FROM t WHERE id = $1",
			want: []int{1},
		},
		{
			name: "转义单引号不结束字符串",
			sql:  "SELECT 'it''s $4' FROM t WHERE id = $1",
			want: []int{1},
		},
		{
			name: "quoted identifier 不算",
			sql:  `SELECT "col$8" FROM t WHERE id = $1`,
			want: []int{1},
		},
		{
			name: "dollar-quoted 段不算",
			sql:  "SELECT $tag$ $6 $tag$ FROM t WHERE id = $1",
			want: []int{1},
		},
		{
			name: "Sprintf 动词不被误认成占位符",
			sql:  "SELECT 1 LIMIT $%d OFFSET $%d",
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sqlgPlaceholderNums(tc.sql)
			if len(got) != len(tc.want) {
				t.Fatalf("占位符集合 = %v，期望 %v", got, tc.want)
			}
			for _, n := range tc.want {
				if !got[n] {
					t.Errorf("缺少期望的占位符 $%d（实际 %v）", n, got)
				}
			}
		})
	}
}

// TestSQLStripNoisePreservesLength 保证抹除是等长替换：护栏报告里的行号
// 与列位置仍与源码对齐，且 substring 之类的下标运算不会被悄悄改变。
func TestSQLStripNoisePreservesLength(t *testing.T) {
	src := "SELECT 'a''b $1', \"c$2\" /* $3 */ -- $4\nFROM t WHERE id = $5"
	if got := len(sqlgStripNoise(src)); got != len(src) {
		t.Errorf("长度被改变：原文 %d 字节，处理后 %d 字节", len(src), got)
	}
	if nums := sqlgPlaceholderNums(src); len(nums) != 1 || !nums[5] {
		t.Errorf("期望只保留 {5}，实际 %v", nums)
	}
}
