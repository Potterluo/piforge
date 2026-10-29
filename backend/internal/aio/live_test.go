package aio

import (
	"context"
	"os"
	"testing"
)

// Optional live check against a running AIO sandbox. It is skipped unless the endpoint is
// exported, because the image is 2 GB and not every machine will have it.
//
//	PIFORGE_AIO_URL=http://127.0.0.1:18080 PIFORGE_AIO_KEY=... go test ./internal/aio
func TestLiveSandbox(t *testing.T) {
	base := os.Getenv("PIFORGE_AIO_URL")
	if base == "" {
		t.Skip("PIFORGE_AIO_URL is not set")
	}
	client, err := New(Config{BaseURL: base, APIKey: os.Getenv("PIFORGE_AIO_KEY")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	info, err := client.BrowserInfo(ctx)
	if err != nil {
		t.Fatalf("BrowserInfo: %v", err)
	}
	if info.CDPURL == "" {
		t.Fatal("the sandbox reported no CDP url")
	}
	t.Logf("cdp: %s", info.CDPURL)

	result, err := client.Exec(ctx, "echo live-ok", "")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code %d: %s", result.ExitCode, result.Output)
	}
	if result.Output == "" {
		t.Fatal("no output")
	}
	t.Logf("shell said: %q", result.Output)

	if err := client.WriteFile(ctx, "/home/gem/piforge-live.txt", "from the control plane\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	file, err := client.ReadFile(ctx, "/home/gem/piforge-live.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if file.Content != "from the control plane\n" {
		t.Fatalf("read back %q", file.Content)
	}

	shot, err := client.Screenshot(ctx)
	if err != nil {
		t.Fatalf("Screenshot: %v (the browser may still be starting)", err)
	}
	t.Logf("screenshot: %d bytes", len(shot))
}
