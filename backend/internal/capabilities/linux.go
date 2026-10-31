//go:build linux

package capabilities

import (
	"os"
	"strconv"
	"strings"
)

// Capability numbers in the kernel's ABI. They are not exported by x/sys as
// plain integers, and spelling them here keeps the /proc parser readable.
const (
	capDACOverride = 1
	capSysAdmin    = 21
)

// effective reports whether the calling thread's effective capability set
// contains n.
//
// The effective set is read rather than a probe operation being attempted: a
// probe mount would need a target directory, would race provisioning, and would
// have to interpret errno. /proc/self/status is the kernel's own answer.
func effective(n int) bool {
	bits, ok := capEff()
	if !ok {
		return false
	}
	return bits&(1<<uint(n)) != 0
}

func capEff() (uint64, bool) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "CapEff:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return 0, false
		}
		bits, perr := strconv.ParseUint(fields[1], 16, 64)
		if perr != nil {
			return 0, false
		}
		return bits, true
	}
	return 0, false
}

func hasSysAdmin() bool { return effective(capSysAdmin) }

func dacOverrideEffective() bool { return effective(capDACOverride) }
