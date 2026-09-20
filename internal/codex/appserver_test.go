package codex

import (
	"encoding/json"
	"testing"

	"github.com/RuanFernandes/centurion/internal/model"
)

func TestAddInitialTurnOverridesOnlyConfiguresNewThreads(t *testing.T) {
	agent := model.AgentProfile{ModelID: "gpt-5.6-luna", ReasoningEffort: "max"}

	initial := map[string]any{}
	addInitialTurnOverrides(initial, agent, true)
	if initial["model"] != "gpt-5.6-luna" || initial["effort"] != "max" {
		t.Fatalf("initial turn did not retain explicit user selection: %#v", initial)
	}

	resumed := map[string]any{}
	addInitialTurnOverrides(resumed, agent, false)
	if len(resumed) != 0 {
		t.Fatalf("resumed turn should inherit its thread configuration, got %#v", resumed)
	}
}

func TestAgentSandboxFollowsFileWriteCapability(t *testing.T) {
	writer := model.AgentProfile{
		WorkspaceRoots: []string{"C:\\workspace"},
		ToolAllowlist:  []string{"files.read", "files.write"},
	}
	mode, policy := agentSandbox(writer)
	if mode != "workspaceWrite" || policy["type"] != "workspaceWrite" {
		t.Fatalf("writer received the wrong sandbox: mode=%q policy=%#v", mode, policy)
	}

	reader := model.AgentProfile{
		WorkspaceRoots: []string{"C:\\workspace"},
		ToolAllowlist:  []string{"files.read"},
	}
	mode, policy = agentSandbox(reader)
	if mode != "readOnly" || policy["type"] != "readOnly" {
		t.Fatalf("reader received the wrong sandbox: mode=%q policy=%#v", mode, policy)
	}
	access, ok := policy["access"].(map[string]any)
	if !ok || access["type"] != "restricted" {
		t.Fatalf("reader access was not restricted to its workspace: %#v", policy)
	}
}

func TestBuildRateLimitSnapshotSupportsSecondaryAndMultipleBuckets(t *testing.T) {
	result := rateLimitResponsePayload{
		RateLimits: rateLimitBucketPayload{
			LimitID:   "codex",
			LimitName: "Codex",
			Primary:   &rateLimitWindowPayload{UsedPercent: 31, WindowDurationMins: 300, ResetsAt: 1730948100},
			Secondary: &rateLimitWindowPayload{UsedPercent: 12, WindowDurationMins: 10080, ResetsAt: 1731542400},
		},
		RateLimitsByLimitID: map[string]rateLimitBucketPayload{
			"codex": {
				LimitID: "codex",
				Primary: &rateLimitWindowPayload{UsedPercent: 31, WindowDurationMins: 300, ResetsAt: 1730948100},
			},
			"codex_other": {
				LimitName: "Other Codex",
				Primary:   &rateLimitWindowPayload{UsedPercent: 42, WindowDurationMins: 60, ResetsAt: 1730950800},
			},
		},
	}

	snapshot := buildRateLimitSnapshot(result)
	if snapshot.LimitID != "codex" || snapshot.UsedPercent != 31 || snapshot.WindowDurationMins != 300 {
		t.Fatalf("unexpected primary usage snapshot: %#v", snapshot)
	}
	if snapshot.Secondary == nil || snapshot.Secondary.UsedPercent != 12 || snapshot.Secondary.WindowDurationMins != 10080 {
		t.Fatalf("unexpected secondary usage window: %#v", snapshot.Secondary)
	}
	if len(snapshot.Buckets) != 2 || snapshot.Buckets[1].LimitID != "codex_other" {
		t.Fatalf("unexpected rate-limit buckets: %#v", snapshot.Buckets)
	}
}

func TestBuildRateLimitSnapshotUsesFirstBucketWhenLegacyViewMissing(t *testing.T) {
	snapshot := buildRateLimitSnapshot(rateLimitResponsePayload{
		RateLimitsByLimitID: map[string]rateLimitBucketPayload{
			"codex": {
				Primary: &rateLimitWindowPayload{UsedPercent: 18, WindowDurationMins: 300},
			},
		},
	})

	if snapshot.LimitID != "codex" || snapshot.UsedPercent != 18 {
		t.Fatalf("expected the first bucket to become the primary view: %#v", snapshot)
	}
}

func TestParseMCPServerStatusEntrySupportsToolMap(t *testing.T) {
	raw := json.RawMessage(`{
        "name":"node_repl",
        "authStatus":"unsupported",
		"resources":[{"name":"Example","uri":"resource://example"}],
        "tools":{
            "js_add_node_module_dir":{"description":"Add a module directory"},
            "js":{"description":"Run JavaScript"}
        }
    }`)

	server, ok := parseMCPServerStatusEntry(raw)
	if !ok {
		t.Fatal("expected MCP server entry to be parsed")
	}
	if server.ID != "node_repl" || server.Name != "node_repl" {
		t.Fatalf("unexpected server identity: %#v", server)
	}
	if server.Status != "ready" {
		t.Fatalf("expected a server with discovered tools to be ready, got %q", server.Status)
	}
	if len(server.Tools) != 2 || server.Tools[0].Name != "js" || server.Tools[1].Name != "js_add_node_module_dir" {
		t.Fatalf("unexpected parsed tools: %#v", server.Tools)
	}
	if len(server.Resources) != 1 || server.Resources[0] != "resource://example" {
		t.Fatalf("unexpected parsed resources: %#v", server.Resources)
	}
}

func TestParseMCPServerStatusEntrySupportsToolListAndErrors(t *testing.T) {
	raw := json.RawMessage(`{
        "id":"github",
        "status":"failed",
        "error":{"message":"authorization failed"},
        "tools":[{"name":"issues.list","description":"List issues"}]
    }`)

	server, ok := parseMCPServerStatusEntry(raw)
	if !ok {
		t.Fatal("expected MCP server entry to be parsed")
	}
	if server.ID != "github" || server.Status != "failed" || server.Error != "authorization failed" {
		t.Fatalf("unexpected failed server: %#v", server)
	}
	if len(server.Tools) != 1 || server.Tools[0].Name != "issues.list" {
		t.Fatalf("unexpected parsed tool list: %#v", server.Tools)
	}
}
