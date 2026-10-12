package repository

// json_corruption_projection_test.go — 红队报告 / 能力投影层的 JSONB 损坏
// 行为护栏（审计 2026-10-12 P1 收口批次）。
//
// # 为什么要单独一个文件
//
//第一批（json_corruption_test.go）覆盖 engine_runs / maclaw_resources /
// templates。本文件覆盖**用户最终读到的红队报告层**与**能力目录投影层**，
// 两者的分级判据与第一批不同，且都经消费者代码核实过（不是按字段名猜的）。
//
// 每个 CRITICAL 判定都在注释里写明「哪个消费者会把它变成什么错误结论」——
// 这是本项目一贯的判据要求：说不出后果的字段，不配fail-closed。

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
	"evaluating_platform/internal/model"
)

// ───────────────────── 红队报告 / 证据 ─────────────────────

// reportRowValues 按 scanRedteamReport 的 Scan 顺序构造一行。
func reportRowValues(t *testing.T, handle, findings, evidenceHandles, metadata string) []any {
	t.Helper()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	score := 88.5
	return []any{
		"rpt-" + handle,         // id
		handle,                  // handle
		uuid.New(),              // platform_user_id
		"inst-1",                // instance_id
		"sess-1",                // session_id
		"run-1",                 // run_id
		"Report Title",          // title
		"summary",               // summary
		"high",                  // risk_level
		&score,                  // safety_score (*float64)
		[]byte(findings),        // findings
		[]byte(evidenceHandles), // evidence_handles
		[]byte(metadata),        // metadata
		now,                     // created_at
		now,                     // updated_at
	}
}

// evidenceRowValues 按 scanRedteamEvidence 的 Scan 顺序构造一行。
func evidenceRowValues(t *testing.T, handle, metadata string) []any {
	t.Helper()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	return []any{
		"ev-" + handle,
		handle,
		uuid.New(),
		"inst-1",
		"sess-1",
		"run-1",
		"response",
		"Evidence Title",
		"summary",
		[]byte(metadata),
		now,
		now,
	}
}

const (
	goodFindings   = `[{"id":"f1","title":"Jailbreak","severity":"critical"}]`
	goodEvidence   = `["ev-a","ev-b"]`
	goodMeta       = `{"judge_track":"engine","engine":"promptfoo"}`
	goodEvMeta     = `{"k":"v"}`
	goodAssessType = `["jailbreak","leak"]`
	goodTags       = `["a","b"]`
	goodTriggers   = `["用户询问越狱","检测到注入尝试"]`
)

// TestScanRedteamReportHealthy 覆盖正常报告行。
//
// 所有损坏用例的对照基准：若解码整体坏掉，损坏用例的「跳过」断言也会跟着通过。
func TestScanRedteamReportHealthy(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{{values: reportRowValues(t, "h1", goodFindings, goodEvidence, goodMeta)}}}
	if !rows.Next() {
		t.Fatal("fake must yield one row")
	}

	report, err := scanRedteamReport(rows)
	if err != nil {
		t.Fatalf("scanRedteamReport error = %v", err)
	}
	if len(report.Findings) != 1 || report.Findings[0].Severity != "critical" {
		t.Fatalf("Findings = %+v, want one critical finding", report.Findings)
	}
	if len(report.EvidenceHandles) != 2 {
		t.Fatalf("EvidenceHandles = %+v, want 2", report.EvidenceHandles)
	}
	// judge_track 必须能被解析回显式口径 —— 这是 metadata 升级为 CRITICAL 的根据。
	if got := maclaw.RedteamJudgeTrackOf(report.Metadata); got != maclaw.RedteamJudgeTrackEngine {
		t.Fatalf("judge_track = %q, want engine", got)
	}
}

// TestScanRedteamReportCorruptCriticalColumnsFailClosed 是本层的核心反证。
//
// 逐个损坏三个关键列，断言行被拒、错误可定位到列。
//
// 三条 CRITICAL 的后果各不相同（已在注释与报告里写明）：
//   - findings归零 → 用户读到「0 个发现、0 次攻击成功」的干净报告（假全清）。
//   - evidence_handles 归零 → 报告还在，但每条结论都点不开原始证据（不可验证）。
//   - metadata 归零 → judge_track 从 promptfoo_native 静默降级为
//     legacy_default/platform：报告照样打得开，只是判定口径的来源标签被改写了。
func TestScanRedteamReportCorruptCriticalColumnsFailClosed(t *testing.T) {
	cases := []struct {
		name            string
		findings        string
		evidenceHandles string
		metadata        string
		wantField       string
	}{
		{"corrupt findings", `[{"id":`, goodEvidence, goodMeta, "findings"},
		{"corrupt evidence_handles", goodFindings, `["ev-a",`, goodMeta, "evidence_handles"},
		{"corrupt metadata (judge_track真源)", goodFindings, goodEvidence, `{"judge_track":`, "metadata"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := &fakeRows{rows: []fakeRow{{
				values: reportRowValues(t, "bad1", c.findings, c.evidenceHandles, c.metadata),
			}}}
			if !rows.Next() {
				t.Fatal("fake must yield one row")
			}

			got, err := scanRedteamReport(rows)
			if err == nil {
				t.Fatalf("corrupt %s must fail closed, got report %+v", c.wantField, got)
			}
			if got != nil {
				t.Fatalf("failed read must not return a report, got %+v", got)
			}
			var corrupt *CorruptRowError
			if !errors.As(err, &corrupt) {
				t.Fatalf("error must unwrap to *CorruptRowError, got %T", err)
			}
			if corrupt.Field != c.wantField {
				t.Fatalf("corrupt.Field = %q, want %q", corrupt.Field, c.wantField)
			}
			if corrupt.RowID != "bad1" {
				t.Fatalf("corrupt.RowID = %q, want bad1 (the handle)", corrupt.RowID)
			}
			if corrupt.Table != "maclaw_redteam_reports" {
				t.Fatalf("corrupt.Table = %q", corrupt.Table)
			}
		})
	}
}

// TestScanRedteamReportMetadataCorruptionWouldMisreportJudgeTrack 把
// 「metadata 损坏 → judge_track 被改写」的后果钉成一个具体断言。
//
// 这是 metadata 从 degradable 升级为 CRITICAL 的**直接证据**：
// 若允许降级，下面这个断言描述的后果就会真实发生——报告照常渲染，
// 只是把「引擎判定」说成了「旧报告/平台判定」，没有任何报错。
func TestScanRedteamReportMetadataCorruptionWouldMisreportJudgeTrack(t *testing.T) {
	// 正常路径：metadata 完好 → 口径正确。
	rows := &fakeRows{rows: []fakeRow{{values: reportRowValues(t, "jt-ok", goodFindings, goodEvidence, goodMeta)}}}
	rows.Next()
	report, err := scanRedteamReport(rows)
	if err != nil {
		t.Fatalf("scanRedteamReport error = %v", err)
	}
	if got := maclaw.RedteamJudgeTrackOf(report.Metadata); got != maclaw.RedteamJudgeTrackEngine {
		t.Fatalf("intact metadata: judge_track = %q, want engine", got)
	}

	// 损坏路径：现在会 fail-closed。
	// 反事实说明（不写断言，只记录）：若改成 degradable，Metadata 归零 →
	// ResolveRedteamJudgeTrack 走 len==0 分支 → legacy_default/platform，
	// 而上面第一个断言仍然会「通过」（因为它测的是完好数据），
	// 错误口径就会被当成正常报告返回给用户。
	rowsBad := &fakeRows{rows: []fakeRow{{values: reportRowValues(t, "jt-bad", goodFindings, goodEvidence, `{oops`)}}}
	rowsBad.Next()
	if _, err := scanRedteamReport(rowsBad); err == nil {
		t.Fatal("corrupt metadata must fail closed, otherwise judge_track is silently misreported")
	}
}

// TestScanRedteamEvidenceCorruptMetadataDegrades 覆盖证据的 metadata 是 degradable。
//
// 证据（不是报告）的 metadata 不承载 judge_track，损坏只损失展示信息，
// 行应保留 —— 与报告形成对照，证明分级不是一刀切。
func TestScanRedteamEvidenceCorruptMetadataDegrades(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{{values: evidenceRowValues(t, "ev1", `{broken`)}}}
	if !rows.Next() {
		t.Fatal("fake must yield one row")
	}
	ev, err := scanRedteamEvidence(rows)
	if err != nil {
		t.Fatalf("evidence metadata corruption must degrade, got err = %v", err)
	}
	if ev == nil {
		t.Fatal("evidence row must survive degradable corruption")
	}
	if ev.Handle != "ev1" {
		t.Fatalf("Handle = %q, want ev1", ev.Handle)
	}
}

// TestScanRedteamRowsSkipsBadKeepsGood 覆盖列表读的双向设计。
//
// 报告列表的坏行必须被跳过、好行保留、且降级上抛 —— 一条脏报告
// 不该让用户的整个报告列表打不开。
func TestScanRedteamRowsSkipsBadKeepsGood(t *testing.T) {
	good1, bad, good2 := "ok-1", "bad-1", "ok-2"
	rows := &fakeRows{rows: []fakeRow{
		{values: reportRowValues(t, good1, goodFindings, goodEvidence, goodMeta)},
		{values: reportRowValues(t, bad, `[{"id":`, goodEvidence, goodMeta)},
		{values: reportRowValues(t, good2, goodFindings, goodEvidence, goodMeta)},
	}}

	items, err := scanRedteamRows(rows, "maclaw_redteam_reports", scanRedteamReport)
	if !maclaw.IsPartialCorruption(err) {
		t.Fatalf("want partial corruption error, got %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2 (healthy reports survive)", len(items))
	}
	if items[0].Handle != good1 || items[1].Handle != good2 {
		t.Fatalf("surviving handles = %q,%q want %q,%q", items[0].Handle, items[1].Handle, good1, good2)
	}
	if !contains(err.Error(), bad) {
		t.Fatalf("error must name the skipped report, got %q", err.Error())
	}
}

// TestScanRedteamRowsHealthyYieldsNoError 覆盖「无坏行不产生降级错误」。
//
// 否则每次正常列表查询都会返回非 nil error，把调用方的错误分支永久点亮。
func TestScanRedteamRowsHealthyYieldsNoError(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{
		{values: reportRowValues(t, "a", goodFindings, goodEvidence, goodMeta)},
		{values: reportRowValues(t, "b", goodFindings, goodEvidence, goodMeta)},
	}}
	items, err := scanRedteamRows(rows, "maclaw_redteam_reports", scanRedteamReport)
	if err != nil {
		t.Fatalf("healthy rows must not produce an error, got %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
}

// TestScanRedteamRowsIterationErrorFailsClosed 覆盖硬错误不被降级。
//
// 连接中断不是「部分可用」，必须整体失败 —— 否则数据库故障会伪装成
// 「列表少了几行」，极难排查。
func TestScanRedteamRowsIterationErrorFailsClosed(t *testing.T) {
	rows := &fakeRows{
		rows:    []fakeRow{{values: reportRowValues(t, "a", goodFindings, goodEvidence, goodMeta)}},
		iterErr: errors.New("conn reset"),
	}
	items, err := scanRedteamRows(rows, "maclaw_redteam_reports", scanRedteamReport)
	if err == nil {
		t.Fatal("iteration error must fail closed")
	}
	if maclaw.IsPartialCorruption(err) {
		t.Fatal("iteration error must NOT be classified as partial corruption")
	}
	if items != nil {
		t.Fatalf("items = %+v, want nil", items)
	}
}

// TestRedteamWritePathChecksMarshalErrors 锁住「写路径显式判错」这一结构性质。
//
// ⚠️ 写这个护栏时踩过一个坑，值得记下来（已升为项目判据）：
// **一条会假红的护栏比没有护栏更糟，因为它训练人忽略告警。**
// 第一版直接扫源码原文，结果命中了 engine_run.go:50 的注释——
// 那行注释正是在描述修复前的 bug（`// （plugins, _ = json.Marshal(...)）`）。
// 于是一个「注释在描述修复前的 bug」被误判成「修复被回退」。
// 修法是 stripGoComments：先剥注释再扫。判据本身没变，变的只是让护栏
// 只看代码、不看散文。
//
// 为什么需要它：RedteamReportRecord / RedteamEvidenceRecord 的字段全是
// 可 JSON 编码的标量与 map[string]string，**无法构造 marshal 失败**，
// 所以运行时断言不到 fail-closed（对比 engine_runs 的 Plugins.Config 是
// map[string]interface{}，可以塞 channel）。
//
// 于是退一步锁结构：源码里不得再出现 `x, _ := json.Marshal` / `x, _ = json.Marshal`
// 这种吞错写法。覆盖面不足，但比「没有任何护栏」强，且守住的是本次修复的核心。
func TestRedteamWritePathChecksMarshalErrors(t *testing.T) {
	sources := map[string]string{
		"maclaw_redteam_artifact.go": readRepoSource(t, "maclaw_redteam_artifact.go"),
		"engine_run.go":              readRepoSource(t, "engine_run.go"),
		"template.go":                readRepoSource(t, "template.go"),
	}
	for name, src := range sources {
		for _, banned := range []string{", _ := json.Marshal(", ", _ = json.Marshal(", "_, _ ="} {
			if contains(src, banned) {
				t.Fatalf("%s still swallows a marshal error (%q): the write path must fail closed", name, banned)
			}
		}
	}
}

// TestResourcePublicationCorruptAssessmentTypesFailsClosed 覆盖资源投影。
//
// assessment_types 经 capability_catalog.go:291 变成能力卡片的 RiskTypes，
// 归零 = 向能力目录误报「不覆盖任何风险类型」。
func TestResourcePublicationCorruptAssessmentTypesFailsClosed(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{
		{values: resourcePublicationRowValues("res-1", `{oops`, goodTags, `{"k":"v"}`)},
	}}
	items, err := scanResourcePublications(rows)
	if !maclaw.IsPartialCorruption(err) {
		t.Fatalf("want partial corruption error, got %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0 (corrupt publication must be skipped)", len(items))
	}
	var corrupt *CorruptRowError
	if !errors.As(err, &corrupt) {
		t.Fatalf("error must unwrap to *CorruptRowError, got %T", err)
	}
	if corrupt.Field != "assessment_types" || corrupt.RowID != "res-1" {
		t.Fatalf("corrupt = %+v, want row res-1 column assessment_types", corrupt)
	}
}

// TestResourcePublicationCorruptDegradableKeepsRow 覆盖 tags/metadata 降级。
func TestResourcePublicationCorruptDegradableKeepsRow(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{
		{values: resourcePublicationRowValues("res-2", goodAssessType, `[broken`, `{"k":`)},
	}}
	items, err := scanResourcePublications(rows)
	if err != nil {
		t.Fatalf("degradable corruption must not fail the row, got %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if len(items[0].AssessmentTypes) != 2 {
		t.Fatalf("AssessmentTypes = %+v, want intact", items[0].AssessmentTypes)
	}
	if items[0].Metadata == nil {
		t.Fatal("Metadata = nil, want non-nil map (downstream writes to it)")
	}
}

// TestResourcePublicationSkipsBadKeepsGood 覆盖「一条坏行不毁整批」。
func TestResourcePublicationSkipsBadKeepsGood(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{
		{values: resourcePublicationRowValues("res-a", goodAssessType, goodTags, `{}`)},
		{values: resourcePublicationRowValues("res-bad", `{oops`, goodTags, `{}`)},
		{values: resourcePublicationRowValues("res-c", goodAssessType, goodTags, `{}`)},
	}}
	items, err := scanResourcePublications(rows)
	if !maclaw.IsPartialCorruption(err) {
		t.Fatalf("want partial corruption, got %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0].SourceResourceID != "res-a" || items[1].SourceResourceID != "res-c" {
		t.Fatalf("surviving ids = %q,%q", items[0].SourceResourceID, items[1].SourceResourceID)
	}
}

// TestSkillPublicationCorruptTriggersFailsClosed 覆盖 triggers 的 CRITICAL 判定。
//
// triggers 经 capability_catalog.go:317 变成 UseWhen —— **技能选用条件**。
// 归零等于「红队智能体在该技能该出手时不出手」，即选择逻辑被静默改写。
// 这比标签丢失严重得多，故为 CRITICAL（degradable 会放过它）。
func TestSkillPublicationCorruptTriggersFailsClosed(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{
		{values: skillPublicationRowValues("sk-bad", `[broken`, goodTags, goodAssessType, `{}`)},
	}}
	items, err := scanSkillPublications(rows)
	if !maclaw.IsPartialCorruption(err) {
		t.Fatalf("want partial corruption, got %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}
	var corrupt *CorruptRowError
	if !errors.As(err, &corrupt) {
		t.Fatalf("error must unwrap to *CorruptRowError, got %T", err)
	}
	if corrupt.Field != "triggers" {
		t.Fatalf("corrupt.Field = %q, want triggers", corrupt.Field)
	}
}

// TestSkillPublicationCorruptAssessmentTypesFailsClosed 覆盖 skill 的 assessment_types。
func TestSkillPublicationCorruptAssessmentTypesFailsClosed(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{
		{values: skillPublicationRowValues("sk-bad2", goodTriggers, goodTags, `{oops`, `{}`)},
	}}
	items, err := scanSkillPublications(rows)
	if !maclaw.IsPartialCorruption(err) {
		t.Fatalf("want partial corruption, got %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}
	var corrupt *CorruptRowError
	if !errors.As(err, &corrupt) {
		t.Fatalf("error must unwrap to *CorruptRowError, got %T", err)
	}
	if corrupt.Field != "assessment_types" {
		t.Fatalf("corrupt.Field = %q, want assessment_types", corrupt.Field)
	}
}

// TestSkillPublicationHealthyPreservesTriggers 覆盖正常行的 triggers 被正确解析。
//
// 这是 triggers 判为 CRITICAL 的对照基准：若解码整体坏掉，
// 上面两个「跳过」断言也会跟着通过。
func TestSkillPublicationHealthyPreservesTriggers(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{
		{values: skillPublicationRowValues("sk-ok", goodTriggers, goodTags, goodAssessType, `{"risk_types":"jailbreak"}`)},
	}}
	items, err := scanSkillPublications(rows)
	if err != nil {
		t.Fatalf("healthy publication must not error, got %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if len(items[0].Triggers) != 2 {
		t.Fatalf("Triggers = %+v, want 2 entries", items[0].Triggers)
	}
	if len(items[0].AssessmentTypes) != 2 {
		t.Fatalf("AssessmentTypes = %+v, want 2 entries", items[0].AssessmentTypes)
	}
	if items[0].Metadata["risk_types"] != "jailbreak" {
		t.Fatalf("Metadata = %+v", items[0].Metadata)
	}
}

// TestSkillPublicationCorruptDegradableKeepsRow 覆盖 tags/metadata 降级保行。
func TestSkillPublicationCorruptDegradableKeepsRow(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{
		{values: skillPublicationRowValues("sk-deg", goodTriggers, `[broken`, goodAssessType, `{"k":`)},
	}}
	items, err := scanSkillPublications(rows)
	if err != nil {
		t.Fatalf("degradable corruption must not fail the row, got %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	// 关键列必须不受影响 —— 降级不能传染。
	if len(items[0].Triggers) != 2 {
		t.Fatalf("Triggers = %+v, want intact", items[0].Triggers)
	}
	if len(items[0].AssessmentTypes) != 2 {
		t.Fatalf("AssessmentTypes = %+v, want intact", items[0].AssessmentTypes)
	}
}

// TestSkillPublicationSkipsBadKeepsGood 覆盖列表不被单条坏记录拖垮。
func TestSkillPublicationSkipsBadKeepsGood(t *testing.T) {
	rows := &fakeRows{rows: []fakeRow{
		{values: skillPublicationRowValues("sk-a", goodTriggers, goodTags, goodAssessType, `{}`)},
		{values: skillPublicationRowValues("sk-bad", `{oops`, goodTags, goodAssessType, `{}`)},
		{values: skillPublicationRowValues("sk-c", goodTriggers, goodTags, goodAssessType, `{}`)},
	}}
	items, err := scanSkillPublications(rows)
	if !maclaw.IsPartialCorruption(err) {
		t.Fatalf("want partial corruption, got %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0].SourceSkillName != "sk-a" || items[1].SourceSkillName != "sk-c" {
		t.Fatalf("surviving names = %q,%q", items[0].SourceSkillName, items[1].SourceSkillName)
	}
}

// TestPublicationIterationErrorFailsClosed 覆盖两个投影的硬错误都不降级。
func TestPublicationIterationErrorFailsClosed(t *testing.T) {
	resRows := &fakeRows{
		rows:    []fakeRow{{values: resourcePublicationRowValues("res-x", goodAssessType, goodTags, `{}`)}},
		iterErr: errors.New("conn reset"),
	}
	if items, err := scanResourcePublications(resRows); err == nil || maclaw.IsPartialCorruption(err) || items != nil {
		t.Fatalf("resource iteration error must fail closed: items=%+v err=%v", items, err)
	}

	skillRows := &fakeRows{
		rows:    []fakeRow{{values: skillPublicationRowValues("sk-x", goodTriggers, goodTags, goodAssessType, `{}`)}},
		iterErr: errors.New("conn reset"),
	}
	if items, err := scanSkillPublications(skillRows); err == nil || maclaw.IsPartialCorruption(err) || items != nil {
		t.Fatalf("skill iteration error must fail closed: items=%+v err=%v", items, err)
	}
}

// ───────────────────── 行值构造 ─────────────────────

// resourcePublicationRowValues 按 scanResourcePublications 的 Scan 顺序构造一行。
func resourcePublicationRowValues(sourceID, assessmentTypes, tags, metadata string) []any {
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	return []any{
		uuid.New(),           // source_expert_user_id
		"tenant-1",           // source_maclaw_tenant_id
		sourceID,             // source_resource_id
		"handle-" + sourceID, // source_resource_handle
		"v1",                 // source_version
		"Resource",           // name
		"plugin",             // kind
		"published",          // status
		true,                 // enabled
		"summary",            // summary
		[]byte(assessmentTypes),
		[]byte(tags),
		[]byte(metadata),
		now,
		now,
	}
}

// skillPublicationRowValues 按 scanSkillPublications 的 Scan 顺序构造一行。
func skillPublicationRowValues(sourceSkillName, triggers, tags, assessmentTypes, metadata string) []any {
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	return []any{
		uuid.New(), // source_expert_user_id
		"tenant-1", // source_maclaw_tenant_id
		sourceSkillName,
		"v1", // source_version
		"Skill",
		"desc",
		"published",
		true,
		[]byte(triggers),
		[]byte(tags),
		[]byte(assessmentTypes),
		[]byte(metadata),
		now,
		now,
	}
}

// 编译期断言：确保测试里的构造仍与生产模型对齐（字段被改名时会立刻报错）。
var _ = model.MaclawResourcePublication{}
var _ = model.MaclawSkillPublication{}

// readRepoSource 读取本包内的生产源文件，供结构性质断言使用。
//
// 为什么需要它：RedteamReportRecord / RedteamEvidenceRecord 的字段全是
// 可 JSON 编码的标量与 map[string]string，**无法构造 marshal 失败**
// （对比 engine_runs 的 Plugins.Config 是 map[string]interface{}，可以塞
// channel）。因此运行时断言不到「写路径 fail-closed」，
// 只能退一步锁结构：源码里不得再出现吞错的 marshal 写法。
//
// 覆盖面确实不足（它只能证明「写了显式判错」，不能证明「判错后行为正确」），
// 但比完全没有护栏强，且守的正是本次修复的核心不变式。
func readRepoSource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return stripGoComments(string(data))
}

// stripGoComments 去掉行注释后再返回源码。
//
// 为什么必须去注释：本仓库大量注释里**引用了被修复前的吞错写法**
// （例如 engine_run.go:50 写着「原先是 `plugins, _ = json.Marshal(...)`」）。
// 结构护栏直接扫原文会命中这些说明文字，把「注释在描述修复前的 bug」
// 误判成「修复被回退了」—— 一条会假红的护栏比没有护栏更糟，
// 因为它训练人忽略告警。
func stripGoComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
