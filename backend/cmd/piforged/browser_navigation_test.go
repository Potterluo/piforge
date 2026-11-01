package main

import "testing"

// G5 of docs/DATA-FLOW-AUDIT.md: the host-placement browser may open a loopback
// page only on a port the deployment published. Two sources feed that set — the
// operator's -browser-localhost-ports and the UI this process serves — and the
// control plane's own listener is always refused. These are the small parsers
// that decide it.
func TestParsePortList(t *testing.T) {
	cases := []struct {
		raw   string
		want  []int
		fails bool
	}{
		{"", nil, false},
		{"3000", []int{3000}, false},
		{" 3000 , 3100 ", []int{3000, 3100}, false},
		{"3000,3000", []int{3000}, false},
		{"3000,,", []int{3000}, false},
		{"3000, 30O1", nil, true},
		{"0", nil, true},
		{"65536", nil, true},
		{"-1", nil, true},
	}
	for _, tc := range cases {
		got, err := parsePortList(tc.raw)
		if tc.fails {
			if err == nil {
				t.Fatalf("parsePortList(%q) = %v, want an error", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parsePortList(%q): %v", tc.raw, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("parsePortList(%q) = %v, want %v", tc.raw, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("parsePortList(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		}
	}
}

func TestListenPort(t *testing.T) {
	cases := map[string]int{
		"127.0.0.1:8080": 8080,
		":7700":          7700,
		"0.0.0.0:80":     80,
		"[::1]:9000":     9000,
	}
	for addr, want := range cases {
		got, ok := listenPort(addr)
		if !ok || got != want {
			t.Fatalf("listenPort(%q) = %d,%v want %d,true", addr, got, ok, want)
		}
	}
	for _, addr := range []string{"", "8080", "127.0.0.1", "127.0.0.1:notaport", "127.0.0.1:0"} {
		if got, ok := listenPort(addr); ok {
			t.Fatalf("listenPort(%q) = %d, want not ok", addr, got)
		}
	}
}

func TestWebUpstreamPort(t *testing.T) {
	allowed := map[string]int{
		"http://127.0.0.1:3100":        3100,
		"http://localhost:3000/":       3000,
		"http://[::1]:8081":            8081,
		"http://127.0.0.1":             80,
		"https://127.0.0.1":            443,
		"https://127.0.0.1:8443/login": 8443,
	}
	for raw, want := range allowed {
		got, ok := webUpstreamPort(raw)
		if !ok || got != want {
			t.Fatalf("webUpstreamPort(%q) = %d,%v want %d,true", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "127.0.0.1:3100", "http://example.com:3100", "http://10.0.0.5:3000"} {
		if got, ok := webUpstreamPort(raw); ok {
			t.Fatalf("webUpstreamPort(%q) = %d, want not ok (not this process's loopback)", raw, got)
		}
	}
}
