package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const version = "0.1.0"

// --- Input/output types ---
//
// Struct tags serve double duty: `json` controls wire format, `jsonschema`
// generates the MCP tool input schema via jsonschema.For[T](). The SDK
// validates incoming requests against that schema before calling the handler.

type ListDirectoryInput struct {
	Path   string `json:"path" jsonschema:"absolute path to list"`
	Detail bool   `json:"detail,omitempty" jsonschema:"include type and size for each entry (default: names only)"`
}

type DirEntry struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	Size *int64 `json:"size,omitempty"`
}

type ListDirectoryOutput struct {
	Entries []DirEntry `json:"entries"`
}

type FindFilesInput struct {
	Path    string `json:"path" jsonschema:"root directory to search from"`
	Pattern string `json:"pattern,omitempty" jsonschema:"glob pattern to match file names (e.g. *.go)"`
	Type    string `json:"type,omitempty" jsonschema:"filter by type: file, dir, or symlink"`
	MaxDepth *int  `json:"max_depth,omitempty" jsonschema:"maximum directory depth to recurse"`
}

type FindFilesOutput struct {
	Matches []string `json:"matches"`
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

// handleListDirectory returns directory contents in one of two modes:
//   - detail=false (default): plain text, one name per line. Identical output
//     to `ls`, so there is zero token overhead vs Bash. This avoids the trap
//     of wrapping simple names in JSON and making things worse.
//   - detail=true: structured JSON with name, type, and size per entry.
//     Smaller than `ls -la` because it drops owner, group, date formatting,
//     and alignment padding.
func handleListDirectory(_ context.Context, _ *mcp.CallToolRequest, in ListDirectoryInput) (*mcp.CallToolResult, ListDirectoryOutput, error) {
	entries, err := os.ReadDir(in.Path)
	if err != nil {
		return nil, ListDirectoryOutput{}, fmt.Errorf("reading directory: %w", err)
	}

	if !in.Detail {
		var b strings.Builder
		for _, e := range entries {
			b.WriteString(e.Name())
			b.WriteByte('\n')
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
		}, ListDirectoryOutput{}, nil
	}

	out := ListDirectoryOutput{Entries: make([]DirEntry, 0, len(entries))}
	for _, e := range entries {
		entry := DirEntry{
			Name: e.Name(),
			Type: entryType(e.Type()),
		}
		if info, err := e.Info(); err == nil && !e.IsDir() {
			size := info.Size()
			entry.Size = &size
		}
		out.Entries = append(out.Entries, entry)
	}
	return nil, out, nil
}

// handleFindFiles walks a directory tree and returns relative paths. The main
// token saving comes from stripping the repeated absolute prefix that `find`
// prints on every line (e.g. /Users/name/projects/foo/ x N matches).
// Depth semantics match `find -maxdepth`: depth 1 = immediate children only.
func handleFindFiles(_ context.Context, _ *mcp.CallToolRequest, in FindFilesInput) (*mcp.CallToolResult, FindFilesOutput, error) {
	root := filepath.Clean(in.Path)
	var matches []string
	maxDepth := -1
	if in.MaxDepth != nil {
		maxDepth = *in.MaxDepth
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}

		if maxDepth >= 0 {
			depth := strings.Count(rel, string(filepath.Separator)) + 1
			if d.IsDir() && depth >= maxDepth {
				return fs.SkipDir
			}
			if depth > maxDepth {
				return nil
			}
		}

		if in.Type != "" && entryType(d.Type()) != in.Type {
			return nil
		}

		if in.Pattern != "" {
			matched, _ := filepath.Match(in.Pattern, d.Name())
			if !matched {
				return nil
			}
		}

		matches = append(matches, rel)
		return nil
	})
	if err != nil {
		return nil, FindFilesOutput{}, fmt.Errorf("walking directory: %w", err)
	}
	if matches == nil {
		matches = []string{}
	}
	return nil, FindFilesOutput{Matches: matches}, nil
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

func countLines(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, nil
	}
	count := 0
	for _, b := range data {
		if b == '\n' {
			count++
		}
	}
	if data[len(data)-1] != '\n' {
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
		Description: "List the contents of a directory. Returns structured entries with name, type (file/dir/symlink), and size. More token-efficient than `ls` because output is structured JSON with no formatting noise.",
		InputSchema: mustSchema[ListDirectoryInput](),
	}, handleListDirectory)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_files",
		Description: "Recursively find files and directories matching a glob pattern. Returns relative paths from the search root. More token-efficient than `find` because paths are relative (no repeated absolute prefixes) and output is structured.",
		InputSchema: mustSchema[FindFilesInput](),
	}, handleFindFiles)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "file_info",
		Description: "Get metadata about a file or directory: name, type, size, permissions, modification time, and line count (for regular files). Replaces stat/wc -l/file in a single call.",
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
