package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fixtureRoot is a fixed path used for savings tests so that absolute path
// lengths in Bash output are deterministic across runs. Correctness tests
// use t.TempDir() instead (isolated, auto-cleaned).
const fixtureRoot = "/tmp/home/user/projects/example-service"

// fixtureFiles defines the fixture tree. Ordered slice so iteration is
// deterministic (map iteration order varies across Go versions).
var fixtureFiles = []struct {
	path string
	size int
}{
	{"README.md", 200},
	{"main.go", 500},
	{"go.mod", 50},
	{".gitignore", 30},
	{"Makefile", 120},
	{"cmd/server/main.go", 900},
	{"cmd/server/main_test.go", 450},
	{"cmd/client/main.go", 600},
	{"internal/handler/handler.go", 1200},
	{"internal/handler/handler_test.go", 800},
	{"internal/handler/middleware.go", 650},
	{"internal/config/config.go", 400},
	{"internal/config/config_test.go", 350},
	{"internal/storage/postgres.go", 1100},
	{"internal/storage/postgres_test.go", 700},
	{"internal/storage/memory.go", 500},
	{"pkg/api/types.go", 300},
	{"pkg/api/client.go", 550},
	{"docs/architecture.md", 2000},
	{"docs/api-reference.md", 1500},
	{"docs/getting-started.md", 800},
	{".github/workflows/ci.yml", 400},
	{".github/workflows/release.yml", 350},
}

var fixtureDirs = []string{
	"cmd/server",
	"cmd/client",
	"internal/handler",
	"internal/config",
	"internal/storage",
	"pkg/api",
	"docs",
	".github/workflows",
	"empty",
}

func populateFixture(t *testing.T, root string) {
	t.Helper()
	for _, d := range fixtureDirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range fixtureFiles {
		content := make([]byte, f.size)
		for i := range content {
			content[i] = 'x'
		}
		if f.size > 0 {
			content[f.size-1] = '\n'
		}
		if err := os.WriteFile(filepath.Join(root, f.path), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func setupFixtureTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	populateFixture(t, root)
	return root
}

func setupStableFixture(t *testing.T) string {
	t.Helper()
	os.RemoveAll(fixtureRoot)
	populateFixture(t, fixtureRoot)
	t.Cleanup(func() { os.RemoveAll(fixtureRoot) })
	return fixtureRoot
}

func bashOutput(t *testing.T, command string) string {
	t.Helper()
	out, err := exec.Command("bash", "-c", command).CombinedOutput()
	if err != nil {
		// Some commands (ls on missing file) intentionally fail; return output anyway
		return string(out)
	}
	return string(out)
}

func mcpResultText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "")
}

func mcpJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// assertSavings checks that MCP output is at most maxRatio of Bash output.
// A maxRatio of 0.65 means MCP must be <= 65% of Bash size (i.e., >= 35% savings).
func assertSavings(t *testing.T, label string, bashSize, mcpSize int, maxRatio float64) {
	t.Helper()
	if bashSize == 0 {
		t.Errorf("%s: bash output is empty, cannot compute ratio", label)
		return
	}
	ratio := float64(mcpSize) / float64(bashSize)
	savings := (1 - ratio) * 100
	t.Logf("%s: bash=%d chars, mcp=%d chars, savings=%.0f%%, ratio=%.2f (max %.2f)",
		label, bashSize, mcpSize, savings, ratio, maxRatio)
	if ratio > maxRatio {
		t.Errorf("%s: ratio %.2f exceeds max %.2f (savings %.0f%% below threshold)",
			label, ratio, maxRatio, savings)
	}
}

// ---------------------------------------------------------------------------
// Correctness tests
// ---------------------------------------------------------------------------

func TestListDirectory_NamesMatch(t *testing.T) {
	root := setupFixtureTree(t)

	result, _, err := handleListDirectory(context.Background(), nil, ListDirectoryInput{Path: root})
	if err != nil {
		t.Fatal(err)
	}

	mcpNames := strings.Split(strings.TrimSpace(mcpResultText(result)), "\n")

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var expected []string
	for _, e := range entries {
		expected = append(expected, e.Name())
	}

	if len(mcpNames) != len(expected) {
		t.Fatalf("count mismatch: got %d, want %d", len(mcpNames), len(expected))
	}
	for i := range expected {
		if mcpNames[i] != expected[i] {
			t.Errorf("entry %d: got %q, want %q", i, mcpNames[i], expected[i])
		}
	}
}

func TestListDirectory_DetailIncludesType(t *testing.T) {
	root := setupFixtureTree(t)

	_, out, err := handleListDirectory(context.Background(), nil, ListDirectoryInput{Path: root, Detail: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(out.Entries) == 0 {
		t.Fatal("no entries returned")
	}

	var hasFile, hasDir bool
	for _, e := range out.Entries {
		switch e.Type {
		case "file":
			hasFile = true
		case "dir":
			hasDir = true
		}
	}
	if !hasFile {
		t.Error("no file-type entries found")
	}
	if !hasDir {
		t.Error("no dir-type entries found")
	}
}

func TestFindFiles_GlobPattern(t *testing.T) {
	root := setupFixtureTree(t)

	_, out, err := handleFindFiles(context.Background(), nil, FindFilesInput{Path: root, Pattern: "*.go"})
	if err != nil {
		t.Fatal(err)
	}

	expected := map[string]bool{}
	for _, f := range fixtureFiles {
		if strings.HasSuffix(f.path, ".go") {
			expected[f.path] = true
		}
	}

	if len(out.Matches) != len(expected) {
		t.Fatalf("got %d matches, want %d: %v", len(out.Matches), len(expected), out.Matches)
	}
	for _, m := range out.Matches {
		if !expected[m] {
			t.Errorf("unexpected match: %s", m)
		}
	}
}

func TestFindFiles_MaxDepth(t *testing.T) {
	root := setupFixtureTree(t)
	depth := 1

	_, out, err := handleFindFiles(context.Background(), nil, FindFilesInput{Path: root, Pattern: "*.go", MaxDepth: &depth})
	if err != nil {
		t.Fatal(err)
	}

	if len(out.Matches) != 1 || out.Matches[0] != "main.go" {
		t.Errorf("expected [main.go] at depth 1, got %v", out.Matches)
	}

	depth = 3
	_, out2, err := handleFindFiles(context.Background(), nil, FindFilesInput{Path: root, Pattern: "*.go", MaxDepth: &depth})
	if err != nil {
		t.Fatal(err)
	}

	expected := map[string]bool{}
	for _, f := range fixtureFiles {
		if !strings.HasSuffix(f.path, ".go") {
			continue
		}
		d := strings.Count(f.path, string(filepath.Separator)) + 1
		if d <= 3 {
			expected[f.path] = true
		}
	}
	if len(out2.Matches) != len(expected) {
		t.Errorf("depth 3: got %d matches, want %d: %v", len(out2.Matches), len(expected), out2.Matches)
	}
}

func TestFindFiles_TypeFilter(t *testing.T) {
	root := setupFixtureTree(t)

	_, out, err := handleFindFiles(context.Background(), nil, FindFilesInput{Path: root, Type: "dir"})
	if err != nil {
		t.Fatal(err)
	}

	expected := map[string]bool{}
	for _, d := range fixtureDirs {
		expected[d] = true
		// Also add parent dirs that are implicit
		parts := strings.Split(d, string(filepath.Separator))
		for i := 1; i < len(parts); i++ {
			expected[strings.Join(parts[:i], string(filepath.Separator))] = true
		}
	}
	if len(out.Matches) != len(expected) {
		t.Fatalf("got %d dirs, want %d\ngot:  %v\nwant: %v", len(out.Matches), len(expected), out.Matches, expected)
	}
}

func TestFileInfo_RegularFile(t *testing.T) {
	root := setupFixtureTree(t)
	path := filepath.Join(root, "cmd/server/main.go")

	_, out, err := handleFileInfo(context.Background(), nil, FileInfoInput{Path: path})
	if err != nil {
		t.Fatal(err)
	}

	if out.Name != "main.go" {
		t.Errorf("name: got %q, want %q", out.Name, "main.go")
	}
	if out.Type != "file" {
		t.Errorf("type: got %q, want %q", out.Type, "file")
	}
	if out.Size != 900 {
		t.Errorf("size: got %d, want %d", out.Size, 900)
	}
	if out.Lines == nil || *out.Lines != 1 {
		t.Errorf("lines: got %v, want 1", out.Lines)
	}
}

func TestFileInfo_Directory(t *testing.T) {
	root := setupFixtureTree(t)

	_, out, err := handleFileInfo(context.Background(), nil, FileInfoInput{Path: filepath.Join(root, "docs")})
	if err != nil {
		t.Fatal(err)
	}

	if out.Type != "dir" {
		t.Errorf("type: got %q, want %q", out.Type, "dir")
	}
	if out.Lines != nil {
		t.Errorf("lines should be nil for directory, got %v", out.Lines)
	}
}

func TestFileExists_Exists(t *testing.T) {
	root := setupFixtureTree(t)

	_, out, err := handleFileExists(context.Background(), nil, FileExistsInput{Path: filepath.Join(root, "main.go")})
	if err != nil {
		t.Fatal(err)
	}

	if !out.Exists {
		t.Error("expected exists=true")
	}
	if out.Type == nil || *out.Type != "file" {
		t.Errorf("expected type=file, got %v", out.Type)
	}
}

func TestFileExists_NotExists(t *testing.T) {
	root := setupFixtureTree(t)

	_, out, err := handleFileExists(context.Background(), nil, FileExistsInput{Path: filepath.Join(root, "nonexistent.txt")})
	if err != nil {
		t.Fatal(err)
	}

	if out.Exists {
		t.Error("expected exists=false")
	}
	if out.Type != nil {
		t.Errorf("expected type=nil, got %v", out.Type)
	}
}

func TestFileExists_Directory(t *testing.T) {
	root := setupFixtureTree(t)

	_, out, err := handleFileExists(context.Background(), nil, FileExistsInput{Path: filepath.Join(root, "docs")})
	if err != nil {
		t.Fatal(err)
	}

	if !out.Exists {
		t.Error("expected exists=true")
	}
	if out.Type == nil || *out.Type != "dir" {
		t.Errorf("expected type=dir, got %v", out.Type)
	}
}

// ---------------------------------------------------------------------------
// Savings regression tests
// ---------------------------------------------------------------------------

func TestSavings_ListDirectoryDetail(t *testing.T) {
	root := setupStableFixture(t)

	bashOut := bashOutput(t, fmt.Sprintf("ls -la %s", root))

	_, out, err := handleListDirectory(context.Background(), nil, ListDirectoryInput{Path: root, Detail: true})
	if err != nil {
		t.Fatal(err)
	}
	mcpOut := mcpJSON(out)

	// Threshold: MCP must be <= 70% of Bash (>= 30% savings)
	assertSavings(t, "list_directory(detail=true) vs ls -la", len(bashOut), len(mcpOut), 0.70)
}

func TestSavings_ListDirectorySimple(t *testing.T) {
	root := setupStableFixture(t)

	// ls -A shows all entries except . and .., matching our handler's behavior
	bashOut := bashOutput(t, fmt.Sprintf("ls -A %s", root))

	result, _, err := handleListDirectory(context.Background(), nil, ListDirectoryInput{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	mcpOut := mcpResultText(result)

	// Plain text mode: MCP should be roughly equal to ls -A (ratio <= 1.05 for minor whitespace diffs)
	assertSavings(t, "list_directory(plain) vs ls -A", len(bashOut), len(mcpOut), 1.05)
}

func TestSavings_FindFiles(t *testing.T) {
	root := setupStableFixture(t)

	bashOut := bashOutput(t, fmt.Sprintf("find %s -name '*.go'", root))

	_, out, err := handleFindFiles(context.Background(), nil, FindFilesInput{Path: root, Pattern: "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	mcpOut := mcpJSON(out)

	// find outputs absolute paths; MCP outputs relative. Threshold: <= 75% (>= 25% savings).
	// Real-world savings are higher (~54%) because paths are longer, but the fixture tree
	// has short temp paths so we use a conservative threshold.
	assertSavings(t, "find_files vs find -name", len(bashOut), len(mcpOut), 0.75)
}

func TestSavings_FileExistsHit(t *testing.T) {
	root := setupStableFixture(t)
	path := filepath.Join(root, "README.md")

	// Real Bash pattern: ls <path> 2>/dev/null returns the path on success
	bashOut := bashOutput(t, fmt.Sprintf("ls %s 2>/dev/null", path))

	_, out, err := handleFileExists(context.Background(), nil, FileExistsInput{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	mcpOut := mcpJSON(out)

	// MCP output: {"exists":true,"type":"file"} (29 chars)
	// Bash output: the full absolute path + newline
	// With temp dir paths this is close, so use a lenient threshold.
	assertSavings(t, "file_exists(hit) vs ls 2>/dev/null", len(bashOut), len(mcpOut), 1.10)
}

func TestSavings_FileExistsMiss(t *testing.T) {
	root := setupStableFixture(t)
	path := filepath.Join(root, "nonexistent.txt")

	// Real Bash pattern: ls <path> 2>&1 returns error message
	bashOut := bashOutput(t, fmt.Sprintf("ls %s 2>&1", path))

	_, out, err := handleFileExists(context.Background(), nil, FileExistsInput{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	mcpOut := mcpJSON(out)

	// Bash: "ls: /tmp/.../nonexistent.txt: No such file or directory\n" (~60+ chars)
	// MCP: {"exists":false} (16 chars)
	assertSavings(t, "file_exists(miss) vs ls 2>&1", len(bashOut), len(mcpOut), 0.50)
}

func TestSavings_FileExistsDetailedCheck(t *testing.T) {
	root := setupStableFixture(t)
	path := filepath.Join(root, "README.md")

	// Real Bash pattern: ls -la <path> 2>/dev/null (common in transcripts)
	bashOut := bashOutput(t, fmt.Sprintf("ls -la %s 2>/dev/null", path))

	_, out, err := handleFileExists(context.Background(), nil, FileExistsInput{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	mcpOut := mcpJSON(out)

	// ls -la returns permissions, owner, size, date, path (~80+ chars)
	// MCP: {"exists":true,"type":"file"} (29 chars)
	assertSavings(t, "file_exists vs ls -la 2>/dev/null", len(bashOut), len(mcpOut), 0.60)
}

func TestSavings_FileInfo(t *testing.T) {
	root := setupStableFixture(t)
	path := filepath.Join(root, "cmd/server/main.go")

	// Bash equivalent: stat + wc -l combined
	statOut := bashOutput(t, fmt.Sprintf("stat %s && wc -l %s", path, path))

	_, out, err := handleFileInfo(context.Background(), nil, FileInfoInput{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	mcpOut := mcpJSON(out)

	// GNU stat is very verbose (~500 chars); macOS stat is shorter (~250 chars).
	// Threshold: <= 50% (>= 50% savings), conservative enough for both.
	assertSavings(t, "file_info vs stat+wc", len(statOut), len(mcpOut), 0.50)
}

func TestSavings_FindFilesDirectoryListing(t *testing.T) {
	root := setupStableFixture(t)

	// Real Bash pattern: ls <dir> 2>/dev/null used as existence check for a directory
	// This returns ALL contents of the directory
	bashOut := bashOutput(t, fmt.Sprintf("ls %s 2>/dev/null", root))

	_, out, err := handleFileExists(context.Background(), nil, FileExistsInput{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	mcpOut := mcpJSON(out)

	// Bash: full directory listing (many lines)
	// MCP: {"exists":true,"type":"dir"} (28 chars)
	// With short temp-dir paths the fixture listing is small, so use a lenient threshold.
	assertSavings(t, "file_exists(dir) vs ls dir 2>/dev/null", len(bashOut), len(mcpOut), 0.80)
}
