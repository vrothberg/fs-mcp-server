package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fixtureRoot is a fixed path used for savings tests so that absolute path
// lengths in Bash output are deterministic across runs. The path mimics a
// real project location (~50 chars) because find/ls output size scales with
// path length; a short /tmp/test root would understate the savings. Correctness
// tests use t.TempDir() instead (isolated, auto-cleaned).
const fixtureRoot = "/tmp/home/user/projects/example-service"

// fixtureFiles defines the fixture tree. Modeled after a real small Go service
// with cmd/, internal/, pkg/, docs/, and CI config to produce realistic
// directory depth, file count, and name lengths. Ordered slice so iteration
// is deterministic (map iteration order varies across Go versions).
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

// bashOutput runs a command via bash and returns combined stdout+stderr.
// Used by savings tests to capture the real Bash output that an agent would
// receive, including error text on missing files.
func bashOutput(t *testing.T, command string) string {
	t.Helper()
	out, err := exec.Command("bash", "-c", command).CombinedOutput()
	if err != nil {
		// Some commands (ls on missing file) intentionally fail; return output anyway
		return string(out)
	}
	return string(out)
}

// mcpResultText extracts plain text from a CallToolResult. Used for
// list_directory in names-only mode, which returns TextContent instead of
// structured JSON to avoid wrapping simple names in JSON overhead.
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

// mcpJSON marshals the handler's structured output to JSON, matching what
// the SDK sends over the wire. This is what the agent actually receives,
// so its length is the fair comparison point against Bash output.
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

	result, _, err := handleListDirectory(context.Background(), nil, ListDirectoryInput{Path: root, Fields: []string{"name", "type"}})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || len(result.Content) == 0 {
		t.Fatal("no result returned")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) == 0 {
		t.Fatal("no lines returned")
	}

	var hasFile, hasDir bool
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			t.Fatalf("bad line format: %q", line)
		}
		switch parts[1] {
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

func TestFindFiles_MaxDepthIncludesDirs(t *testing.T) {
	root := setupFixtureTree(t)
	depth := 1

	_, out, err := handleFindFiles(context.Background(), nil, FindFilesInput{Path: root, Type: "dir", MaxDepth: &depth})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{
		"cmd": true, "internal": true, "pkg": true,
		"docs": true, ".github": true, "empty": true,
	}
	if len(out.Matches) != len(want) {
		t.Fatalf("got %d dirs, want %d: %v", len(out.Matches), len(want), out.Matches)
	}
	for _, m := range out.Matches {
		if !want[m] {
			t.Errorf("unexpected dir at depth 1: %s", m)
		}
	}
}

func TestFindFiles_PathGlob(t *testing.T) {
	root := setupFixtureTree(t)

	_, out, err := handleFindFiles(context.Background(), nil, FindFilesInput{Path: root, Pattern: "cmd/*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) != 0 {
		t.Errorf("cmd/*.go should not match nested files, got %v", out.Matches)
	}

	_, out, err = handleFindFiles(context.Background(), nil, FindFilesInput{Path: root, Pattern: "cmd/server/*.go"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"cmd/server/main.go": true, "cmd/server/main_test.go": true}
	if len(out.Matches) != len(want) {
		t.Fatalf("got %v, want %v", out.Matches, want)
	}
	for _, m := range out.Matches {
		if !want[m] {
			t.Errorf("unexpected match: %s", m)
		}
	}
}

func TestFindFiles_InvalidPattern(t *testing.T) {
	root := setupFixtureTree(t)
	_, _, err := handleFindFiles(context.Background(), nil, FindFilesInput{Path: root, Pattern: "["})
	if err == nil {
		t.Fatal("expected error for invalid glob")
	}
}

func TestFindFiles_MaxResultsTruncated(t *testing.T) {
	root := setupFixtureTree(t)
	limit := 2

	_, out, err := handleFindFiles(context.Background(), nil, FindFilesInput{Path: root, Pattern: "*.go", MaxResults: &limit})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) != 2 {
		t.Errorf("got %d matches, want 2: %v", len(out.Matches), out.Matches)
	}
	if !out.Truncated {
		t.Error("expected truncated=true")
	}
}

func TestFindFiles_SkipsNodeModules(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"src/index.js", "node_modules/pkg/index.js"} {
		dir := filepath.Join(root, filepath.Dir(p))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, out, err := handleFindFiles(context.Background(), nil, FindFilesInput{Path: root, Pattern: "*.js"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) != 1 || out.Matches[0] != "src/index.js" {
		t.Errorf("expected [src/index.js], got %v", out.Matches)
	}

	_, nested, err := handleFindFiles(context.Background(), nil, FindFilesInput{
		Path:    filepath.Join(root, "node_modules"),
		Pattern: "*.js",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(nested.Matches) != 1 || nested.Matches[0] != "pkg/index.js" {
		t.Errorf("searching inside node_modules should return matches, got %v", nested.Matches)
	}
}

func TestFindFiles_MissingRoot(t *testing.T) {
	_, _, err := handleFindFiles(context.Background(), nil, FindFilesInput{Path: filepath.Join(t.TempDir(), "nope")})
	if err == nil {
		t.Fatal("expected error for missing root")
	}
}

func TestFindFiles_WalkErrorRecorded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based walk errors are unix-specific")
	}
	root := t.TempDir()
	denied := filepath.Join(root, "secret")
	if err := os.Mkdir(denied, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(denied, 0o755) })

	f, err := os.Open(denied)
	if err == nil {
		f.Close()
		t.Skip("process can read mode-000 directories")
	}

	_, out, err := handleFindFiles(context.Background(), nil, FindFilesInput{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Errors) == 0 {
		t.Fatalf("expected walk error recorded, matches=%v errors=%v", out.Matches, out.Errors)
	}
}

func TestListDirectory_TabSeparatedSpaceName(t *testing.T) {
	root := t.TempDir()
	name := "go mod.bak"
	if err := os.WriteFile(filepath.Join(root, name), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, _, err := handleListDirectory(context.Background(), nil, ListDirectoryInput{
		Path:   root,
		Fields: []string{"name", "size"},
	})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(mcpResultText(result))
	parts := strings.Split(line, "\t")
	if len(parts) != 2 || parts[0] != name || parts[1] != "6" {
		t.Fatalf("got %q, want name=%q size=6", line, name)
	}
}

func TestListDirectory_UnknownField(t *testing.T) {
	root := t.TempDir()
	_, _, err := handleListDirectory(context.Background(), nil, ListDirectoryInput{
		Path:   root,
		Fields: []string{"name", "permissions"},
	})
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestListDirectory_SizeAndPerm(t *testing.T) {
	root := setupFixtureTree(t)
	result, _, err := handleListDirectory(context.Background(), nil, ListDirectoryInput{
		Path:   root,
		Fields: []string{"name", "size", "perm"},
	})
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, line := range strings.Split(strings.TrimSpace(mcpResultText(result)), "\n") {
		parts := strings.Split(line, "\t")
		if parts[0] != "go.mod" {
			continue
		}
		found = true
		if len(parts) != 3 {
			t.Fatalf("go.mod line %q: want 3 columns", line)
		}
		if parts[1] != "50" {
			t.Errorf("size: got %q, want 50", parts[1])
		}
		info, err := os.Stat(filepath.Join(root, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		if parts[2] != info.Mode().Perm().String() {
			t.Errorf("perm: got %q, want %q", parts[2], info.Mode().Perm().String())
		}
	}
	if !found {
		t.Fatal("go.mod not listed")
	}
}

func TestListDirectory_Missing(t *testing.T) {
	_, _, err := handleListDirectory(context.Background(), nil, ListDirectoryInput{Path: filepath.Join(t.TempDir(), "nope")})
	if err == nil {
		t.Fatal("expected error for missing directory")
	}
}

func TestCountLines(t *testing.T) {
	root := t.TempDir()

	empty := filepath.Join(root, "empty")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := countLines(empty)
	if err != nil || n != 0 {
		t.Errorf("empty: got %d, %v; want 0, nil", n, err)
	}

	nonewline := filepath.Join(root, "nonewline")
	if err := os.WriteFile(nonewline, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err = countLines(nonewline)
	if err != nil || n != 1 {
		t.Errorf("no newline: got %d, %v; want 1, nil", n, err)
	}

	binary := filepath.Join(root, "bin")
	if err := os.WriteFile(binary, []byte("a\x00b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := countLines(binary); !errors.Is(err, errBinaryFile) {
		t.Errorf("binary: got %v, want errBinaryFile", err)
	}

	big := filepath.Join(root, "big")
	data := make([]byte, maxLineCountBytes+1)
	for i := range data {
		data[i] = 'x'
	}
	if err := os.WriteFile(big, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := countLines(big); !errors.Is(err, errFileTooLarge) {
		t.Errorf("too large: got %v, want errFileTooLarge", err)
	}
}

func TestFileInfo_BinaryOmitsLines(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "blob")
	if err := os.WriteFile(p, []byte("a\x00b"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, out, err := handleFileInfo(context.Background(), nil, FileInfoInput{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if out.Lines != nil {
		t.Errorf("lines should be omitted for binary, got %v", out.Lines)
	}
}

func TestFileInfo_Symlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "the-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	_, out, err := handleFileInfo(context.Background(), nil, FileInfoInput{Path: link})
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != "symlink" {
		t.Errorf("type: got %q, want symlink", out.Type)
	}
	if out.Lines != nil {
		t.Errorf("lines should be nil for symlink, got %v", out.Lines)
	}

	_, exists, err := handleFileExists(context.Background(), nil, FileExistsInput{Path: link})
	if err != nil {
		t.Fatal(err)
	}
	if exists.Type == nil || *exists.Type != "symlink" {
		t.Errorf("file_exists type: got %v, want symlink", exists.Type)
	}
}

// ---------------------------------------------------------------------------
// Savings regression tests
// ---------------------------------------------------------------------------
//
// Each test runs a real Bash command and the equivalent MCP handler against
// the same stable fixture, then asserts that the MCP output size stays below
// a threshold ratio of the Bash output size. If a code change bloats the MCP
// output past the threshold, the test fails.
//
// Thresholds are conservative floors derived from 74 real Claude Code sessions
// (183 filesystem Bash calls). The actual savings are typically better than
// the threshold. Run `make test-container` for reproducible numbers pinned to
// GNU coreutils (macOS ls/stat format differs from GNU).
//
// Bash commands used here mirror real agent patterns observed in transcripts,
// not textbook usage. For example, agents check file existence with
// `ls path 2>/dev/null` (which dumps the full path or listing on success),
// not the compact `test -f && echo yes`.

func TestSavings_ListDirectoryDetail(t *testing.T) {
	root := setupStableFixture(t)

	bashOut := bashOutput(t, fmt.Sprintf("ls -la %s", root))

	result, _, err := handleListDirectory(context.Background(), nil, ListDirectoryInput{Path: root, Fields: []string{"name", "type", "size"}})
	if err != nil {
		t.Fatal(err)
	}
	mcpOut := mcpResultText(result)

	// Threshold: MCP must be <= 70% of Bash (>= 30% savings)
	assertSavings(t, "list_directory(fields=name,type,size) vs ls -la", len(bashOut), len(mcpOut), 0.70)
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
