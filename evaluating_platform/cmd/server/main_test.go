package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestApplyCORSHeadersAllowsPatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	applyCORSHeaders(c)

	methods := w.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(methods, "PATCH") {
		t.Fatalf("Access-Control-Allow-Methods = %q, want PATCH", methods)
	}
}

// migrationsDirFromTest 返回测试进程视角下的 migrations 目录。
// go test 的 CWD 是包目录 cmd/server/，而生产 CWD 是仓库根（容器 /app），
// 因此这里向上两级定位。
func migrationsDirFromTest(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", migrationsDir)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("locate migrations dir from cmd/server (%s): %v", dir, err)
	}
	return dir
}

// TestMigrationRegistryCoversDirectory 是 027 漏登记故障的防回归断言：
// migrations/ 目录中的每个 .sql 文件都必须出现在生产清单 migrationFiles 中。
//
// 反证（已执行）：从 migrationFiles 中临时删掉任意一项（如 027），本测试
// 必须失败；还原后恢复绿色。
func TestMigrationRegistryCoversDirectory(t *testing.T) {
	dir := migrationsDirFromTest(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	registered := make(map[string]bool, len(migrationFiles))
	for _, path := range migrationFiles {
		registered[path] = true
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		want := filepath.ToSlash(filepath.Join(migrationsDir, entry.Name()))
		if !registered[want] {
			t.Errorf("migration %q exists in migrations/ but is not registered in "+
				"migrationFiles (cmd/server/main.go): it will never execute — "+
				"this is exactly the 027 regression class "+
				"(audit-2026-10-12-engine-confirm-failure.md)", entry.Name())
		}
	}
}

// TestMigrationRegistryHasNoStaleEntries 反向断言：清单不能引用磁盘上不存在
// 的文件（迁移被删除/改名却忘记同步清单，只在启动时才炸属于过晚的失败）。
func TestMigrationRegistryHasNoStaleEntries(t *testing.T) {
	dir := migrationsDirFromTest(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	onDisk := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		onDisk[filepath.ToSlash(filepath.Join(migrationsDir, entry.Name()))] = true
	}

	for _, path := range migrationFiles {
		if !onDisk[filepath.ToSlash(path)] {
			t.Errorf("migrationFiles registers %q but no such file exists in migrations/", path)
		}
	}
}

// TestMigrationRegistryIncludesEngineRunJobLink 针对本次具体故障的定点断言：
// 027（engine_runs.job_id）必须在清单中，且执行顺序在 026 之后
// （027 依赖 026 建立的 engine_runs 表）。
func TestMigrationRegistryIncludesEngineRunJobLink(t *testing.T) {
	const (
		migration026 = "migrations/026_promptfoo_engine.sql"
		migration027 = "migrations/027_engine_run_job_link.sql"
	)
	indexOf := func(want string) int {
		for i, path := range migrationFiles {
			if path == want {
				return i
			}
		}
		return -1
	}

	i26, i27 := indexOf(migration026), indexOf(migration027)
	if i27 < 0 {
		t.Fatalf("%s missing from migrationFiles — regression of "+
			"audit-2026-10-12 (engine_runs.job_id never applied, "+
			"confirm path fails with SQLSTATE 42703)", migration027)
	}
	if i26 < 0 {
		t.Fatalf("%s missing from migrationFiles", migration026)
	}
	if i27 < i26 {
		t.Fatalf("%s must run after %s (got indexes %d < %d)",
			migration027, migration026, i27, i26)
	}
}

// TestMigrationFilesAreOrdered 锁住「清单按文件名严格排序」这一不变量：
// 迁移之间存在依赖（008→002、012→011、014/015→013），乱序要等真实 PG
// 执行时才炸，本地无 PG 环境，故在清单层锁死顺序。
func TestMigrationFilesAreOrdered(t *testing.T) {
	for i := 1; i < len(migrationFiles); i++ {
		if migrationFiles[i-1] >= migrationFiles[i] {
			t.Fatalf("migrationFiles must be strictly sorted by filename: %q >= %q at index %d",
				migrationFiles[i-1], migrationFiles[i], i)
		}
	}
}

// TestAssertMigrationRegistryDetectsDrift 对断言函数本身做反证：用临时目录
// 构造「漏登记 / 清单陈旧 / 1:1 一致 / 非 .sql 忽略 / 目录不可读」五种场景。
// 这样即使未来有人改坏了 assertMigrationRegistry，也有测试兜底。
func TestAssertMigrationRegistryDetectsDrift(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"001_a.sql", "002_b.sql"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("-- test\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	t.Run("unregistered file is rejected", func(t *testing.T) {
		err := assertMigrationRegistry(dir, []string{filepath.Join(dir, "001_a.sql")})
		if err == nil {
			t.Fatal("expected error for unregistered 002_b.sql, got nil")
		}
		if !strings.Contains(err.Error(), "002_b.sql") {
			t.Errorf("error should name the unregistered file, got: %v", err)
		}
	})

	t.Run("stale entry is rejected", func(t *testing.T) {
		err := assertMigrationRegistry(dir, []string{
			filepath.Join(dir, "001_a.sql"),
			filepath.Join(dir, "002_b.sql"),
			filepath.Join(dir, "003_gone.sql"),
		})
		if err == nil {
			t.Fatal("expected error for stale 003_gone.sql, got nil")
		}
		if !strings.Contains(err.Error(), "003_gone.sql") {
			t.Errorf("error should name the stale entry, got: %v", err)
		}
	})

	t.Run("exact match passes", func(t *testing.T) {
		err := assertMigrationRegistry(dir, []string{
			filepath.Join(dir, "001_a.sql"),
			filepath.Join(dir, "002_b.sql"),
		})
		if err != nil {
			t.Fatalf("expected nil for 1:1 registry, got: %v", err)
		}
	})

	t.Run("non-sql files are ignored", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("notes\n"), 0o600); err != nil {
			t.Fatalf("write README.md: %v", err)
		}
		err := assertMigrationRegistry(dir, []string{
			filepath.Join(dir, "001_a.sql"),
			filepath.Join(dir, "002_b.sql"),
		})
		if err != nil {
			t.Fatalf("non-.sql files must not trip the registry, got: %v", err)
		}
	})

	t.Run("unreadable dir is an error", func(t *testing.T) {
		if err := assertMigrationRegistry(filepath.Join(dir, "does-not-exist"), nil); err == nil {
			t.Fatal("expected error for unreadable migrations dir, got nil")
		}
	})
}

// TestAssertMigrationRegistryAcceptsProductionRegistry 端到端闭环：用生产
// 断言函数校验「真实目录 + 真实清单」必须通过。测试 CWD 在 cmd/server/，
// 因此把清单键改写成测试视角的相对路径（../../migrations/...）。
func TestAssertMigrationRegistryAcceptsProductionRegistry(t *testing.T) {
	dir := migrationsDirFromTest(t)
	files := make([]string, 0, len(migrationFiles))
	for _, path := range migrationFiles {
		files = append(files, filepath.Join("..", "..", path))
	}
	if err := assertMigrationRegistry(dir, files); err != nil {
		t.Fatalf("production registry must be 1:1 with migrations/: %v", err)
	}
}
