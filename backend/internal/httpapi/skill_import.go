package httpapi

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
)

// Importing skills from a repository (PRD FR-14.x).
//
// Writing a SKILL.md in a textarea is fine for one skill; bringing in a curated
// collection meant copying files by hand. This clones a repository, finds every
// directory that contains a SKILL.md, and stores each as its own skill.
//
// Only the files under a skill's own directory are taken. A repository is somebody
// else's code: importing the whole tree would put scripts the skill does not reference
// into a place Pi loads from.

// skillImportMaxBytes caps how much a single import may pull in, so a mistake (a repo of
// binaries) cannot fill the disk.
const skillImportMaxBytes = 4 << 20

// handleImportSkills clones a git URL and imports the skills it contains.
func (s *server) handleImportSkills(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	var in struct {
		URL    string `json:"url"`
		Ref    string `json:"ref"`
		DryRun bool   `json:"dry_run"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	url := strings.TrimSpace(in.URL)
	if url == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "url is required"))
		return
	}
	// Only transports a clone can use here. `file://` is allowed because a self-hosted
	// install may keep its skills on the same disk.
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") &&
		!strings.HasPrefix(url, "file://") && !strings.HasPrefix(url, "git@") {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument,
			"只支持 https:// 、http:// 、file:// 或 git@ 开头的仓库地址"))
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 120*time.Second)
	defer cancel()

	dir, err := os.MkdirTemp("", "piforge-skills-")
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInternal, "无法创建临时目录：%v", err))
		return
	}
	defer os.RemoveAll(dir)

	args := []string{"clone", "--depth", "1"}
	if strings.TrimSpace(in.Ref) != "" {
		args = append(args, "--branch", strings.TrimSpace(in.Ref))
	}
	args = append(args, url, dir)
	cmd := exec.CommandContext(ctx, "git", args...)
	if out, cerr := cmd.CombinedOutput(); cerr != nil {
		detail := strings.TrimSpace(string(out))
		if len(detail) > 400 {
			detail = detail[len(detail)-400:]
		}
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "克隆失败：%s", detail))
		return
	}

	found, err := findSkills(dir, skillImportMaxBytes)
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "%v", err))
		return
	}
	if len(found) == 0 {
		s.fail(w, r, apiErr(contracts.ErrNotFound, "这个仓库里没有 SKILL.md"))
		return
	}

	type imported struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Files   int    `json:"files"`
		Skipped bool   `json:"skipped"`
		Reason  string `json:"reason,omitempty"`
	}
	result := make([]imported, 0, len(found))

	for _, candidate := range found {
		item := imported{Name: candidate.Name, Path: candidate.Rel, Files: len(candidate.Files)}
		if in.DryRun {
			result = append(result, item)
			continue
		}
		// A skill with the same name is replaced rather than duplicated: importing the
		// same repository twice should not leave two entries fighting over the name.
		existing := s.findSkillByName(ctx, user.ID, candidate.Name)
		var target *contracts.Skill
		if existing != nil {
			target = existing
		} else {
			created, cerr := s.deps.Store.CreateSkill(ctx, user.ID, contracts.Skill{
				Name:        candidate.Name,
				Version:     "imported",
				Description: candidate.Description,
				ContentHash: "pending",
				SourceURL:   url,
				Enabled:     true,
			})
			if cerr != nil {
				item.Skipped, item.Reason = true, cerr.Error()
				result = append(result, item)
				continue
			}
			target = created
		}

		bundleKey, hash, werr := s.writeSkillBundle(target.ID, candidate.Files)
		if werr != nil {
			item.Skipped, item.Reason = true, werr.Error()
			result = append(result, item)
			continue
		}
		if _, uerr := s.deps.Store.UpdateSkillBundle(ctx, user.ID, target.ID, bundleKey, hash); uerr != nil {
			item.Skipped, item.Reason = true, uerr.Error()
			result = append(result, item)
			continue
		}
		result = append(result, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{"source": url, "dry_run": in.DryRun, "skills": result})
}

// skillCandidate is one directory that holds a SKILL.md.
type skillCandidate struct {
	Name        string
	Description string
	Rel         string
	Files       map[string]string
}

// findSkills walks a checkout and collects every directory with a SKILL.md, keeping only
// the files under that directory.
func findSkills(root string, maxBytes int) ([]skillCandidate, error) {
	var out []skillCandidate
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// Never descend into git's own storage.
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "SKILL.md" {
			return nil
		}
		dir := filepath.Dir(path)
		rel, rerr := filepath.Rel(root, dir)
		if rerr != nil {
			return rerr
		}
		files := map[string]string{}
		size := 0
		werr := filepath.WalkDir(dir, func(inner string, ie os.DirEntry, ierr error) error {
			if ierr != nil {
				return ierr
			}
			if ie.IsDir() {
				if ie.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			relInner, rerr := filepath.Rel(dir, inner)
			if rerr != nil {
				return rerr
			}
			body, berr := os.ReadFile(inner)
			if berr != nil {
				return berr
			}
			size += len(body)
			if size > maxBytes {
				return errImportTooLarge
			}
			files[filepath.ToSlash(relInner)] = string(body)
			return nil
		})
		if werr != nil {
			return werr
		}
		name := slugSkill(filepath.Base(dir))
		if rel == "." {
			// A repository that *is* one skill: use the repository's own name.
			name = slugSkill(filepath.Base(root))
		}
		out = append(out, skillCandidate{
			Name:        name,
			Description: describeSkill(files["SKILL.md"]),
			Rel:         filepath.ToSlash(rel),
			Files:       files,
		})
		return filepath.SkipDir
	})
	return out, err
}

var errImportTooLarge = &importError{"这个技能超过了单次导入上限（4 MB）"}

type importError struct{ msg string }

func (e *importError) Error() string { return e.msg }

// slugSkill turns a directory name into the identifier the rest of the system uses.
func slugSkill(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	dash := false
	for _, r := range lower {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == '-' || r == '_' || r == ' ' || r == '.':
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "skill"
	}
	return out
}

// describeSkill reads the first real prose line of a SKILL.md as its description.
//
// Front matter is skipped as a block, not line by line: `name: PDF` between the dashes is
// metadata, and reporting it as the description is how the first version of this
// described every skill as "name: …".
func describeSkill(md string) string {
	inFrontMatter := false
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			inFrontMatter = !inFrontMatter
			continue
		}
		if inFrontMatter {
			continue
		}
		line = trimmed
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) > 200 {
			line = line[:200]
		}
		return line
	}
	return ""
}

// findSkillByName looks a skill up by name within the caller's own skills.
func (s *server) findSkillByName(ctx context.Context, userID, name string) *contracts.Skill {
	list, err := s.deps.Store.ListSkills(ctx, userID)
	if err != nil {
		return nil
	}
	for _, skill := range list {
		if skill.Name == name {
			return skill
		}
	}
	return nil
}
