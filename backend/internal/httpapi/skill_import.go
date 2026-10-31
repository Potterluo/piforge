package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/diskquota"
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

	result := s.persistCandidates(ctx, user.ID, url, found, in.DryRun)
	writeJSON(w, http.StatusOK, map[string]any{"source": url, "dry_run": in.DryRun, "skills": result})
}

// importedSkill is one row of an import response.
type importedSkill struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Files   int    `json:"files"`
	Skipped bool   `json:"skipped"`
	Reason  string `json:"reason,omitempty"`
	// Enabled is the skill's state after this call. An import of a disabled
	// skill re-enables it (see persistCandidates), so this is the field the UI
	// must read instead of assuming "imported" means "in use".
	Enabled bool `json:"enabled"`
	// Reenabled says the skill was disabled before the import and is enabled
	// now, so the toast can name that instead of reporting a bare success.
	Reenabled bool `json:"reenabled,omitempty"`
}

// persistCandidates turns discovered directories into skills.
//
// A git import and a zip import differ only in where the directories came from, so
// they share this: the same replace-by-name rule, the same bundle writer, the same
// per-skill failure that leaves the others alone.
//
// Replacing a skill that is disabled re-enables it. The judgement call: importing
// a collection is an explicit "I want this skill available" — the alternative
// (replace the bytes, keep the row disabled) is what made the UI report success
// while the agent still could not see it, because mounting reads `enabled`. So the
// import turns it back on and says so in the response (and the UI toast), which is
// the only honest version of "the import succeeded".
func (s *server) persistCandidates(
	ctx context.Context,
	userID, source string,
	found []skillCandidate,
	dryRun bool,
) []importedSkill {
	result := make([]importedSkill, 0, len(found))
	for _, candidate := range found {
		item := importedSkill{Name: candidate.Name, Path: candidate.Rel, Files: len(candidate.Files)}
		// A skill with the same name is replaced rather than duplicated: importing the
		// same collection twice should not leave two entries fighting over the name.
		target := s.findSkillByName(ctx, userID, candidate.Name)
		if dryRun {
			// Report the state the import would leave behind, without touching
			// anything. A name with no row would be created enabled.
			item.Enabled = target == nil || target.Enabled
			result = append(result, item)
			continue
		}
		if target == nil {
			created, cerr := s.deps.Store.CreateSkill(ctx, userID, contracts.Skill{
				Name:        candidate.Name,
				Version:     "imported",
				Description: candidate.Description,
				ContentHash: "pending",
				SourceURL:   source,
				Enabled:     true,
			})
			if cerr != nil {
				item.Skipped, item.Reason = true, cerr.Error()
				result = append(result, item)
				continue
			}
			target = created
		} else {
			// Only a skill the caller may edit can be replaced. A shared skill is
			// another tenant's row as far as a normal user is concerned: writing its
			// bundle would edit what everybody sees, and the update would fail after
			// the files had already been replaced. Refuse before the write.
			editable, eerr := s.deps.Store.SkillEditable(ctx, userID, target.ID)
			if eerr != nil {
				item.Skipped, item.Reason = true, eerr.Error()
				result = append(result, item)
				continue
			}
			if !editable {
				item.Skipped = true
				item.Reason = "同名共享 Skill 由管理员维护，导入不会覆盖它"
				item.Enabled = target.Enabled
				result = append(result, item)
				continue
			}
		}

		bundleKey, hash, werr := s.writeSkillBundle(target.ID, candidate.Files)
		if werr != nil {
			item.Skipped, item.Reason = true, werr.Error()
			result = append(result, item)
			continue
		}
		if _, uerr := s.deps.Store.UpdateSkillBundle(ctx, userID, target.ID, bundleKey, hash); uerr != nil {
			item.Skipped, item.Reason = true, uerr.Error()
			result = append(result, item)
			continue
		}
		// The content is in place; now make sure it can actually be mounted. A
		// failure here is reported rather than swallowed: a disabled row with new
		// content is exactly the state that lied before.
		if !target.Enabled {
			if _, serr := s.deps.Store.SetSkillEnabled(ctx, userID, target.ID, true); serr != nil {
				item.Skipped = true
				item.Reason = "内容已替换，但重新启用失败：" + serr.Error()
				item.Enabled = false
				result = append(result, item)
				continue
			}
			item.Reenabled = true
		}
		item.Enabled = true
		result = append(result, item)
	}
	return result
}

// Limits on a zip upload. A skill collection is text; these caps are the same
// 4 MB the repository walk enforces, plus a request bound so an oversized body is
// rejected before it is buffered.
const (
	skillZipMaxBytes   = 8 << 20 // the upload itself
	skillZipMaxEntries = 256     // a zip is a bookkeeping format, not a filesystem
)

// handleImportSkillsZip installs skills from an uploaded zip (FR-14.4).
//
// A curated collection usually arrives as an archive, not a repository: requiring
// a git remote to ship three Markdown files is the friction the PRD calls out. The
// archive is unpacked into a temporary directory and then treated exactly like a
// checkout — the same walk finds every directory with a SKILL.md, and only that
// directory's files are taken.
func (s *server) handleImportSkillsZip(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, skillZipMaxBytes)
	if err := r.ParseMultipartForm(skillZipMaxBytes); err != nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument,
			"需要一个 zip 文件（multipart/form-data 的 file 字段）"))
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument,
			"需要一个 zip 文件（multipart/form-data 的 file 字段）"))
		return
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, skillZipMaxBytes+1))
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "读取上传文件失败：%v", err))
		return
	}
	if len(data) > skillZipMaxBytes {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "zip 不能超过 8 MB"))
		return
	}

	dir, err := os.MkdirTemp("", "piforge-skills-zip-")
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInternal, "无法创建临时目录：%v", err))
		return
	}
	defer os.RemoveAll(dir)

	if err := extractSkillZipWithin(data, dir, s.skillMaxBytes()); err != nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "%s", err.Error()))
		return
	}

	found, err := findSkills(dir, skillImportMaxBytes)
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "%v", err))
		return
	}
	if len(found) == 0 {
		s.fail(w, r, apiErr(contracts.ErrNotFound, "这个 zip 里没有 SKILL.md"))
		return
	}

	source := "zip:" + strings.TrimSpace(header.Filename)
	result := s.persistCandidates(r.Context(), user.ID, source, found, false)
	writeJSON(w, http.StatusOK, map[string]any{"source": source, "dry_run": false, "skills": result})
}

// extractSkillZip unpacks an archive under dst, refusing anything that could write
// outside it.
//
// This is the same class of boundary as bundleFiles: entry names are attacker
// input, and `../` in a zip has been a real vulnerability in real products. The
// archive is rejected as a whole rather than skipping the bad entry, because a
// half-imported collection is harder to reason about than a refusal.
func extractSkillZip(data []byte, dst string) error {
	return extractSkillZipWithin(data, dst, DefaultSkillMaxBytes)
}

// extractSkillZipWithin is extractSkillZip with an explicit total-expansion cap.
//
// The cap is the archive-bomb guard (FR-5.8). A zip's header sizes are attacker
// input too: an entry can declare 1 KiB and stream a gigabyte, so the limit is
// enforced against the bytes actually read and written as well as against the
// declared total. The cap is a configuration (-skill-max-bytes /
// PIFORGE_SKILL_MAX_BYTES) because "how big may a curated collection be" is a
// deployment's judgement, not a constant.
//
// Nothing is left behind on refusal: the caller removes the staging directory it
// passed in, and no skill row is written until this returns nil.
func extractSkillZipWithin(data []byte, dst string, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = DefaultSkillMaxBytes
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("不是有效的 zip：%v", err)
	}
	if len(reader.File) > skillZipMaxEntries {
		return fmt.Errorf("zip 里的文件过多（上限 %d）", skillZipMaxEntries)
	}

	// Refuse on the declared total first: a bomb with honest headers never has to
	// be expanded to be refused.
	var declared int64
	for _, entry := range reader.File {
		declared += int64(entry.UncompressedSize64)
	}
	if declared > maxBytes {
		return fmt.Errorf(
			"技能包展开后超过上限（%s）：压缩包声明解压后为 %s，没有导入任何文件。请拆分技能，或请管理员调整 -skill-max-bytes / PIFORGE_SKILL_MAX_BYTES",
			diskquota.HumanBytes(maxBytes), diskquota.HumanBytes(declared))
	}

	total := int64(0)
	for _, entry := range reader.File {
		name := strings.ReplaceAll(entry.Name, "\\", "/")
		if entry.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			continue
		}
		if !entry.Mode().IsRegular() {
			return fmt.Errorf("zip 里的 %q 不是普通文件", name)
		}
		if strings.HasPrefix(name, "/") {
			return fmt.Errorf("path %q is not allowed", name)
		}
		for _, segment := range strings.Split(name, "/") {
			if segment == ".." {
				return fmt.Errorf("path %q is not allowed", name)
			}
		}
		rel := strings.TrimPrefix(path.Clean("/"+name), "/")
		if rel == "" || rel == "." {
			return fmt.Errorf("path %q is not allowed", name)
		}
		if len(rel) > maxSkillPathLen {
			return fmt.Errorf("path %q is too long", rel)
		}
		if entry.UncompressedSize64 > maxSkillFileBytes {
			return fmt.Errorf("%s is larger than %d bytes", rel, maxSkillFileBytes)
		}

		opened, oerr := entry.Open()
		if oerr != nil {
			return fmt.Errorf("读取 %s 失败：%v", rel, oerr)
		}
		body, rerr := io.ReadAll(io.LimitReader(opened, maxSkillFileBytes+1))
		closeErr := opened.Close()
		if rerr != nil {
			return fmt.Errorf("读取 %s 失败：%v", rel, rerr)
		}
		if closeErr != nil {
			return fmt.Errorf("读取 %s 失败：%v", rel, closeErr)
		}
		if len(body) > maxSkillFileBytes {
			return fmt.Errorf("%s is larger than %d bytes", rel, maxSkillFileBytes)
		}
		// The declared sizes are not trusted: this is the number that decides, and
		// it is checked before the bytes are written.
		total += int64(len(body))
		if total > maxBytes {
			return fmt.Errorf(
				"技能包展开后超过上限（%s）：已展开 %s 时停止，没有导入任何文件。请拆分技能，或请管理员调整 -skill-max-bytes / PIFORGE_SKILL_MAX_BYTES",
				diskquota.HumanBytes(maxBytes), diskquota.HumanBytes(total))
		}

		target := filepath.Join(dst, filepath.FromSlash(rel))
		// Belt and braces after the segment check: the final path must still be
		// inside the directory this function owns.
		if inside, ierr := filepath.Rel(dst, target); ierr != nil || inside == ".." ||
			strings.HasPrefix(inside, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("path %q is not allowed", name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("解压 %s 失败：%v", rel, err)
		}
		if err := os.WriteFile(target, body, 0o644); err != nil {
			return fmt.Errorf("解压 %s 失败：%v", rel, err)
		}
	}
	return nil
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
