//go:build !linux

package quotaenforcer

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
)

// The enforcer runs inside a Linux sandbox. These stubs exist so the binary that
// also builds for Windows (the development and e2e control plane) compiles, and
// so `piforged quota-enforcer` on another platform says why it cannot work
// instead of pretending the workspace is empty.

var errUnsupported = errors.New("quota-enforcer: this platform has no /proc, so it cannot find the writers in a workspace; it runs inside a Linux sandbox")

// Measure is not implemented off Linux.
func Measure(ctx context.Context, root string) (int64, error) { return 0, errUnsupported }

// MeasureRoot is not implemented off Linux: the enforcer measures the container's
// own root filesystem, which only exists where it runs.
func MeasureRoot(ctx context.Context, root string) (int64, error) { return 0, errUnsupported }

// ReadBaseline is safe everywhere: it is a number in a file, and 0 means "no
// baseline" (which turns the second term off) on every platform.
func ReadBaseline(path string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

// Writers is not implemented off Linux.
func Writers(roots ...string) ([]int, error) { return nil, errUnsupported }

func signalProcess(pid int, action Action) error { return errUnsupported }

func processAlive(pid int) bool { return false }

func isolate() {}
