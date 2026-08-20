# fs-mcp-server

An MCP server for filesystem operations that AI coding agents should not need to shell out for.

## Why

Coding agents like Claude Code, Cursor, and OpenCode use Bash tool calls for basic filesystem operations: `ls`, `find`, `stat`, `test -f`. Each call spawns a shell process, produces human-formatted text output, and the model has to parse that text to extract the 2-3 fields it actually needs.

This server replaces those Bash calls with structured MCP tools that return JSON. The savings vary by operation:

| Operation | Avg Bash output | Avg MCP output | Savings |
|---|---|---|---|
| `ls -la` (detailed listing) | 520 chars | 322 chars | 38% |
| `find` (recursive search) | 1,186 chars | 545 chars | 54% |
| existence check (`test -f`, `ls 2>/dev/null`) | 341 chars | 22 chars | 94% |
| `stat` / `wc -l` | 289 chars | 120 chars | 58% |

Data from 74 Claude Code sessions, 183 filesystem Bash calls. Overall savings: 42% fewer output chars. Simple `ls` (names only) is at parity: the MCP server returns plain text, not JSON, so there is no overhead.

## Tools

| Tool | Replaces | What it returns |
|---|---|---|
| `list_directory` | `ls`, `ls -la` | `{entries: [{name, type, size}]}` |
| `find_files` | `find` | `{matches: ["relative/path", ...]}` |
| `file_info` | `stat`, `wc -l`, `file` | `{name, type, size, permissions, modified, lines}` |
| `file_exists` | `test -f`, `ls 2>/dev/null` | `{exists: bool, type: "file"\|"dir"\|"symlink"}` |

## Install

```
go install github.com/vrothberg/fs-mcp-server@latest
```

Or build from source:

```
git clone https://github.com/vrothberg/fs-mcp-server
cd fs-mcp-server
go build -o fs-mcp-server .
```

## Configure for Claude Code

Add to `~/.claude/settings.json`:

```json
{
  "mcpServers": {
    "fs": {
      "command": "/path/to/fs-mcp-server"
    }
  }
}
```

## Configure for Cursor / other MCP clients

Add to your MCP configuration:

```json
{
  "mcpServers": {
    "fs": {
      "command": "/path/to/fs-mcp-server",
      "transport": "stdio"
    }
  }
}
```

## License

Apache-2.0
