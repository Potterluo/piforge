//go:build !linux

package quotaenforcer

import (
	"context"
	"errors"
)

// The enforcer runs inside a Linux sandbox. These stubs exist so the binary that
// also builds for Windows (the development and e2e control plane) compiles, and
// so `piforged quota-enforcer` on another platform says why it cannot work
// instead of pretending the workspace is empty.

var errUnsupported = errors.New("quota-enforcer: this platform has no /proc, so it cannot find the writers in a workspace; it runs inside a Linux sandbox")

// Measure is not implemented off Linux.
func Measure(ctx context.Context, root string) (int64, error) { return 0, errUnsupported }

// Writers is not implemented off Linux.
func Writers(root string) ([]int, error) { return nil, errUnsupported }

func signalProcess(pid int, action Action) error { return errUnsupported }

func processAlive(pid int) bool { return false }

func isolate() {}
