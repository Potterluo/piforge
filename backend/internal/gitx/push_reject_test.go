package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FR-12.6: a push refused because the remote moved on is typed, so the PR
// dialog can say "pull first" instead of "failed".
func TestPushRejectedIsTyped(t *testing.T) {
	bare, seed := remoteFixture(t)
	work := filepath.Join(t.TempDir(), "work")
	gitIn(t, filepath.Dir(work), "clone", bare, work)

	if err := os.WriteFile(filepath.Join(work, "mine.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-m", "local work")

	// Somebody else pushes first.
	if err := os.WriteFile(filepath.Join(seed, "theirs.txt"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-m", "upstream")
	gitIn(t, seed, "push", "origin", "main")

	_, err := NewRunner().Push(context.Background(), work)
	if !errors.Is(err, ErrPushRejected) {
		t.Fatalf("push error = %v, want ErrPushRejected", err)
	}
	var pushErr *PushError
	if !errors.As(err, &pushErr) || strings.TrimSpace(pushErr.Stderr) == "" {
		t.Fatalf("the refusal lost git's own text: %v", err)
	}
	if !strings.Contains(err.Error(), "rejected") && !strings.Contains(err.Error(), "fetch first") {
		t.Fatalf("the sentence does not explain the rejection: %v", err)
	}
}

// The other refusal a person can act on: credentials. Git's wording is what a
// deployment actually produces (no terminal to prompt on, in a container).
func TestClassifyPushFailure(t *testing.T) {
	cases := []struct {
		stderr string
		want   error
	}{
		{"! [rejected]        main -> main (fetch first)", ErrPushRejected},
		{"! [rejected]        main -> main (non-fast-forward)", ErrPushRejected},
		{"fatal: could not read Username for 'https://github.com': terminal prompts disabled", ErrNoCredentials},
		{"remote: Invalid username or password.", ErrNoCredentials},
		{"git@github.com: Permission denied (publickey).", ErrNoCredentials},
		{"fatal: repository 'https://example/x.git/' not found", ErrNoCredentials},
		{"fatal: unable to access 'https://x/': Could not resolve host: x", nil},
	}
	for _, tc := range cases {
		got := classifyPushFailure(tc.stderr)
		if tc.want == nil {
			if got != nil {
				t.Errorf("%q classified as %v", tc.stderr, got)
			}
			continue
		}
		if !errors.Is(got, tc.want) {
			t.Errorf("%q classified as %v, want %v", tc.stderr, got, tc.want)
		}
	}
}

// A push error never prints the remote's URL twice or invents a cause: the
// typed kind plus git's last line is the whole message.
func TestPushErrorKeepsTheLastLine(t *testing.T) {
	err := &PushError{Kind: ErrPushRejected, Stderr: "remote: something\n! [rejected] main -> main (fetch first)\n"}
	if !errors.Is(err, ErrPushRejected) {
		t.Fatal("Unwrap lost the kind")
	}
	if !strings.Contains(err.Error(), "fetch first") {
		t.Fatalf("message = %q", err.Error())
	}
	if strings.Count(err.Error(), "\n") != 0 {
		t.Fatalf("message is not one line: %q", err.Error())
	}
}
