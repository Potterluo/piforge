package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

// A container provider creates the checkout as the sandbox user, while the agent
// process runs as root. git 2.39 refuses such a repository ("detected dubious
// ownership") before running anything, which breaks the `git commit` a coding task
// ends with. The agent cannot be given `-c safe.directory=…` because it passes its
// own flags, so the grant goes in through the environment git reads.
func TestAgentGitEnvNamesOnlyThisSessionsCheckout(t *testing.T) {
	// The second path is derived with the host's separators, which is what the
	// running deployment uses: a container path inside a Linux container, a Windows
	// path when the control plane is a host process on Windows.
	branchWorktree := filepath.Join(string(filepath.Separator), "data", "sandboxes", "usr_1", "prj_1", "worktrees", "fix-cache")
	branchRepo := filepath.Join(filepath.Dir(filepath.Dir(branchWorktree)), "repo")
	mainWorktree := filepath.Join(string(filepath.Separator), "data", "sandboxes", "usr_1", "prj_1", "repo")
	otherWorktree := filepath.Join(string(filepath.Separator), "data", "sandboxes", "usr_2", "prj_9", "repo")
	tests := []struct {
		name     string
		kind     string
		worktree string
		want     []string
	}{
		{
			name:     "local placement needs no grant",
			kind:     "local",
			worktree: mainWorktree,
		},
		{
			name:     "no worktree yet: nothing to grant",
			kind:     "runner",
			worktree: "  ",
		},
		{
			name:     "runner: the main checkout is the worktree",
			kind:     "runner",
			worktree: mainWorktree,
			want: []string{
				"GIT_CONFIG_COUNT=1",
				"GIT_CONFIG_KEY_0=safe.directory",
				"GIT_CONFIG_VALUE_0=" + mainWorktree,
			},
		},
		{
			// A linked worktree keeps its gitdir in the main checkout, which git
			// checks too; both are named, and nothing else.
			name:     "docker: a branch session also names the repository it links into",
			kind:     "docker",
			worktree: branchWorktree,
			want: []string{
				"GIT_CONFIG_COUNT=2",
				"GIT_CONFIG_KEY_0=safe.directory",
				"GIT_CONFIG_VALUE_0=" + branchWorktree,
				"GIT_CONFIG_KEY_1=safe.directory",
				"GIT_CONFIG_VALUE_1=" + branchRepo,
			},
		},
		{
			// The value is this session's path, never the wildcard: trusting every
			// repository the agent walks into would defeat the check for cloned
			// projects.
			name:     "the grant is a path, not a wildcard",
			kind:     "runner",
			worktree: otherWorktree,
			want: []string{
				"GIT_CONFIG_COUNT=1",
				"GIT_CONFIG_KEY_0=safe.directory",
				"GIT_CONFIG_VALUE_0=" + otherWorktree,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agentGitEnv(tt.kind, tt.worktree)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("agentGitEnv(%q, %q) = %q, want %q", tt.kind, tt.worktree, got, tt.want)
			}
			for _, entry := range got {
				if entry == "GIT_CONFIG_VALUE_0=*" || entry == "GIT_CONFIG_VALUE_1=*" {
					t.Fatalf("the grant trusts every repository: %q", got)
				}
			}
		})
	}
}
