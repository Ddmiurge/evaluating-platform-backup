package handler

// redteam_mcp_snapshot_test.go — U3 护栏：MCP 工具清单（tools/list）快照。
//
// 背景：U3 结构治理要把 internal/maclaw/redteam_tool_bridge.go（3042 行、115 个函数）
// 拆成 internal/maclaw/redteam/ 子包。工具清单是 MaClaw 与平台之间的**对外契约**，
// 拆包时最容易发生的无意识回归是「顺手把某个 description 改通顺」——
// 编译器发现不了，现有的 redteam_mcp_test.go 也发现不了（它只做 strings.Contains
// 抽查 7 个工具名 + 16 个字段名）。本测试把 15 个工具的
// name + description + inputSchema 全量锁定，任何漂移都会红。
//
// 生成 golden（只在 U3 Step 1 跑一次，之后不再生成）：
//
//	UPDATE_MCP_SNAPSHOT=1 go test ./internal/api/handler/ -run TestRedteamMCPToolSnapshot
//
// 校验（CI 与每次 U3 步骤都跑）：
//
//	go test ./internal/api/handler/ -run TestRedteamMCPToolSnapshot
//
// 注意：golden 路径用相对路径，因为 `go test` 的工作目录是测试文件所属的包目录。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

const (
	// mcpToolSnapshotPath 指向 golden 快照文件。
	//
	// 路径说明：`go test` 的工作目录是本测试文件所属的包目录
	// internal/api/handler，因此回到模块内的 internal/maclaw 需要两级
	// `../`（handler -> api -> internal）。
	//
	// U3 设计文档 §4.3 写的是 "../maclaw/redteam/testdata/"，实测那是错的：
	// 从 internal/api/handler 出发 `..` 只回到 internal/api，会把快照写进
	// 不存在的 internal/api/maclaw/。正确前缀是 "../../maclaw/"。
	//
	// U3 Step 2 起，快照随 redteam/ 子包一起迁到 internal/maclaw/redteam/testdata/。
	mcpToolSnapshotPath = "../../maclaw/redteam/testdata/mcp_tool_snapshot.json"

	// wantRedteamMCPToolCount 是 tools/list 的工具数量（实测，见 U3 Step 1）。
	wantRedteamMCPToolCount = 15
)

// wantRedteamMCPToolNames 是 tools/list 的完整工具名清单（升序）。
//
// 这份清单与 golden 快照互为独立护栏：快照能发现「内容变了」，
// 清单能发现「工具增删了」，两者失效能给出完全不同的失败信息。
var wantRedteamMCPToolNames = []string{
	"call_evaluation_target",
	"compile_redteam_report",
	"compose_redteam_payloads",
	"execute_redteam_evaluation_batch",
	"get_capability_detail",
	"get_promptfoo_evaluation_result",
	"judge_attack_result",
	"prepare_redteam_capability",
	"prepare_skill_input_data",
	"register_skill_payload_dataset",
	"run_promptfoo_redteam_evaluation",
	"save_redteam_evidence",
	"search_platform_redteam_capabilities",
	"search_redteam_capabilities",
	"search_redteam_plugin_catalog",
}

// mcpToolSnapshotEntry 是单个工具的契约条目。
type mcpToolSnapshotEntry struct {
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// mcpToolSnapshot 是 tools/list 的完整快照。
type mcpToolSnapshot struct {
	Tools map[string]mcpToolSnapshotEntry `json:"tools"`
}

// collectRedteamMCPToolSnapshot 从 redteamMCPToolDefinitions() 采集当前工具清单。
//
// 只依赖 map 与json.Marshal 的稳定 key排序（Go 的 encoding/json 对 map key
// 排序输出），因此同一份代码任意次运行都得到逐字节相同的结果，可直接做golden 比对。
func collectRedteamMCPToolSnapshot(t *testing.T) mcpToolSnapshot {
	t.Helper()

	raw := redteamMCPToolDefinitions()
	snapshot := mcpToolSnapshot{Tools: make(map[string]mcpToolSnapshotEntry, len(raw))}
	for _, item := range raw {
		name, ok := item["name"].(string)
		if !ok || name == "" {
			t.Fatalf("tool definition without a usable string name: %#v", item)
		}
		if _, duplicated := snapshot.Tools[name]; duplicated {
			t.Fatalf("duplicated tool name %q in tools/list", name)
		}
		description, ok := item["description"].(string)
		if !ok {
			t.Fatalf("tool %q has no string description", name)
		}
		schema, exists := item["inputSchema"]
		if !exists || schema == nil {
			t.Fatalf("tool %q has no inputSchema", name)
		}
		encoded, err := json.Marshal(schema)
		if err != nil {
			t.Fatalf("marshal inputSchema of tool %q: %v", name, err)
		}
		snapshot.Tools[name] = mcpToolSnapshotEntry{
			Description: description,
			InputSchema: encoded,
		}
	}
	return snapshot
}

// marshalRedteamMCPToolSnapshot 把快照序列化为golden 文件的规范字节。
func marshalRedteamMCPToolSnapshot(t *testing.T, snapshot mcpToolSnapshot) []byte {
	t.Helper()
	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return append(encoded, '\n')
}

// sortedSnapshotToolNames 返回快照中按字典序排列的工具名。
func sortedSnapshotToolNames(snapshot mcpToolSnapshot) []string {
	names := make([]string, 0, len(snapshot.Tools))
	for name := range snapshot.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// jsonBytesEqual 比较两份 JSON 的**语义**是否相等，忽略缩进等排版差异。
//
// 为什么需要它：golden 文件里的 input_schema 是被 MarshalIndent 缩进过的，
// 而实时采集到的inputSchema 是 json.Marshal 的紧凑形态，直接比字节会永远不等。
// 这里统一用「解析后按key 排序重新编码」的方式归一化。
func jsonBytesEqual(a, b []byte) bool {
	return canonicalJSON(a) == canonicalJSON(b)
}

// canonicalJSON 把任意 JSON 归一化为紧凑且 key 有序的字符串。
//
// 非法 JSON 输入原样返回（附带截断），以便失败信息里能看出问题。
func canonicalJSON(raw []byte) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		const maxLen = 200
		if len(raw) > maxLen {
			return string(raw[:maxLen]) + "...(截断)"
		}
		return string(raw)
	}
	// encoding/json 对 map key 排序输出，因此 Marshal 结果是稳定的。
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

// diffRedteamMCPTools 逐工具比对两个快照，返回人类可读的差异描述。
//
// 按工具名逐项比对（而不是整块 diff 字节）是为了让失败信息直接指出
// 「哪个工具的哪一块变了」，而不是让人在两份 500 行 JSON 里找红绿。
func diffRedteamMCPTools(want, got mcpToolSnapshot) []string {
	var diffs []string

	seen := make(map[string]struct{}, len(want.Tools)+len(got.Tools))
	for name := range want.Tools {
		seen[name] = struct{}{}
	}
	for name := range got.Tools {
		seen[name] = struct{}{}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		wantEntry, inWant := want.Tools[name]
		gotEntry, inGot := got.Tools[name]
		switch {
		case inWant && !inGot:
			diffs = append(diffs, fmt.Sprintf(
				"工具已从 tools/list 中消失（golden 有、当前没有）: %s", name))
			continue
		case !inWant && inGot:
			diffs = append(diffs, fmt.Sprintf(
				"工具已新出现在 tools/list 中（golden 没有、当前有）: %s", name))
			continue
		}
		if wantEntry.Description != gotEntry.Description {
			diffs = append(diffs, fmt.Sprintf(
				"工具 %s 的 description 变了:\n  golden: %s\n  当前值: %s",
				name, wantEntry.Description, gotEntry.Description))
		}
		if !jsonBytesEqual(wantEntry.InputSchema, gotEntry.InputSchema) {
			diffs = append(diffs, fmt.Sprintf(
				"工具 %s 的 inputSchema 变了:\n  golden: %s\n  当前值: %s",
				name, canonicalJSON(wantEntry.InputSchema), canonicalJSON(gotEntry.InputSchema)))
		}
	}
	return diffs
}

// readRedteamMCPToolSnapshotGolden 读取并解析 golden 快照文件。
func readRedteamMCPToolSnapshotGolden(t *testing.T) mcpToolSnapshot {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(mcpToolSnapshotPath))
	if err != nil {
		t.Fatalf("读取 golden 快照失败（首次生成请跑 UPDATE_MCP_SNAPSHOT=1）: %v", err)
	}
	var snapshot mcpToolSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("解析 golden 快照失败: %v", err)
	}
	if len(snapshot.Tools) == 0 {
		t.Fatalf("golden 快照里没有任何工具: %s", mcpToolSnapshotPath)
	}
	return snapshot
}

// TestRedteamMCPToolSnapshotMatchesGolden 校验实时工具清单与 golden 快照逐字节一致。
//
// 若本测试变红且改动是有意的，用以下命令重新生成并review diff：
//
//	UPDATE_MCP_SNAPSHOT=1 go test ./internal/api/handler/ -run TestRedteamMCPToolSnapshot
func TestRedteamMCPToolSnapshotMatchesGolden(t *testing.T) {
	got := collectRedteamMCPToolSnapshot(t)
	encoded := marshalRedteamMCPToolSnapshot(t, got)

	if os.Getenv("UPDATE_MCP_SNAPSHOT") == "1" {
		path := filepath.FromSlash(mcpToolSnapshotPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("创建 testdata 目录失败: %v", err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatalf("写入 golden 快照失败: %v", err)
		}
		t.Logf("已写入 %d 个工具到 %s", len(got.Tools), path)
		return
	}

	want := readRedteamMCPToolSnapshotGolden(t)
	if diffs := diffRedteamMCPTools(want, got); len(diffs) > 0 {
		t.Fatalf("MCP 工具清单快照漂移，共 %d 处差异:\n%s\n\n"+
			"若该变更是预期的，请重新生成 golden 并逐条review 差异:\n"+
			"  UPDATE_MCP_SNAPSHOT=1 go test ./internal/api/handler/ -run TestRedteamMCPToolSnapshot",
			len(diffs), joinLines(diffs))
	}

	// 逐字节相等是最终判据：确保 golden 文件本身也是规范格式（无多余空白），
	// 这样 U3 Step 6 之后可以直接 diff 两个 golden 文件来留证。
	wantEncoded := marshalRedteamMCPToolSnapshot(t, want)
	if string(wantEncoded) != string(encoded) {
		t.Fatalf("golden 快照文件不是规范格式（工具内容一致但字节不同），请重新生成:\n%s", mcpToolSnapshotPath)
	}

	t.Logf("MCP 工具清单与 golden 一致：%d 个工具", len(got.Tools))
}

// TestRedteamMCPToolManifestPinned 锁定工具的数量与名称清单。
//
// 与快照测试分工：快照负责「内容不变」，本测试负责「工具集合不变」，
// 并给出比 JSON diff 更直观的失败信息。
func TestRedteamMCPToolManifestPinned(t *testing.T) {
	got := collectRedteamMCPToolSnapshot(t)

	if len(got.Tools) != wantRedteamMCPToolCount {
		t.Errorf("tools/list 工具数量 = %d，期望 %d", len(got.Tools), wantRedteamMCPToolCount)
	}

	gotNames := sortedSnapshotToolNames(got)
	if len(gotNames) != len(wantRedteamMCPToolNames) {
		t.Fatalf("tools/list 工具数量 = %d（golden 清单 %d）: %v",
			len(gotNames), len(wantRedteamMCPToolNames), gotNames)
	}
	for i := range wantRedteamMCPToolNames {
		if gotNames[i] != wantRedteamMCPToolNames[i] {
			t.Errorf("tools/list 第 %d 个工具 = %q，期望 %q（完整清单: %v）",
				i+1, gotNames[i], wantRedteamMCPToolNames[i], gotNames)
		}
	}
}

// joinLines 给差异列表加缩进，让多行 description / schema 在测试输出里可读。
func joinLines(diffs []string) string {
	out := ""
	for _, line := range diffs {
		out += "  - " + line + "\n"
	}
	return out
}
