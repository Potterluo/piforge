package main

import (
	"flag"
	"testing"
)

// parseLimit drives the exact pair the daemon uses: defineParallelLimit registers
// -parallel-limit and resolveParallelLimit applies flag-over-env precedence plus
// validation. A test that re-implemented either half would only prove the test.
func parseLimit(t *testing.T, args []string) (int, error) {
	t.Helper()
	fs := flag.NewFlagSet("piforged-test", flag.ContinueOnError)
	var value int
	defineParallelLimit(fs, &value)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return resolveParallelLimit(fs, &value)
}

// FR-3.3: the limit is real configuration. The default is 3, the documented range
// is 1–10, and a 0 or an absurd value fails startup instead of silently falling
// back — the old code accepted any number but never read one.
func TestParallelLimitFromFlagAndEnv(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		env     string
		want    int
		wantErr bool
	}{
		{name: "default is 3", want: 3},
		{name: "flag 5", args: []string{"-parallel-limit=5"}, want: 5},
		{name: "flag 10 is the ceiling and valid", args: []string{"-parallel-limit=10"}, want: 10},
		{name: "flag 0 is an error", args: []string{"-parallel-limit=0"}, wantErr: true},
		{name: "flag 11 is an error", args: []string{"-parallel-limit=11"}, wantErr: true},
		{name: "env 5", env: "5", want: 5},
		{name: "flag wins over env", args: []string{"-parallel-limit=4"}, env: "7", want: 4},
		{name: "env 0 is an error", env: "0", wantErr: true},
		{name: "env 11 is an error", env: "11", wantErr: true},
		{name: "env junk is an error", env: "three", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PIFORGE_PARALLEL_LIMIT", tc.env)
			got, err := parseLimit(t, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected a startup error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("limit = %d, want %d", got, tc.want)
			}
		})
	}
}
