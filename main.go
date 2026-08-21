package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	version               = "0.1.0"
	defaultFindMaxResults = 1000
	maxFindMaxResults     = 10000
	maxLineCountBytes     = 1 << 20 // 1 MiB
)

var (
	errBinaryFile   = errors.New("binary file")
	errFileTooLarge = errors.New("file exceeds line-count size limit")

	listFieldNames = map[string]bool{
		"name":     true,
		"type":     true,
		"size":     true,
		"perm":     true,
		"modified": true,
		"lines":    true,
	}
)

// --- Input/output types ---
//
// Struct tags serve double duty: `json` controls wire format, `jsonschema`
// generates the MCP tool input schema via jsonschema.For[T](). The SDK
// validates incoming requests against that schema before calling the handler.

type ListDirectoryInput struct {
	Path   string   `json:"path" jsonschema:"absolute path to list"`
	Fields []string `json:"fields,omitempty" jsonschema:"columns to include per entry, like ps -o. Available: name (always included), type, size, perm, modified, lines. Example: [\"name\",\"size\",\"perm\"]. Default: name only. Columns are tab-separated."`
}

type FindFilesInput struct {
	Path       string `json:"path" jsonschema:"root directory to search from"`
	Pattern    string `json:"pattern,omitempty" jsonschema:"glob to match basenames (*.go) or relative paths (cmd/*.go)"`
	Type       string `json:"type,omitempty" jsonschema:"filter by type: file, dir, or symlink"`
	MaxDepth   *int   `json:"max_depth,omitempty" jsonschema:"maximum directory depth to recurse. depth 1 = immediate children only"`
	MaxResults *int   `json:"max_results,omitempty" jsonschema:"maximum matches to return. Default 1000, hard max 10000"`
}

type FindFilesOutput struct {
	Matches   []string `json:"matches"`
	Truncated bool     `json:"truncated,omitempty"`
	Errors    []string `json:"errors,omitempty"`
}

type FileInfoInput struct {
	Path string `json:"path" jsonschema:"absolute path to the file or directory"`
}

type FileInfoOutput struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // "file", "dir", "symlink"
	Size        int64  `json:"size"`
	Permissions string `json:"permissions"`
	Modified    string `json:"modified"`
	Lines       *int   `json:"lines,omitempty"`
}

type FileExistsInput struct {
	Path string `json:"path" jsonschema:"absolute path to check"`
}

type FileExistsOutput struct {
	Exists bool    `json:"exists"`
	Type   *string `json:"type,omitempty"` // "file", "dir", "symlink", null if not exists
}

// --- Handlers ---
//
// Each handler follows the ToolHandlerFor[In, Out] signature from the SDK.
// Return paths:
//   - (nil, out, nil): SDK marshals `out` as structured JSON content.
//   - (result, _, nil): raw CallToolResult is sent as-is (used for plain text).
//   - (_, _, err):      SDK returns an error to the caller.

// handleListDirectory returns directory contents as compact plain text.
// Fields control which columns appear per line (like ps -o). With no fields
// or just "name", output is one name per line. Additional fields are appended
// tab-separated so names with spaces stay unambiguous:
// fields=["name","size","perm"] -> "go.mod\t484\t-rw-r--r--".
func handleListDirectory(_ context.Context, _ *mcp.CallToolRequest, in ListDirectoryInput) (*mcp.CallToolResult, any, error) {
	if err := validateListFields(in.Fields); err != nil {
		return nil, nil, err
	}

	entries, err := os.ReadDir(in.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading directory: %w", err)
	}

	wantFields := make(map[string]bool)
	for _, f := range in.Fields {
		wantFields[f] = true
	}
	needsInfo := wantFields["type"] || wantFields["size"] || wantFields["perm"] || wantFields["modified"] || wantFields["lines"]

	var b strings.Builder
	lineBudget := int64(maxLineCountBytes)
	for _, e := range entries {
		var info os.FileInfo
		if needsInfo {
			info, _ = e.Info()
		}

		parts := []string{e.Name()}

		for _, f := range in.Fields {
			switch f {
			case "name":
				continue
			case "type":
				parts = append(parts, entryType(e.Type()))
			case "size":
				if info != nil && !e.IsDir() {
					parts = append(parts, fmt.Sprintf("%d", info.Size()))
				} else {
					parts = append(parts, "-")
				}
			case "perm":
				if info != nil {
					parts = append(parts, info.Mode().Perm().String())
				} else {
					parts = append(parts, "-")
				}
			case "modified":
				if info != nil {
					parts = append(parts, info.ModTime().Format(time.RFC3339))
				} else {
					parts = append(parts, "-")
				}
			case "lines":
				col := "-"
				if info != nil && info.Mode().IsRegular() && info.Size() <= maxLineCountBytes && info.Size() <= lineBudget {
					p := filepath.Join(in.Path, e.Name())
					if n, err := countLines(p); err == nil {
						col = fmt.Sprintf("%d", n)
						lineBudget -= info.Size()
					}
				}
				parts = append(parts, col)
			}
		}

		b.WriteString(strings.Join(parts, "\t"))
		b.WriteByte('\n')
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
	}, nil, nil
}

// handleFindFiles walks a directory tree and returns relative paths. The main
// token saving comes from stripping the repeated absolute prefix that `find`
// prints on every line (e.g. /Users/name/projects/foo/ x N matches).
// Depth semantics match `find -maxdepth`: depth 1 = immediate children only,
// and those children are included (directories at the boundary are listed,
// then not descended into).
func handleFindFiles(_ context.Context, _ *mcp.CallToolRequest, in FindFilesInput) (*mcp.CallToolResult, FindFilesOutput, error) {
	switch in.Type {
	case "", "file", "dir", "symlink":
	default:
		return nil, FindFilesOutput{}, fmt.Errorf("invalid type %q; want file, dir, or symlink", in.Type)
	}
	if in.Pattern != "" {
		if _, err := path.Match(filepath.ToSlash(in.Pattern), ""); err != nil {
			return nil, FindFilesOutput{}, fmt.Errorf("invalid pattern: %w", err)
		}
	}

	root := filepath.Clean(in.Path)
	maxDepth := -1
	if in.MaxDepth != nil {
		maxDepth = *in.MaxDepth
	}
	limit := findMaxResults(in)

	var matches []string
	var walkErrs []string
	truncated := false

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d == nil || p == root {
				return err
			}
			report := p
			if rel, relErr := filepath.Rel(root, p); relErr == nil {
				report = rel
			}
			walkErrs = append(walkErrs, fmt.Sprintf("%s: %v", report, err))
			return nil
		}

		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}

		if d.IsDir() && skipWalkDir(d.Name()) {
			return fs.SkipDir
		}

		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if maxDepth >= 0 && depth > maxDepth {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		include := true
		if in.Type != "" && entryType(d.Type()) != in.Type {
			include = false
		}
		if include && in.Pattern != "" {
			matched, mErr := matchGlob(in.Pattern, d.Name(), rel)
			if mErr != nil {
				return mErr
			}
			if !matched {
				include = false
			}
		}
		if include {
			matches = append(matches, rel)
			if len(matches) >= limit {
				truncated = true
				return fs.SkipAll
			}
		}

		if maxDepth >= 0 && d.IsDir() && depth >= maxDepth {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, FindFilesOutput{}, fmt.Errorf("walking directory: %w", err)
	}
	if matches == nil {
		matches = []string{}
	}
	return nil, FindFilesOutput{Matches: matches, Truncated: truncated, Errors: walkErrs}, nil
}

// handleFileInfo combines stat + wc -l + file-type detection into a single
// call. Uses Lstat so symlinks are reported as symlinks, not resolved.
func handleFileInfo(_ context.Context, _ *mcp.CallToolRequest, in FileInfoInput) (*mcp.CallToolResult, FileInfoOutput, error) {
	info, err := os.Lstat(in.Path)
	if err != nil {
		return nil, FileInfoOutput{}, fmt.Errorf("stat: %w", err)
	}

	out := FileInfoOutput{
		Name:        info.Name(),
		Type:        fileType(info.Mode()),
		Size:        info.Size(),
		Permissions: info.Mode().Perm().String(),
		Modified:    info.ModTime().Format(time.RFC3339),
	}

	if info.Mode().IsRegular() {
		lines, err := countLines(in.Path)
		if err == nil {
			out.Lines = &lines
		}
	}

	return nil, out, nil
}

// handleFileExists replaces patterns like `test -f`, `ls path 2>/dev/null`,
// and `ls -la path 2>&1`. In real sessions these patterns average 341 chars
// of output (error text, directory listings used as existence probes, etc.);
// the structured response is 16-29 chars.
func handleFileExists(_ context.Context, _ *mcp.CallToolRequest, in FileExistsInput) (*mcp.CallToolResult, FileExistsOutput, error) {
	info, err := os.Lstat(in.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, FileExistsOutput{Exists: false}, nil
		}
		return nil, FileExistsOutput{}, fmt.Errorf("stat: %w", err)
	}

	t := fileType(info.Mode())
	return nil, FileExistsOutput{Exists: true, Type: &t}, nil
}

// --- Helpers ---

func validateListFields(fields []string) error {
	for _, f := range fields {
		if !listFieldNames[f] {
			return fmt.Errorf("unknown field %q; available: name, type, size, perm, modified, lines", f)
		}
	}
	return nil
}

func findMaxResults(in FindFilesInput) int {
	n := defaultFindMaxResults
	if in.MaxResults != nil {
		n = *in.MaxResults
	}
	if n <= 0 {
		n = defaultFindMaxResults
	}
	if n > maxFindMaxResults {
		n = maxFindMaxResults
	}
	return n
}

func skipWalkDir(name string) bool {
	return name == ".git" || name == "node_modules"
}

// matchGlob treats patterns with a slash as relative-path globs (cmd/*.go)
// and patterns without as basename globs (*.go), matching find -path vs -name.
func matchGlob(pattern, name, rel string) (bool, error) {
	pattern = filepath.ToSlash(pattern)
	if strings.Contains(pattern, "/") {
		return path.Match(pattern, filepath.ToSlash(rel))
	}
	return path.Match(pattern, name)
}

// entryType classifies a DirEntry's file mode. Used by ReadDir results where
// the mode comes from DirEntry.Type() which only has the type bits set.
func entryType(mode fs.FileMode) string {
	switch {
	case mode&fs.ModeSymlink != 0:
		return "symlink"
	case mode&fs.ModeDir != 0:
		return "dir"
	default:
		return "file"
	}
}

// fileType classifies a full os.FileMode from Lstat. Separate from entryType
// because FileMode.IsDir() checks different bits than ModeDir alone.
func fileType(mode fs.FileMode) string {
	switch {
	case mode&fs.ModeSymlink != 0:
		return "symlink"
	case mode.IsDir():
		return "dir"
	default:
		return "file"
	}
}

func countLines(filename string) (int, error) {
	f, err := os.Open(filename)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	buf := make([]byte, 32*1024)
	count := 0
	var total int64
	var last byte
	gotAny := false
	for {
		n, err := f.Read(buf)
		if n > 0 {
			gotAny = true
			total += int64(n)
			if total > maxLineCountBytes {
				return 0, errFileTooLarge
			}
			for _, b := range buf[:n] {
				if b == 0 {
					return 0, errBinaryFile
				}
				if b == '\n' {
					count++
				}
				last = b
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if !gotAny {
		return 0, nil
	}
	if last != '\n' {
		count++
	}
	return count, nil
}

// --- Main ---

func main() {
	ctx := context.Background()

	server := mcp.NewServer(
		&mcp.Implementation{Name: "fs-mcp-server", Version: version},
		nil,
	)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_directory",
		Description: "List the contents of a directory. By default returns one name per line. Use the fields parameter (like ps -o) to add only the columns you need: type, size, perm, modified, lines. Extra columns are tab-separated. Example: fields=[\"name\",\"size\"] returns \"go.mod\\t484\" per line. Only request fields you will actually use.",
		InputSchema: mustSchema[ListDirectoryInput](),
	}, handleListDirectory)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_files",
		Description: "Recursively find files and directories matching a glob. *.go matches any basename; cmd/*.go matches relative paths. Returns relative paths from the search root. Skips .git and node_modules. Caps at 1000 matches (max_results, hard max 10000); truncated is true if the cap was hit.",
		InputSchema: mustSchema[FindFilesInput](),
	}, handleFindFiles)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "file_info",
		Description: "Get metadata about a file or directory: name, type, size, permissions, modification time, and line count (for regular files). Replaces stat/wc -l/file in a single call. Line count is omitted for binary files and files larger than 1MiB.",
		InputSchema: mustSchema[FileInfoInput](),
	}, handleFileInfo)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "file_exists",
		Description: "Check whether a path exists and what type it is (file, dir, symlink). Returns a boolean and type. Replaces `test -f`, `ls path 2>/dev/null`, and similar existence checks with no error-text parsing.",
		InputSchema: mustSchema[FileExistsInput](),
	}, handleFileExists)

	session, err := server.Connect(ctx, &mcp.StdioTransport{}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect: %v\n", err)
		os.Exit(1)
	}
	if err := session.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "session error: %v\n", err)
		os.Exit(1)
	}
}

// mustSchema generates a JSON Schema from Go struct tags for MCP tool
// input validation. Uses google/jsonschema-go which the MCP SDK depends on.
func mustSchema[T any]() *jsonschema.Schema {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("schema inference failed: %v", err))
	}
	return s
}
