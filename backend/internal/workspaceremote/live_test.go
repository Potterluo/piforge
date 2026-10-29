package workspaceremote

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/gitx"
)

// Optional live check against a running AIO sandbox. It is skipped unless the
// endpoint is exported, because the image is 13 GB and not every machine can
// start it.
//
// PIFORGE_AIO_URL=http://127.0.0.1:46027 go test ./internal/workspaceremote -run Live -v
//
// The point of the test is not "the API answers": it is that a file written
// through the workspace and a file read by a shell command are the same file.
func TestLiveWorkspaceSharesTheSandboxFilesystem(t *testing.T) {
	base := os.Getenv("PIFORGE_AIO_URL")
	if base == "" {
		t.Skip("PIFORGE_AIO_URL is not set: no live sandbox to talk to")
	}
	client, err := aio.New(aio.Config{BaseURL: base, APIKey: os.Getenv("PIFORGE_AIO_KEY"), Root: "/workspace"})
	if err != nil {
		t.Fatalf("aio.New: %v", err)
	}
	ctx := context.Background()

	suffix := make([]byte, 6)
	if _, rerr := rand.Read(suffix); rerr != nil {
		t.Fatalf("random suffix: %v", rerr)
	}
	root := path.Join("/workspace", "piforge-live-"+hex.EncodeToString(suffix))
	if res, cerr := client.Exec(ctx, "mkdir -p -- "+aio.Quote(root), ""); cerr != nil || res.ExitCode != 0 {
		t.Fatalf("mkdir %s: %v: %s", root, cerr, res.Output)
	}
	t.Cleanup(func() {
		_, _ = client.Exec(context.Background(), "rm -rf -- "+aio.Quote(root), "")
	})

	backend := staticBackend{target: Target{Client: client, Root: root}}
	ws, werr := New(Config{
		Backend: backend,
		Git:     gitx.New(gitx.Config{Exec: ShellExecutor{Backend: backend}.Run}),
	})
	if werr != nil {
		t.Fatalf("New: %v", werr)
	}

	body := []byte("hello from the remote workspace\n")
	if err := ws.WriteFile(ctx, root, "notes.txt", body); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// `cat` alone cannot prove byte equality: the shell endpoint trims the
	// trailing newline of its output. base64 can, so the bytes are compared
	// exactly and `cat` is kept only as the human-readable proof.
	cat, cerr := client.Exec(ctx, "cat -- "+aio.Quote(path.Join(root, "notes.txt")), "")
	if cerr != nil {
		t.Fatalf("cat through the shell: %v", cerr)
	}
	if cat.ExitCode != 0 {
		t.Fatalf("cat exit %d: %s", cat.ExitCode, cat.Output)
	}
	t.Logf("docker exec cat: %q", cat.Output)

	res, cerr := client.Exec(ctx, "base64 < "+aio.Quote(path.Join(root, "notes.txt"))+" | tr -d '\\n'", "")
	if cerr != nil {
		t.Fatalf("base64 through the shell: %v", cerr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("base64 exit %d: %s", res.ExitCode, res.Output)
	}
	if got, want := strings.TrimSpace(res.Output), base64.StdEncoding.EncodeToString(body); got != want {
		t.Fatalf("the shell sees %q (base64 %s), the panel wrote %q (%s)", cat.Output, got, body, want)
	}
	t.Logf("same bytes, base64 %s", res.Output)

	// And the other direction: what the agent writes, the panel reads.
	if res, xerr := client.Exec(ctx, "printf 'written by the agent\\n' > "+aio.Quote(path.Join(root, "agent.txt")), ""); xerr != nil || res.ExitCode != 0 {
		t.Fatalf("agent write: %v: %s", xerr, res.Output)
	}
	content, rerr := ws.ReadFile(ctx, root, "agent.txt")
	if rerr != nil {
		t.Fatalf("ReadFile: %v", rerr)
	}
	if content.Body != "written by the agent\n" {
		t.Fatalf("panel read %q", content.Body)
	}

	nodes, terr := ws.Tree(ctx, root, "", 1)
	if terr != nil {
		t.Fatalf("Tree: %v", terr)
	}
	seen := map[string]bool{}
	for _, n := range nodes {
		seen[n.Name] = true
	}
	if !seen["notes.txt"] || !seen["agent.txt"] {
		t.Fatalf("tree = %+v", nodes)
	}

	// Git also runs in there: init a repository and read its status through the
	// same bridge the panel uses.
	if res, xerr := client.Exec(ctx, "cd -- "+aio.Quote(root)+" && git init -q -b main && echo tracked > tracked.txt && git add tracked.txt", ""); xerr != nil || res.ExitCode != 0 {
		t.Fatalf("git init: %v: %s", xerr, res.Output)
	}
	status, serr := ws.Status(ctx, root)
	if serr != nil {
		t.Fatalf("Status: %v", serr)
	}
	if status.Branch != "main" {
		t.Fatalf("branch = %q", status.Branch)
	}
	var tracked bool
	for _, change := range status.Changes {
		if change.Path == "tracked.txt" && strings.Contains(change.Index+change.Worktree, "A") {
			tracked = true
		}
	}
	if !tracked {
		t.Fatalf("status did not report the staged file: %+v", status.Changes)
	}

	if err := ws.DeleteFile(ctx, root, "notes.txt"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if res, xerr := client.Exec(ctx, "test -e "+aio.Quote(path.Join(root, "notes.txt"))+"; echo PF_EXISTS:$?", ""); xerr != nil {
		t.Fatalf("test -e: %v", xerr)
	} else if !strings.Contains(res.Output, "PF_EXISTS:1") {
		t.Fatalf("the file survived the delete: %s", res.Output)
	}
}
