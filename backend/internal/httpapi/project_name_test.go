package httpapi

import (
	"testing"
	"time"
)

func TestRepoBasename(t *testing.T) {
	cases := map[string]string{
		"https://github.com/octocat/Spoon-Knife.git":  "Spoon-Knife",
		"https://github.com/octocat/Spoon-Knife":      "Spoon-Knife",
		"https://github.com/octocat/Spoon-Knife.git/": "Spoon-Knife",
		"https://github.com/x/y.git?ref=main":         "y",
		"git@github.com:x/y.git":                      "y",
		"git@github.com:single.git":                   "single",
		`C:\repos\cache-service`:                      "cache-service",
		"/repos/cache-service.git":                    "cache-service",
		"":                                            "",
		"   ":                                         "",
	}
	for input, want := range cases {
		if got := repoBasename(input); got != want {
			t.Errorf("repoBasename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDeriveProjectName(t *testing.T) {
	now := time.Date(2026, 10, 29, 21, 0, 0, 0, time.Local)
	if got := deriveProjectName("https://github.com/octocat/Spoon-Knife.git", now); got != "Spoon-Knife" {
		t.Errorf("derived name = %q, want Spoon-Knife", got)
	}
	if got := deriveProjectName("git@github.com:x/y.git", now); got != "y" {
		t.Errorf("derived name = %q, want y", got)
	}
	if got := deriveProjectName("", now); got != "空白工作区 · 10-29" {
		t.Errorf("blank workspace name = %q, want 空白工作区 · 10-29", got)
	}
}
