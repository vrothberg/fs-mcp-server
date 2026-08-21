# fs-mcp-server

An MCP server for filesystem operations that AI coding agents should not need to shell out for.

## Why

Coding agents like Claude Code, Cursor, and OpenCode use Bash tool calls for basic filesystem operations: `ls`, `find`, `stat`, `test -f`. Each call spawns a shell process, produces human-formatted text output, and the model has to parse that text to extract the 2-3 fields it actually needs.

This server replaces those Bash calls with MCP tools. MCP is not automatically cheaper than a shell; the payload has to be designed that way.

### Where tokens go to waste

1. **Agents bloat context through habit, not through need.** Models reach for `ls -la`, `find`, `stat`, and `ls path 2>/dev/null` even when they want two fields. Analysis of 251 `ls -la` calls across real sessions shows most of them only needed names. The extra columns, absolute prefixes, and error text are default Unix output plus trained muscle memory. Fifty names-only listings vs `ls -la` is ~43,000 chars of context that carries no signal.

2. **A careless MCP server can make the bill worse.** JSON repeats `"name":`, `"type":`, `"size":` on every row (~200 chars of keys for a 12-entry directory). Tool schemas sit in the prompt on every turn, paid even if the model never calls them. An uncapped `find` that walks `node_modules` dumps more tokens than the Bash it replaced. An all-or-nothing `file_info` always returns permissions and timestamps the caller did not ask for.

3. **MCP helps when the payload is designed, not when the transport is MCP.** Compact defaults (names only; `file_info` as type and size), a `fields` parameter like `ps -o`, plain text for listings, JSON only for tiny objects, relative paths, skip lists, and a hard result cap. Those choices are what save tokens. Optional knobs do not help if the default is verbose: models will not opt into compactness they were not given.

4. **Routing is still unproven.** Descriptions that name the Bash they replace are the lever we have. Whether models actually pick these tools over Bash, after paying the schema tax, needs a live eval on held-out transcripts. Until that exists, install cost is a bet.

5. **Proxies can compress the context after the fact.** Replacing the tool is not the only lever. A proxy sitting between the agent and the model can shrink tool output, JSON, and history that already landed in the prompt — including the `ls -la` the model still chose to run. That path does not depend on routing. [Headroom](https://github.com/chopratejas/headroom) is one example: a local compression layer (library, proxy, or MCP) that cuts tokens before they reach the LLM, reversibly.

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

`testdata/routing.json` maps the Bash patterns above to these tools. `go test` fails if a description drops the synonym, and it caps combined name + description + schema size so prompt tax cannot grow unnoticed.

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
