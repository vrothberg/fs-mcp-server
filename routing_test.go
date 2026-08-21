package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxToolPromptBytes is a ceiling on name + description + input schema for all
// four tools combined. These bytes sit in the model prompt every turn, whether
// or not the tools are used. The cap is loose enough for wording tweaks and
// tight enough that schema tax cannot double unnoticed.
const maxToolPromptBytes = 8000

type routingCase struct {
	ID          string   `json:"id"`
	Bash        string   `json:"bash"`
	Intent      string   `json:"intent"`
	Tool        string   `json:"tool"`
	MustMention []string `json:"must_mention"`
}

func TestRoutingCorpus_DescriptionsSteerBash(t *testing.T) {
	data, err := os.ReadFile("testdata/routing.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []routingCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("empty routing corpus")
	}

	tools := listServerTools(t)
	for _, c := range cases {
		tool, ok := tools[c.Tool]
		if !ok {
			t.Errorf("%s: tool %q not registered", c.ID, c.Tool)
			continue
		}
		desc := strings.ToLower(tool.Description)
		for _, needle := range c.MustMention {
			if !strings.Contains(desc, strings.ToLower(needle)) {
				t.Errorf("%s: %s description %q does not mention %q (bash %q, intent %q)",
					c.ID, c.Tool, tool.Description, needle, c.Bash, c.Intent)
			}
		}
	}
}

func TestToolSchemaTax(t *testing.T) {
	tools := listServerTools(t)
	total := 0
	for name, tool := range tools {
		n := len(tool.Name) + len(tool.Description)
		if tool.InputSchema != nil {
			b, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatalf("schema %s: %v", name, err)
			}
			n += len(b)
		}
		t.Logf("%s: %d prompt bytes (name+description+schema)", name, n)
		total += n
	}
	t.Logf("total tool prompt bytes: %d (max %d)", total, maxToolPromptBytes)
	if total == 0 {
		t.Fatal("no tools")
	}
	if total > maxToolPromptBytes {
		t.Errorf("tool schemas+descriptions are %d bytes; cap is %d so prompt tax cannot grow unnoticed", total, maxToolPromptBytes)
	}
}

func listServerTools(t *testing.T) map[string]*mcp.Tool {
	t.Helper()
	cs := connectServer(t)
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]*mcp.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		out[tool.Name] = tool
	}
	return out
}
