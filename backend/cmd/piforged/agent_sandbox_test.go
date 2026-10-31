package main

import (
	"strings"
	"testing"
)

// The agent's paths inside the sandbox are a translation, not a guess: the
// control plane's /data/sandboxes/<u>/<p> is the container's /workspace. Getting
// this wrong is not a cosmetic bug — it puts a path in the session file's cwd
// header that does not exist in the container, so every tool the agent runs
// starts in the wrong place (or fails).
func TestContainerUnderMapsTheWorkspaceAndNothingElse(t *testing.T) {
	root := "/data/sandboxes/usr_1/prj_1"
	cases := []struct {
		name   string
		host   string
		want   string
		mapped bool
	}{
		{"the checkout", root + "/repo", "/workspace/repo", true},
		{"the workspace itself", root, "", false},
		{"a branch worktree", root + "/worktrees/feature", "/workspace/worktrees/feature", true},
		{"the session file", root + "/repo/.piforge/sessions/ses_1.jsonl", "/workspace/repo/.piforge/sessions/ses_1.jsonl", true},
		{"another tenant", "/data/sandboxes/usr_2/prj_9/repo", "", false},
		{"the control plane's data dir", "/data/piforge.db", "", false},
		{"a relative path", "repo", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := containerUnder(root, "/workspace", tc.host)
			if ok != tc.mapped {
				t.Fatalf("mapped = %v, want %v (%q)", ok, tc.mapped, got)
			}
			if ok && got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The environment handed to the sandbox is the session's environment with three
// deliberate changes, and every one of them is a bug if it regresses:
//
//   - PI_CODING_AGENT_DIR points into the sandbox (models.json is written there
//     through the file API);
//   - GIT_CONFIG_* is dropped (it names host paths and exists to lift a
//     "dubious ownership" refusal that cannot happen when the agent runs as the
//     account that owns the checkout);
//   - HOME is the sandbox user's, because `docker exec -u` does not set it.
func TestSandboxAgentEnvKeepsTheTokenAndMovesThePaths(t *testing.T) {
	host := []string{
		"PI_CODING_AGENT_DIR=/data/pi-agents/ses_1",
		"ANTHROPIC_BASE_URL=http://pf-control:7700/gw/llm/tok",
		"ANTHROPIC_API_KEY=tok",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=safe.directory",
		"GIT_CONFIG_VALUE_0=/data/sandboxes/usr_1/prj_1/repo",
		"UNRELATED=1",
	}
	got := sandboxAgentEnv(host, "/workspace/.piforge/agents/ses_1")
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "PI_CODING_AGENT_DIR=/workspace/.piforge/agents/ses_1") {
		t.Fatalf("the agent dir was not moved into the sandbox: %v", got)
	}
	if !strings.Contains(joined, "ANTHROPIC_API_KEY=tok") {
		t.Fatalf("the gateway token was dropped: %v", got)
	}
	if strings.Contains(joined, "/data/") {
		t.Fatalf("a control-plane path survived into the sandbox environment: %v", got)
	}
	if !strings.Contains(joined, "HOME=/home/gem") {
		t.Fatalf("HOME is not the sandbox user's: %v", got)
	}
	if !strings.Contains(joined, "UNRELATED=1") {
		t.Fatalf("an unrelated entry was dropped: %v", got)
	}
}
