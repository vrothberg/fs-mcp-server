# fs-mcp-server

An MCP server for filesystem operations that AI coding agents should not need to shell out for.

## Why

Coding agents like Claude Code, Cursor, and OpenCode use Bash tool calls for basic filesystem operations: `ls`, `find`, `stat`, `test -f`. Each call spawns a shell process, produces human-formatted text output, and the model has to parse that text to extract the 2-3 fields it actually needs.

This server replaces those Bash calls with MCP tools that return compact plain text instead of verbose shell output.

### Where tokens go to waste

Three layers of waste stack up in the typical agent-to-filesystem path:

1. **Model drift.** Claude and Sonnet models default to `ls -la` out of habit, fetching permissions, owner, group, and timestamps when they just need file names. Analysis of 251 `ls -la` calls across real sessions shows that most of them only needed names. The tool description is the lever: it shapes what the model asks for.

2. **JSON overhead.** MCP's default wire format repeats key names (`"name":`, `"type":`, `"size":`) on every entry. For a 12-entry directory that is ~200 chars of repeated keys carrying zero information. Plain text with positional fields eliminates that entirely. The LLM does not need JSON to understand a tab-separated `go.mod	484`.

3. **All-or-nothing APIs.** Most MCP tools offer one fixed response shape. A `ps -o` style `fields` parameter lets the caller request exactly the columns it needs, so a simple listing costs ~100 chars and a full audit costs ~650, instead of always paying ~960 for `ls -la` or ~1,300 for per-file JSON.

The compound effect is real: if an agent runs 50 directory listings in a session, the difference between `ls -la` and names-only is ~43,000 chars of wasted context. That is tokens the model has to read, attend to, and pay for, all carrying no useful signal.

### Savings by operation

| Fields requested | MCP output | vs `ls -la` (~960 chars) |
|---|---|---|
| `name` only (default) | ~100 chars | **90% savings** |
| `name, size` | ~135 chars | **86% savings** |
| `name, type` | ~150 chars | **84% savings** |
| `name, type, size` | ~195 chars | **80% savings** |
| `name, type, size, perm` | ~325 chars | **66% savings** |
| `name, type, size, perm, modified` | ~650 chars | **32% savings** |

Other tools:

| Operation | Bash equivalent | Savings |
|---|---|---|
| `find_files` | `find -name` | 57% |
| `file_exists` | `test -f`, `ls 2>/dev/null` | 42-82% |
| `file_info` | `stat` + `wc -l` | 59% |

## Tools

| Tool | Replaces | What it returns |
|---|---|---|
| `list_directory` | `ls`, `ls -la` | One name per line. `fields` (like `ps -o`) adds tab-separated columns: type, size, perm, modified, lines. Default: name only. |
| `find_files` | `find -name` | One relative path per line. Basename globs (`*.go`) or path globs (`cmd/*.go`). Skips `.git` and `node_modules`. Caps at 1000 matches (`max_results`); extra content `truncated` if the cap was hit. Walk errors are reported in a follow-up `errors:` block. |
| `file_info` | `stat`, `wc -l`, `file` | JSON with only requested `fields`. Default: `type`, `size`. Optional: name, perm, modified, lines (omitted for binaries and files > 1MiB). |
| `file_exists` | `test -f`, `ls path 2>/dev/null` | `{exists: bool, type: "file"\|"dir"\|"symlink"}` |

`list_directory` and `find_files` use plain text so listings do not pay JSON key repetition. `file_info` and `file_exists` stay JSON: they return one small object, and `file_info`'s `fields` parameter is what avoids all-or-nothing payloads.

## Will the model call these tools?

The four tool schemas are injected into the prompt on every turn. That cost is paid whether or not the model picks them over Bash.

Each description names the Bash it replaces (`ls`, `find -name`, `stat`, `test -f`, `ls path 2>/dev/null`) so a model that was about to shell out has a direct mapping. `testdata/routing.json` is a corpus of those real-session patterns; `go test` fails if a description drops the synonym. Combined name + description + input schema size is also capped so prompt tax cannot grow unnoticed.

A live tool-choice eval against held-out transcripts (does the model actually select these tools, including schema tax in the token budget) is still open.

## Install and configure

Install:

```
go install github.com/vrothberg/fs-mcp-server@latest
```

Add the MCP server to Claude Code:

```
claude mcp add fs-mcp-server -- go run github.com/vrothberg/fs-mcp-server@latest
```

Or, if you prefer pointing at the installed binary:

```
claude mcp add fs-mcp-server -- $(go env GOPATH)/bin/fs-mcp-server
```

For Cursor or other MCP clients, add to your MCP configuration:

```json
{
  "mcpServers": {
    "fs": {
      "command": "go",
      "args": ["run", "github.com/vrothberg/fs-mcp-server@latest"],
      "transport": "stdio"
    }
  }
}
```

## License

Apache-2.0
