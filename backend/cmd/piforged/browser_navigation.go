package main

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// parsePortList parses the comma-separated loopback ports a host-placement
// browser may open (-browser-localhost-ports). An empty value is the safe
// default: no loopback navigation at all.
//
// It fails the boot rather than skipping a typo: an operator who wrote
// "3000, 30O1" believes 3001 is published, and silently dropping it would
// produce a panel that refuses a page with no explanation of why.
func parsePortList(raw string) ([]int, error) {
	var ports []int
	for _, field := range strings.Split(raw, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		n, err := strconv.Atoi(field)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("port %q is not a TCP port (1-65535)", field)
		}
		duplicate := false
		for _, seen := range ports {
			if seen == n {
				duplicate = true
				break
			}
		}
		if !duplicate {
			ports = append(ports, n)
		}
	}
	return ports, nil
}

// listenPort extracts the port of a listen address ("127.0.0.1:8080", ":8080").
// It is the control plane's own port, which the browser rule always refuses.
func listenPort(addr string) (int, bool) {
	_, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return 0, false
	}
	return n, true
}

// webUpstreamPort is the loopback port of the UI this deployment serves
// (-web-upstream, e.g. the image's 127.0.0.1:3100). It is a port the platform
// published, so a host-placement browser may render it without an operator
// flag; a non-loopback upstream is not this process's loopback and returns ok
// false.
func webUpstreamPort(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return 0, false
	}
	if !loopbackName(u.Hostname()) {
		return 0, false
	}
	if text := u.Port(); text != "" {
		n, perr := strconv.Atoi(text)
		if perr != nil || n < 1 || n > 65535 {
			return 0, false
		}
		return n, true
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return 443, true
	case "http", "":
		return 80, true
	}
	return 0, false
}

// loopbackName reports whether host is a spelling of this machine's loopback.
func loopbackName(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.IsLoopback()
	}
	return false
}
