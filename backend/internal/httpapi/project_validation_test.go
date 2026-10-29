package httpapi

import "testing"

// A repository address is either supported or it is not. The two accepted forms
// are what the product documents (https and git's scp syntax); everything else
// must be refused before a project exists, because a project with an unclonable
// address produces sessions with no worktree.
func TestValidateRepoURLAcceptsOnlySupportedForms(t *testing.T) {
	accepted := []string{
		"",
		"   ",
		"https://github.com/acme/cache-service.git",
		"https://git.example.com/team/repo",
		"git@github.com:acme/cache-service.git",
		"git@git.example.com:team/repo.git",
		// The local sandbox provider clones a bare repository by absolute path;
		// the git push/pull tests depend on it.
		t.TempDir(),
	}
	for _, value := range accepted {
		if err := validateRepoURL(value); err != nil {
			t.Errorf("validateRepoURL(%q) = %v, want nil", value, err)
		}
	}

	refused := []string{
		"not-a-repo-url",
		"http://github.com/acme/repo.git",
		"ftp://github.com/acme/repo.git",
		"https://",
		"git@github.com",
		"git@github.com:",
		"https://github.com/acme/repo with space.git",
	}
	for _, value := range refused {
		err := validateRepoURL(value)
		if err == nil {
			t.Errorf("validateRepoURL(%q) = nil, want a rejection", value)
			continue
		}
		// The UI maps this message, but the wire error still has to identify the
		// field so a script can tell what was wrong.
		if got := err.Error(); got == "" {
			t.Errorf("validateRepoURL(%q) returned an empty message", value)
		}
	}
}
