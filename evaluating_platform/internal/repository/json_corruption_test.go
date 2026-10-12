package repository

// json_corruption_test.go — 损坏 JSONB 的行为护栏（审计 2026-10-12 P1-1/2/3/6）。
//
// # 本机无法验证的部分（如实登记）
//
// 本机**没有 PostgreSQL**，所以这些测试不打真库，而是用 fake rowsScanner
// 直接驱动生产扫描函数（scanEngineRunRows / scanMaclawResourceRecords /
// scanTemplateRows）。这是有意的选择而非妥协：
//
//   - 被测函数（scanEngineRun → decodeCriticalJSONB 等）就是真库路径上
//     唯一执行解码的代码，fake 只替换了「字节从哪来」；
//   - 护栏纪律要求「打生产出口，不是打 helper」—— scanEngineRunRows 是
//     List 的唯一出口，scanOneMaclawResourceRecord 是三个读方法的共同出口，
//     因此断言落在这一层而非 decode* helper 上。
//
// 未覆盖：真实 pgx 的类型映射与 SQL 语义（例如 jsonb 列被驱动返回什么形态）。
// 这一项需要一台有 PG 的机器补跑，已作为遗留登记。

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
	"evaluating_platform/internal/model"
)

// ───────────────────────── fake rows ─────────────────────────

// fakeRow 是一行待扫描的数据：按顺序把值写入 Scan 的目标指针。
//
// 用 reflect 是为了避免为 27 个字段手写27 个 switch 分支——那样的话一旦
// 增删列，测试会静默错位而不是失败。reflect 版本在数量不匹配时直接报错。
type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("fakeRow: dest/values length mismatch")
	}
	for i := range dest {
		if err := assignValue(dest[i], r.values[i]); err != nil {
			return err
		}
	}
	return nil
}

// fakeRows 是 rowsScanner 的最小实现，按行依次返回。
type fakeRows struct {
	rows    []fakeRow
	idx     int
	iterErr error
}

func (f *fakeRows) Next() bool {
	if f.idx >= len(f.rows) {
		return false
	}
	f.idx++
	return true
}

func (f *fakeRows) Scan(dest ...any) error { return f.rows[f.idx-1].Scan(dest...) }
func (f *fakeRows) Err() error             { return f.iterErr }

// ───────────────────── engine_runs 测试 ─────────────────────

// engineRunRowValues 按 engineRunColumns 的顺序构造一行扫描值。
func engineRunRowValues(t *testing.T, id, plugins, strategies, stageDurations, tokenUsage, result string) []any {
	t.Helper()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	return []any{
		id,
		uuid.New(),
		"inst-1",
		"sess-1",
		"wizard",
		"",
		"promptfoo",
		string(maclaw.EnginePhaseSucceeded),
		"auto",
		"regression",
		[]byte(plugins),
		[]byte(strategies),
		"target-1",
		20,
		20,
		20,
		"report",
		int64(1234),
		[]byte(stageDurations),
		[]byte(tokenUsage),
		[]byte(result),
		"",
		"",
		&now,
		&now,
		now,
		now,
	}
}

const (
	goodPlugins    = `[{"id":"ccbos-system-prompt-leak","label":"BOS"}]`
	goodStrategies = `[{"id":"multilingual"}]`
	goodStages     = `{"generate":100,"execute":900}`
	goodTokens     = `{"generation":10,"judging":20,"target":30}`
	goodResult     = `{"totals":{"total":20}}`
)

// TestScanEngineRunRowsHealthyRow 覆盖正常行：字段被正确解析。
//
// 这是所有损坏用例的对照基准 —— 若解码整体坏掉，损坏用例的「跳过」
// 断言也会跟着通过，所以必须先钉住健康路径。
func TestScanEngineRunRowsHealthyRow(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{{values: engineRunRowValues(t, "er-1", goodPlugins, goodStrategies, goodStages, goodTokens, goodResult)}}}

	items, err := scanEngineRunRows(rows)
	if err != nil {
		t.Fatalf("scanEngineRunRows error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if len(items[0].Plugins) != 1 || items[0].Plugins[0].ID != "ccbos-system-prompt-leak" {
		t.Fatalf("Plugins = %+v, want one ccbos-system-prompt-leak", items[0].Plugins)
	}
	if len(items[0].Strategies) != 1 || items[0].Strategies[0].ID != "multilingual" {
		t.Fatalf("Strategies = %+v, want one multilingual", items[0].Strategies)
	}
	if items[0].Result == nil {
		t.Fatal("Result = nil, want parsed safe result")
	}
	if items[0].TokenUsage.Judging != 20 {
		t.Fatalf("TokenUsage.Judging = %d, want 20", items[0].TokenUsage.Judging)
	}
}

// TestScanEngineRunRowsCorruptCriticalColumnFailsClosed 是 P1-2 的核心反证。
//
// 逐个损坏**关键列**，断言：行被跳过 + 上抛可定位的 CorruptRowError。
//
// 反证含义：修复前这些列被静默零值化，调用方拿到的是「plugins 为空的
// 正常记录」；修复后必须明确失败。若这个测试失败（即某列损坏却仍返回
// 记录），说明fail-closed 被回退成了静默降级。
func TestScanEngineRunRowsCorruptCriticalColumnFailsClosed(t *testing.T) {
	cases := []struct {
		name       string
		plugins    string
		strategies string
		result     string
		wantField  string
	}{
		{"corrupt plugins", `{"broken":`, goodStrategies, goodResult, "plugins"},
		{"corrupt strategies", goodPlugins, `[not json`, goodResult, "strategies"},
		{"corrupt result", goodPlugins, goodStrategies, `{"totals":`, "result"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := &fakeRows{rows: []fakeRow{{
				values: engineRunRowValues(t, "er-bad", c.plugins, c.strategies, goodStages, goodTokens, c.result),
			}}}

			items, err := scanEngineRunRows(rows)
			// 降级错误必须被上抛（否则等于把静默吞噬换了个地方）。
			if err == nil {
				t.Fatalf("scanEngineRunRows error = nil, want partial corruption error")
			}
			if !maclaw.IsPartialCorruption(err) {
				t.Fatalf("error must satisfy maclaw.PartialCorruption, got %T: %v", err, err)
			}
			// 坏行不得出现在结果里 —— 这是 fail-closed 的实质。
			if len(items) != 0 {
				t.Fatalf("len(items) = %d, want 0 (corrupt row must not be returned as healthy)", len(items))
			}
			// 必须能定位到具体列。
			var corrupt *CorruptRowError
			if !errors.As(err, &corrupt) {
				t.Fatalf("error must unwrap to *CorruptRowError, got %T", err)
			}
			if corrupt.Field != c.wantField {
				t.Fatalf("corrupt.Field = %q, want %q", corrupt.Field, c.wantField)
			}
			if corrupt.RowID != "er-bad" {
				t.Fatalf("corrupt.RowID = %q, want %q", corrupt.RowID, "er-bad")
			}
			if corrupt.Table != "engine_runs" {
				t.Fatalf("corrupt.Table = %q, want %q", corrupt.Table, "engine_runs")
			}
		})
	}
}

// TestScanEngineRunRowsCorruptDegradableColumnKeepsRow 覆盖展示性列的降级。
//
// stage_durations / token_usage 损坏时：行**保留**（不 500），
// 字段为零值，且不产生降级错误（因为它们不影响任何判定）。
//
// 为什么允许降级：它们是进度遥测。丢掉「每阶段耗时」只是少一段图表，
// 而丢掉整个 run 列表可用性代价高得多。但降级**必须记日志**——
// decodeDegradableJSONB 里的 logger.Error 就是这个约束的落点。
func TestScanEngineRunRowsCorruptDegradableColumnKeepsRow(t *testing.T) {
	for _, c := range []struct{ name, stages, tokens string }{
		{"corrupt stage_durations", `{broken`, goodTokens},
		{"corrupt token_usage", goodStages, `{"generation":`},
		{"both corrupt", `{`, `}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			rows := &fakeRows{rows: []fakeRow{{
				values: engineRunRowValues(t, "er-degraded", goodPlugins, goodStrategies, c.stages, c.tokens, goodResult),
			}}}

			items, err := scanEngineRunRows(rows)
			if err != nil {
				t.Fatalf("degradable corruption must not fail the row, got err = %v", err)
			}
			if len(items) != 1 {
				t.Fatalf("len(items) = %d, want 1 (degradable corruption keeps the row)", len(items))
			}
			// 关键列仍须正确 —— 降级不能传染。
			if len(items[0].Plugins) != 1 {
				t.Fatalf("Plugins = %+v, want intact", items[0].Plugins)
			}
			if items[0].Result == nil {
				t.Fatal("Result = nil, want intact")
			}
		})
	}
}

// TestScanEngineRunRowsSkipsBadRowKeepsGoodRows 是本次任务的核心约束：
// **一条脏数据不能让整个列表打不开**。
func TestScanEngineRunRowsSkipsBadRowKeepsGoodRows(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{
		{values: engineRunRowValues(t, "er-good-1", goodPlugins, goodStrategies, goodStages, goodTokens, goodResult)},
		{values: engineRunRowValues(t, "er-bad", `{broken`, goodStrategies, goodStages, goodTokens, goodResult)},
		{values: engineRunRowValues(t, "er-good-2", goodPlugins, goodStrategies, goodStages, goodTokens, goodResult)},
	}}

	items, err := scanEngineRunRows(rows)
	if !maclaw.IsPartialCorruption(err) {
		t.Fatalf("want partial corruption error, got %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2 (two healthy rows survive)", len(items))
	}
	if items[0].ID != "er-good-1" || items[1].ID != "er-good-2" {
		t.Fatalf("surviving ids = %q,%q want er-good-1,er-good-2", items[0].ID, items[1].ID)
	}
	// 错误必须指明被跳过的是哪一行。
	if got := err.Error(); !contains(got, "er-bad") {
		t.Fatalf("error message must name the bad row, got %q", got)
	}
}

// TestScanEngineRunRowsAllRowsHealthyYieldsNoError 覆盖「没有坏行时不产生
// 任何降级错误」—— 否则每次正常列表查询都会打一条 ERROR 日志，
// 久而久之真出事时反而被淹没（告警疲劳）。
func TestScanEngineRunRowsAllRowsHealthyYieldsNoError(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{
		{values: engineRunRowValues(t, "er-1", goodPlugins, goodStrategies, goodStages, goodTokens, goodResult)},
		{values: engineRunRowValues(t, "er-2", goodPlugins, goodStrategies, goodStages, goodTokens, goodResult)},
	}}

	items, err := scanEngineRunRows(rows)
	if err != nil {
		t.Fatalf("healthy rows must not produce an error, got %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
}

// TestScanEngineRunRowsEmptyResultObjectIsNotCorruption 覆盖 `'{}'` 语义。
//
// result 列是 NOT NULL DEFAULT '{}'，即「还没有结果」。若把它当损坏，
// 所有刚创建的 run 都会被误判为脏行。
func TestScanEngineRunRowsEmptyResultObjectIsNotCorruption(t *testing.T) {
	for _, result := range []string{"{}", ""} {
		rows := &fakeRows{rows: []fakeRow{{
			values: engineRunRowValues(t, "er-new", goodPlugins, goodStrategies, goodStages, goodTokens, result),
		}}}
		items, err := scanEngineRunRows(rows)
		if err != nil {
			t.Fatalf("result=%q must not be treated as corruption, got %v", result, err)
		}
		if len(items) != 1 {
			t.Fatalf("result=%q: len(items) = %d, want 1", result, len(items))
		}
		if items[0].Result != nil {
			t.Fatalf("result=%q: Result = %+v, want nil (no result yet)", result, items[0].Result)
		}
	}
}

// TestScanEngineRunRowsIterationErrorFailsClosed 覆盖非损坏类错误不被降级。
//
// 连接中断 / Scan 失败不是「部分可用」，必须整体失败。若这里也走降级
// 路径，数据库故障会伪装成「列表少了几行」，极难排查。
func TestScanEngineRunRowsIterationErrorFailsClosed(t *testing.T) {
	rows := &fakeRows{
		rows:    []fakeRow{{values: engineRunRowValues(t, "er-1", goodPlugins, goodStrategies, goodStages, goodTokens, goodResult)}},
		iterErr: errors.New("connection reset by peer"),
	}

	items, err := scanEngineRunRows(rows)
	if err == nil {
		t.Fatal("iteration error must fail closed, got nil")
	}
	if maclaw.IsPartialCorruption(err) {
		t.Fatal("iteration error must NOT be classified as partial corruption")
	}
	if items != nil {
		t.Fatalf("items = %+v, want nil on hard failure", items)
	}
}

// TestScanEngineRunRowsEmptyResultSet 覆盖空结果集：返回空切片而非 nil，
// 且无错误。
func TestScanEngineRunRowsEmptyResultSet(t *testing.T) {
	items, err := scanEngineRunRows(&fakeRows{})
	if err != nil {
		t.Fatalf("empty set error = %v", err)
	}
	if items == nil {
		t.Fatal("items = nil, want empty non-nil slice")
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}
}

// ───────────────────── 写路径 fail-closed ─────────────────────

// TestMarshalEngineRunBlobsFailsClosedOnUnmarshalableValue 覆盖 P1-1 写路径。
//
// 构造一个 json.Marshal **必然失败**的 Plugins（Config 里塞 channel），
// 断言 marshalEngineRunBlobs 返回错误而不是静默写出 nil。
//
// 为什么这条重要：修复前是 `plugins, _ = json.Marshal(...)`——失败时写入
// nil，DB 里落下一行 plugins 为 NULL 的记录，而调用方拿到成功返回。
// 现在必须失败，才有机会不落库。
func TestMarshalEngineRunBlobsFailsClosedOnUnmarshalableValue(t *testing.T) {
	record := maclaw.EngineRunRecord{
		ID: "er-bad",
		Plugins: []maclaw.EngineCapabilityRef{{
			ID:     "broken",
			Config: map[string]interface{}{"ch": make(chan int)}, // json 无法编码
		}},
	}

	blobs, err := marshalEngineRunBlobs(record)
	if err == nil {
		t.Fatalf("marshalEngineRunBlobs error = nil, want marshal failure (blobs = %+v)", blobs)
	}
	if !contains(err.Error(), "plugins") {
		t.Fatalf("error must name the failing column, got %q", err.Error())
	}
}

// TestMarshalEngineRunBlobsHealthy 覆盖正常编码，并钉住两个既有语义：
//   - nil Result 必须写成 '{}'（该列是 NOT NULL DEFAULT '{}'）；
//   - 其余列必须是合法 JSON。
func TestMarshalEngineRunBlobsHealthy(t *testing.T) {
	blobs, err := marshalEngineRunBlobs(maclaw.EngineRunRecord{
		ID:         "er-ok",
		Plugins:    []maclaw.EngineCapabilityRef{{ID: "p1"}},
		Strategies: []maclaw.EngineCapabilityRef{{ID: "s1"}},
	})
	if err != nil {
		t.Fatalf("marshalEngineRunBlobs error = %v", err)
	}
	if string(blobs.result) != "{}" {
		t.Fatalf("result = %q, want %q for a nil Result", blobs.result, "{}")
	}
	var decoded []maclaw.EngineCapabilityRef
	if err := json.Unmarshal(blobs.plugins, &decoded); err != nil {
		t.Fatalf("plugins blob is not valid JSON: %v", err)
	}
	if len(decoded) != 1 || decoded[0].ID != "p1" {
		t.Fatalf("plugins blob = %q", blobs.plugins)
	}
}

// TestMarshalEngineRunBlobsRoundTripsThroughScan 覆盖写→读闭环。
//
// 把 marshal 的输出直接喂给 scanEngineRun，断言字段无损。这条防止
// 「写出来的 JSONB 读回来不一样」这类只在真库才暴露的问题。
func TestMarshalEngineRunBlobsRoundTripsThroughScan(t *testing.T) {
	record := maclaw.EngineRunRecord{
		ID:         "er-rt",
		Plugins:    []maclaw.EngineCapabilityRef{{ID: "ccbos-x", Config: map[string]interface{}{"label": "X"}}},
		Strategies: []maclaw.EngineCapabilityRef{{ID: "strat-y"}},
		Result:     &maclaw.EngineSafeResult{Totals: maclaw.EngineRunTotals{Probes: 7, AttackSuccess: 3}},
	}
	blobs, err := marshalEngineRunBlobs(record)
	if err != nil {
		t.Fatalf("marshalEngineRunBlobs error = %v", err)
	}

	rows := &fakeRows{rows: []fakeRow{{values: engineRunRowValues(t, "er-rt",
		string(blobs.plugins), string(blobs.strategies),
		string(blobs.stageDurations), string(blobs.tokenUsage), string(blobs.result))}}}

	items, err := scanEngineRunRows(rows)
	if err != nil {
		t.Fatalf("scanEngineRunRows error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	got := items[0]
	if len(got.Plugins) != 1 || got.Plugins[0].ID != "ccbos-x" {
		t.Fatalf("Plugins round trip = %+v", got.Plugins)
	}
	if got.Plugins[0].Config["label"] != "X" {
		t.Fatalf("Plugins[0].Config round trip = %+v", got.Plugins[0].Config)
	}
	if got.Result == nil || got.Result.Totals.Probes != 7 || got.Result.Totals.AttackSuccess != 3 {
		t.Fatalf("Result round trip = %+v", got.Result)
	}
}

// ───────────────── maclaw_resources 测试 ─────────────────

// resourceRowValues 按资源表的 SELECT 顺序构造一行扫描值。
func resourceRowValues(t *testing.T, id uuid.UUID, assessmentTypes, tags, metadata string) []any {
	t.Helper()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	return []any{
		id,
		uuid.New(),
		"tenant-1",
		"handle-1",
		"Resource One",
		"desc",
		"plugin",
		"v1",
		"published",
		true,
		"healthy",
		[]byte(assessmentTypes),
		[]byte(tags),
		"summary",
		[]byte("encrypted-bytes"),
		"v1",
		[]byte(metadata),
		now,
		now,
	}
}

// TestScanMaclawResourceRecordsHealthy 覆盖正常行。
func TestScanMaclawResourceRecordsHealthy(t *testing.T) {
	id := uuid.New()
	rows := &fakeRows{rows: []fakeRow{{
		values: resourceRowValues(t, id, `["jailbreak","leak"]`, `["a","b"]`, `{"k":"v"}`),
	}}}

	items, err := scanMaclawResourceRecords(rows)
	if err != nil {
		t.Fatalf("scanMaclawResourceRecords error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if len(items[0].AssessmentTypes) != 2 {
		t.Fatalf("AssessmentTypes = %+v, want 2 entries", items[0].AssessmentTypes)
	}
	if items[0].Metadata["k"] != "v" {
		t.Fatalf("Metadata = %+v", items[0].Metadata)
	}
}

// TestScanMaclawResourceRecordsCorruptAssessmentTypesFailsClosed 覆盖 P1-3
// 的关键列：损坏即跳过并上抛可定位错误。
func TestScanMaclawResourceRecordsCorruptAssessmentTypesFailsClosed(t *testing.T) {
	id := uuid.New()
	rows := &fakeRows{rows: []fakeRow{{
		values: resourceRowValues(t, id, `["broken`, `["a"]`, `{}`),
	}}}

	items, err := scanMaclawResourceRecords(rows)
	if !maclaw.IsPartialCorruption(err) {
		t.Fatalf("want partial corruption error, got %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0 (corrupt row must be skipped)", len(items))
	}
	var corrupt *CorruptRowError
	if !errors.As(err, &corrupt) {
		t.Fatalf("error must unwrap to *CorruptRowError, got %T", err)
	}
	if corrupt.Field != "assessment_types" {
		t.Fatalf("corrupt.Field = %q, want assessment_types", corrupt.Field)
	}
	if corrupt.RowID != id.String() {
		t.Fatalf("corrupt.RowID = %q, want %q", corrupt.RowID, id.String())
	}
}

// TestScanMaclawResourceRecordsCorruptDegradableKeepsRow 覆盖 tags / metadata
// 损坏时降级保行。
func TestScanMaclawResourceRecordsCorruptDegradableKeepsRow(t *testing.T) {
	id := uuid.New()
	rows := &fakeRows{rows: []fakeRow{{
		values: resourceRowValues(t, id, `["jailbreak"]`, `[broken`, `{"k":`),
	}}}

	items, err := scanMaclawResourceRecords(rows)
	if err != nil {
		t.Fatalf("degradable corruption must not fail the row, got %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	// metadata 损坏后仍必须是非 nil map（下游会直接写它）。
	if items[0].Metadata == nil {
		t.Fatal("Metadata = nil, want non-nil map (downstream writes to it)")
	}
	// 关键列不受影响。
	if len(items[0].AssessmentTypes) != 1 {
		t.Fatalf("AssessmentTypes = %+v, want intact", items[0].AssessmentTypes)
	}
}

// TestScanMaclawResourceRecordsSkipsBadKeepsGood 覆盖「一条坏行不毁整批」。
func TestScanMaclawResourceRecordsSkipsBadKeepsGood(t *testing.T) {
	good1, bad, good2 := uuid.New(), uuid.New(), uuid.New()
	rows := &fakeRows{rows: []fakeRow{
		{values: resourceRowValues(t, good1, `["a"]`, `[]`, `{}`)},
		{values: resourceRowValues(t, bad, `{oops`, `[]`, `{}`)},
		{values: resourceRowValues(t, good2, `["b"]`, `[]`, `{}`)},
	}}

	items, err := scanMaclawResourceRecords(rows)
	if !maclaw.IsPartialCorruption(err) {
		t.Fatalf("want partial corruption error, got %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0].ID != good1 || items[1].ID != good2 {
		t.Fatalf("surviving ids = %v,%v want %v,%v", items[0].ID, items[1].ID, good1, good2)
	}
}

// TestScanMaclawResourceRecordsIterationErrorFailsClosed 覆盖硬错误不被降级。
func TestScanMaclawResourceRecordsIterationErrorFailsClosed(t *testing.T) {
	rows := &fakeRows{
		rows:    []fakeRow{{values: resourceRowValues(t, uuid.New(), `["a"]`, `[]`, `{}`)}},
		iterErr: errors.New("conn reset"),
	}
	items, err := scanMaclawResourceRecords(rows)
	if err == nil {
		t.Fatal("iteration error must fail closed")
	}
	if maclaw.IsPartialCorruption(err) {
		t.Fatal("iteration error must NOT be partial corruption")
	}
	if items != nil {
		t.Fatalf("items = %+v, want nil", items)
	}
}

// ───────────────────── templates 测试 ─────────────────────

// templateRowValues 按 tplSelectCols 顺序构造一行扫描值。
func templateRowValues(t *testing.T, id uuid.UUID, variables string) []any {
	t.Helper()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	return []any{
		id,
		uuid.New(),
		uuid.New(),
		"batch-1",
		"attack",
		"Template One",
		"desc",
		"content with {{var}}",
		[]byte(variables),
		"published",
		"public",
		now,
		now,
	}
}

// TestScanTemplateRowsHealthy 覆盖正常模板行。
func TestScanTemplateRowsHealthy(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{{
		values: templateRowValues(t, uuid.New(), `[{"name":"var","required":true}]`),
	}}}

	var out []model.Template
	corrupt, err := scanTemplateRows(rows, &out)
	if err != nil {
		t.Fatalf("scanTemplateRows error = %v", err)
	}
	if len(corrupt) != 0 {
		t.Fatalf("corrupt = %+v, want empty", corrupt)
	}
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	if len(out[0].Variables) != 1 || out[0].Variables[0].Name != "var" {
		t.Fatalf("Variables = %+v", out[0].Variables)
	}
}

// TestScanTemplateRowsCorruptVariablesFailsClosed 覆盖审计 #6。
//
// variables 损坏 → 行被跳过 + 上抛可定位错误。
//
// 为什么这是关键列：模板变量归零后，渲染时不做任何替换，真正发往靶场
// 的攻击文本与被评审的那份悄悄不同——评测照跑，测的却是另一个东西。
func TestScanTemplateRowsCorruptVariablesFailsClosed(t *testing.T) {
	id := uuid.New()
	rows := &fakeRows{rows: []fakeRow{{values: templateRowValues(t, id, `[{"name":`)}}}

	var out []model.Template
	corrupt, err := scanTemplateRows(rows, &out)
	if err != nil {
		t.Fatalf("scanTemplateRows error = %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("len(out) = %d, want 0 (corrupt template must be skipped)", len(out))
	}
	if len(corrupt) != 1 {
		t.Fatalf("len(corrupt) = %d, want 1", len(corrupt))
	}
	if corrupt[0].Field != "variables" {
		t.Fatalf("corrupt.Field = %q, want variables", corrupt[0].Field)
	}
	if corrupt[0].RowID != id.String() {
		t.Fatalf("corrupt.RowID = %q, want %q", corrupt[0].RowID, id.String())
	}
}

// TestScanTemplateRowsSkipsBadKeepsGood 覆盖列表不被单条坏模板拖垮。
func TestScanTemplateRowsSkipsBadKeepsGood(t *testing.T) {
	good1, bad, good2 := uuid.New(), uuid.New(), uuid.New()
	rows := &fakeRows{rows: []fakeRow{
		{values: templateRowValues(t, good1, `[{"name":"a"}]`)},
		{values: templateRowValues(t, bad, `{oops`)},
		{values: templateRowValues(t, good2, `[{"name":"b"}]`)},
	}}

	var out []model.Template
	corrupt, err := scanTemplateRows(rows, &out)
	if err != nil {
		t.Fatalf("scanTemplateRows error = %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if len(corrupt) != 1 {
		t.Fatalf("len(corrupt) = %d, want 1", len(corrupt))
	}
	// 降级错误必须能被聚合上抛。
	if !maclaw.IsPartialCorruption(newPartialCorruptionError("templates", corrupt)) {
		t.Fatal("newPartialCorruptionError must satisfy maclaw.PartialCorruption")
	}
}

// TestNewPartialCorruptionErrorNilWhenClean 覆盖「无坏行则不产生错误」。
//
// 这条很重要：否则每次正常列表查询都会返回非 nil error，调用方的
// `err != nil` 分支会被永久点亮。
func TestNewPartialCorruptionErrorNilWhenClean(t *testing.T) {
	if err := newPartialCorruptionError("t", nil); err != nil {
		t.Fatalf("newPartialCorruptionError(nil) = %v, want nil", err)
	}
	if err := newPartialCorruptionError("t", []*CorruptRowError{}); err != nil {
		t.Fatalf("newPartialCorruptionError(empty) = %v, want nil", err)
	}
}

// TestIsPartialCorruptionRejectsPlainErrors 覆盖判定不会误伤普通错误。
//
// 若普通 error 也被判为降级，数据库故障就会被静默吞掉 —— 那正是本任务
// 要消灭的行为。
func TestIsPartialCorruptionRejectsPlainErrors(t *testing.T) {
	for _, err := range []error{
		nil,
		errors.New("plain failure"),
		newCorruptRowError("t", "r", "c", errors.New("json")),
	} {
		if maclaw.IsPartialCorruption(err) {
			t.Fatalf("IsPartialCorruption(%v) = true, want false", err)
		}
	}
}

// TestPartialCorruptionErrorUnwrapsToEveryCause 覆盖多错误 Unwrap。
//
// 调用方需要能逐条取出被跳过的行做告警聚合，因此 Unwrap 必须返回全部。
func TestPartialCorruptionErrorUnwrapsToEveryCause(t *testing.T) {
	partial := &PartialCorruptionError{
		Table:   "engine_runs",
		Skipped: 2,
		Causes: []*CorruptRowError{
			newCorruptRowError("engine_runs", "er-1", "plugins", errors.New("bad json")),
			newCorruptRowError("engine_runs", "er-2", "result", errors.New("bad json")),
		},
	}

	seen := 0
	for _, cause := range partial.Unwrap() {
		var corrupt *CorruptRowError
		if !errors.As(cause, &corrupt) {
			t.Fatalf("cause %v is not a *CorruptRowError", cause)
		}
		seen++
	}
	if seen != 2 {
		t.Fatalf("Unwrap yielded %d causes, want 2", seen)
	}
	// 消息里必须同时出现表名与被跳过的行数。
	msg := partial.Error()
	if !contains(msg, "engine_runs") || !contains(msg, "er-1") || !contains(msg, "er-2") {
		t.Fatalf("Error() = %q, want table and row ids", msg)
	}
}

// ───────────────────────── helpers ─────────────────────────

// contains 是 strings.Contains 的本地短别名，避免为一个断言引入 import。
func contains(haystack, needle string) bool {
	return len(needle) == 0 || indexOf(haystack, needle) >= 0
}

// indexOf 返回 needle 在 haystack 中的首字节下标，未找到返回 -1。
func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestCreateFailsClosedBeforeTouchingPool 在**生产出口**验证 Create 在
// marshal 失败时不接触 DB 就返回错误（P1-1 写路径）。
//
// 为什么断言错误「来源」而不只是 err != nil：repo 的 pool 为 nil 时，
// 即使 marshal 错误被吞掉，后续 QueryRow 也会失败 —— 那样断言会
// 「因为错误的原因而通过」，成为假护栏（conditional 断言破口）。
// 必须断言错误来自 marshal 阶段，才真正锁住 fail-closed。
//
// 已实测反证：把 Create 改成 `blobs, _ := marshalEngineRunBlobs(record)`
// 后，本测试因执行流抵达 pool.QueryRow 而 panic（nil pointer），
// 即该护栏能真实捕获「错误被吞掉」的回归。
func TestCreateFailsClosedBeforeTouchingPool(t *testing.T) {
	record := maclaw.EngineRunRecord{
		ID:      "er-probe",
		Plugins: []maclaw.EngineCapabilityRef{{ID: "b", Config: map[string]interface{}{"ch": make(chan int)}}},
	}
	repo := &EngineRunRepository{}
	_, err := repo.Create(t.Context(), record)
	if err == nil {
		t.Fatal("Create must fail closed on marshal error")
	}
	if !contains(err.Error(), "marshal engine run plugins") {
		t.Fatalf("error must come from the marshal stage, got %q", err.Error())
	}
}

// TestScanEngineRunSingleRowFailsClosed 覆盖**单行**读出口。
//
// 与列表不同，单行读（Get / GetByJobID / LinkJob）面对损坏行时必须
// 直接失败，而不是返回一条「字段为空的正常记录」——
// 用户是点名来查这一条 run 的，拿到一个空 plugins 的 200 响应，
// 会以为「这次没跑任何插件」，即误读为安全。
//
// 列表之所以可以降级，是因为「少一行」远小于「整个页面打不开」；
// 单行没有这种权衡空间。
func TestScanEngineRunSingleRowFailsClosed(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{{
		values: engineRunRowValues(t, "er-single", `{"broken":`, goodStrategies, goodStages, goodTokens, goodResult),
	}}}
	// 单行读的形态是 QueryRow：取出唯一一行后直接交给 scanEngineRun。
	if !rows.Next() {
		t.Fatal("fake row set must yield one row")
	}

	got, err := scanEngineRun(rows)
	if err == nil {
		t.Fatalf("single-row read must fail closed, got record %+v", got)
	}
	if got != nil {
		t.Fatalf("failed single-row read must not return a record, got %+v", got)
	}
	var corrupt *CorruptRowError
	if !errors.As(err, &corrupt) {
		t.Fatalf("error must unwrap to *CorruptRowError, got %T", err)
	}
	if corrupt.RowID != "er-single" || corrupt.Field != "plugins" {
		t.Fatalf("corrupt = %+v, want row er-single column plugins", corrupt)
	}
}
