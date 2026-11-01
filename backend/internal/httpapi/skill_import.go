package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/diskquota"
	"github.com/piforge/piforge/backend/internal/tenantfs"
	"github.com/piforge/piforge/backend/internal/urlguard"
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

// DefaultSkillCloneMaxBytes bounds the *checkout* a clone may write, measured
// while git runs and again when it exits.
//
// Why a second cap: the 4 MB cap above is enforced by the walk, after the clone
// has already written the whole tree (adversarial acceptance #4, attack 10: a
// 32 MiB repository left 38 829 857 B in /tmp and a 268 MiB one left
// 268 800 878 B, all root-owned on the same overlay as /data). "The read is
// capped" was true and useless: the disk had already been spent. This cap is
// checked by a watchdog that measures the destination every 25 ms and kills git
// when it crosses the line, and once more after git exits; either way the
// directory is deleted and the import is refused with a 400 that names the cap
// and the bytes measured. It is a configuration (-skill-clone-max-bytes /
// PIFORGE_SKILL_CLONE_MAX_BYTES) because "how big a repository may this
// deployment fetch" is an operator's judgement.
const DefaultSkillCloneMaxBytes = 16 << 20

// clonePollInterval is how often the watchdog measures the checkout. It is the
// overshoot window: git may write for at most this long after the cap is
// crossed, which is why the measured worst case (see docs) is "cap + one
// interval of writes", not "cap".
const clonePollInterval = 25 * time.Millisecond

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

	// A clone over http(s) is this process fetching a tenant URL from its own
	// network position, which is exactly what the report measured through the
	// importer's git (`http://127.0.0.1:7700/healthz`,
	// `http://169.254.169.254/...`). The policy refuses those before git runs.
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "git@") {
		if err := s.refuseOutboundURL(r.Context(), gitURLForPolicy(url), "skill repository URL"); err != nil {
			s.fail(w, r, err)
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 120*time.Second)
	defer cancel()

	dir, err := os.MkdirTemp("", "piforge-skills-")
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInternal, "无法创建临时目录：%v", err))
		return
	}
	defer os.RemoveAll(dir)

	// The clone is confined twice: git is told not to create links (below), and
	// the repository's own index is refused if it declares one. See
	// cloneSkillRepo and refuseDeclaredSymlinks.
	if cerr := cloneSkillRepo(ctx, url, in.Ref, dir, s.skillCloneMaxBytes(), s.outboundPolicy()); cerr != nil {
		s.fail(w, r, cerr)
		return
	}

	found, err := findSkills(dir, skillImportMaxBytes)
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "%s", importRefusalMessage(err)))
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
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "%s", importRefusalMessage(err)))
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

// cloneSkillRepo clones a tenant-supplied URL into dir under the two rules a
// repository from a tenant has to obey.
//
// First, git is told not to create symbolic links (`core.symlinks=false`), so a
// link in the repository is checked out as the regular text file it would have
// pointed at. That removes the link from the control plane's disk. Second, the
// checkout's index is read: a repository that declares a link is refused
// outright, naming the path, because a tenant who ships one is trying to have
// this process read a file outside the checkout — materialising it quietly would
// import a skill whose "key" file now contains "/proc/1/environ" as text, which
// is a confusing success where a refusal is the honest answer.
//
// Depth stays at 1: the import only reads what a skill directory contains, and a
// tenant's repository should not be able to make this process fetch its history.
func cloneSkillRepo(ctx context.Context, url, ref, dir string, maxBytes int64, policy *urlguard.Policy) error {
	if cerr := cloneCheckout(ctx, url, ref, dir, maxBytes, policy); cerr != nil {
		return cerr
	}
	if rerr := refuseDeclaredSymlinks(ctx, dir); rerr != nil {
		return apiErr(contracts.ErrInvalidArgument, "%s", importRefusalMessage(rerr))
	}
	return nil
}

// cloneCheckout is the clone half on its own: git, with links materialised as
// text files instead of created. It is separate from cloneSkillRepo so the flag
// that makes a link harmless can be tested without the index check that refuses
// the repository anyway.
func cloneCheckout(ctx context.Context, url, ref, dir string, maxBytes int64, policy *urlguard.Policy) error {
	if maxBytes <= 0 {
		maxBytes = DefaultSkillCloneMaxBytes
	}
	// --depth 1 --single-branch --no-tags: the importer reads one commit's tree,
	// so a tenant's repository must not be able to make this process fetch its
	// history or its other branches. (Before this, --depth 1 already limited the
	// commit graph but every branch tip and tag was still advertised and, for a
	// ref named explicitly, fetched.)
	args := []string{"-c", "core.symlinks=false", "clone", "--depth", "1", "--single-branch", "--no-tags"}
	if strings.TrimSpace(ref) != "" {
		args = append(args, "--branch", strings.TrimSpace(ref))
	}
	// A name the policy verified is pinned to the address it verified, which
	// closes the window between the check above and git's own DNS lookup.
	args = append(args, gitResolveArgs(ctx, policy, url)...)
	args = append(args, url, dir)

	cmd := exec.CommandContext(ctx, "git", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if cerr := cmd.Start(); cerr != nil {
		return apiErr(contracts.ErrInternal, "无法启动 git：%v", cerr)
	}

	// The watchdog: measure the checkout while it is being written, and stop git
	// the moment it is over the cap. Without this the cap is a post-mortem: the
	// report's 268 MiB clone is 268 MiB on disk before anything reads it.
	var (
		peak   atomic.Int64
		killed atomic.Bool
		stop   = make(chan struct{})
		done   = make(chan struct{})
	)
	go func() {
		defer close(done)
		ticker := time.NewTicker(clonePollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				size, merr := treeBytes(dir)
				if merr != nil {
					continue
				}
				if size > peak.Load() {
					peak.Store(size)
				}
				if size > maxBytes {
					peak.Store(size)
					killed.Store(true)
					_ = cmd.Process.Kill()
					return
				}
			}
		}
	}()

	waitErr := cmd.Wait()
	close(stop)
	<-done

	measured, merr := treeBytes(dir)
	if merr == nil && measured > peak.Load() {
		peak.Store(measured)
	}
	if killed.Load() || (merr == nil && measured > maxBytes) {
		_ = os.RemoveAll(dir)
		return apiErr(contracts.ErrInvalidArgument,
			"仓库克隆超过了单次导入的克隆上限（%s）：已写入 %s 时停止并删除，没有导入任何 Skill。"+
				"请换一个更小的仓库，或请管理员调整 -skill-clone-max-bytes / PIFORGE_SKILL_CLONE_MAX_BYTES",
			diskquota.HumanBytes(maxBytes), diskquota.HumanBytes(peak.Load()))
	}
	if waitErr != nil {
		detail := strings.TrimSpace(out.String())
		if len(detail) > 400 {
			detail = detail[len(detail)-400:]
		}
		return apiErr(contracts.ErrInvalidArgument, "克隆失败：%s", detail)
	}
	return nil
}

// treeBytes measures everything under dir, including .git: the pack and the
// checkout are both disk this process spent, and the report counted both.
func treeBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			// A file git renamed under us is not a measurement failure.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, ierr := entry.Info()
		if ierr != nil {
			if os.IsNotExist(ierr) {
				return nil
			}
			return ierr
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// gitURLForPolicy turns a scp-style `git@host:path` address into the https URL
// the policy understands, so the same host rules apply to both shapes.
func gitURLForPolicy(raw string) string {
	if !strings.HasPrefix(raw, "git@") {
		return raw
	}
	rest := strings.TrimPrefix(raw, "git@")
	host, path, ok := strings.Cut(rest, ":")
	if !ok {
		return raw
	}
	// The transport differs (ssh, not https) but the question is the same one —
	// where does this host live — so it is checked as an https URL of it.
	return "https://" + host + "/" + strings.TrimPrefix(path, "/")
}

// gitResolveArgs pins the name git is about to fetch to the address the policy
// verified, via Git's own CURLOPT_RESOLVE hook. An empty result means the
// policy had nothing to pin (or git is too old to know the key, which the
// pre-check above still covers).
func gitResolveArgs(ctx context.Context, policy *urlguard.Policy, raw string) []string {
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return nil
	}
	if policy == nil {
		policy = urlguard.Default()
	}
	target, err := policy.Check(ctx, raw)
	if err != nil || len(target.Addrs) == 0 {
		return nil
	}
	parts := make([]string, 0, len(target.Addrs))
	for _, addr := range target.Addrs {
		parts = append(parts, fmt.Sprintf("%s:%s:%s", target.Host, target.Port, addr.String()))
	}
	return []string{"-c", "http.curloptResolve=" + strings.Join(parts, ",")}
}

// refuseDeclaredSymlinks reads the index a clone just wrote and refuses the
// import if any entry is declared as a symbolic link (git mode 120000).
//
// An index that cannot be read is not a refusal: the walk over the checkout
// (findSkills) is still the fence, and failing here would refuse an import that
// may be perfectly ordinary.
func refuseDeclaredSymlinks(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "-s", "-z")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	for _, entry := range strings.Split(string(out), "\x00") {
		tab := strings.IndexByte(entry, '\t')
		if tab < 0 {
			continue
		}
		meta := strings.Fields(entry[:tab])
		if len(meta) == 0 || meta[0] != "120000" {
			continue
		}
		return &tenantfs.Refusal{
			Path:   entry[tab+1:],
			Kind:   tenantfs.KindSymlink,
			Detail: "the repository declares it as a symbolic link (git mode 120000)",
		}
	}
	return nil
}

// findSkills walks a checkout and collects every directory with a SKILL.md, keeping only
// the files under that directory.
//
// The walk is confined (internal/tenantfs): the checkout is a tree a tenant
// controls, and a path in it is read only when every component is a real
// directory or a real file inside the checkout. A link anywhere in the tree
// refuses the whole import — the whole repository, not the one file — because a
// symlink in a repository is an attempt to read the control plane's own disk:
// `leakskill/key.txt -> /proc/1/environ` was a real, measured attack
// (e2e/.artifacts/adversarial3/REPORT.md, attack 3). The refusal is returned
// before a single skill row or bundle is written.
func findSkills(root string, maxBytes int) ([]skillCandidate, error) {
	tree, err := tenantfs.Open(root)
	if err != nil {
		return nil, err
	}

	// One pass over the whole tree. It does two jobs: it finds the skill
	// directories, and it is the fence — a link in a directory nobody would have
	// read still refuses the import, because the scan visits everything.
	var dirs []string
	scanErr := tree.Walk("", func(rel string, info os.FileInfo) error {
		if info.Name() != "SKILL.md" {
			return nil
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		if dir == "." {
			dir = ""
		}
		dirs = append(dirs, dir)
		return nil
	})
	if scanErr != nil {
		return nil, scanErr
	}

	// A skill inside another skill is part of the outer one, exactly as the
	// directory walk's SkipDir made it before: the outer bundle carries the inner
	// directory's files, so importing it twice would only create two rows fighting
	// over the same content.
	sort.Slice(dirs, func(i, j int) bool {
		di, dj := strings.Count(dirs[i], "/"), strings.Count(dirs[j], "/")
		if dirs[i] == "" {
			di = -1
		}
		if dirs[j] == "" {
			dj = -1
		}
		if di != dj {
			return di < dj
		}
		return dirs[i] < dirs[j]
	})
	kept := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		nested := false
		for _, parent := range kept {
			if dir == parent || parent == "" || strings.HasPrefix(dir, parent+"/") {
				nested = true
				break
			}
		}
		if !nested {
			kept = append(kept, dir)
		}
	}

	out := make([]skillCandidate, 0, len(kept))
	for _, dir := range kept {
		files := map[string]string{}
		size := 0
		werr := tree.WalkFiles(dir, func(rel string, info os.FileInfo) error {
			inner := rel
			if dir != "" {
				var rerr error
				inner, rerr = filepath.Rel(dir, rel)
				if rerr != nil {
					return rerr
				}
			}
			// Checked before the read so a huge file is refused rather than
			// buffered.
			if info.Size() > int64(maxBytes) {
				return errImportTooLarge
			}
			body, berr := tree.ReadFile(rel, int64(maxBytes))
			if berr != nil {
				return berr
			}
			size += len(body)
			if size > maxBytes {
				return errImportTooLarge
			}
			files[filepath.ToSlash(inner)] = string(body)
			return nil
		})
		if werr != nil {
			return nil, werr
		}
		name := slugSkill(filepath.Base(filepath.FromSlash(dir)))
		if dir == "" {
			// A repository that *is* one skill: use the repository's own name.
			name = slugSkill(filepath.Base(root))
		}
		out = append(out, skillCandidate{
			Name:        name,
			Description: describeSkill(files["SKILL.md"]),
			Rel:         dir,
			Files:       files,
		})
	}
	return out, nil
}

// importRefusalMessage turns a containment refusal into the sentence the tenant
// reads. It names the offending path: "this import was refused" without saying
// which file is a bug report nobody can act on.
func importRefusalMessage(err error) string {
	var refusal *tenantfs.Refusal
	if errors.As(err, &refusal) {
		switch refusal.Kind {
		case tenantfs.KindSymlink:
			return fmt.Sprintf(
				"导入被拒绝：仓库里的 %q 是符号链接。符号链接可以指向导入目录之外的任何文件"+
					"（例如控制面自己的密钥），所以整次导入都不会进行，也没有创建任何 Skill。请把该文件换成普通文件后重试。",
				refusal.Path)
		case tenantfs.KindOutside:
			return fmt.Sprintf(
				"导入被拒绝：仓库里的 %q 解析到了导入目录之外（%s），整次导入都不会进行，也没有创建任何 Skill。",
				refusal.Path, refusal.Detail)
		case tenantfs.KindNotRegular:
			return fmt.Sprintf(
				"导入被拒绝：仓库里的 %q 不是普通文件（%s），整次导入都不会进行，也没有创建任何 Skill。",
				refusal.Path, refusal.Detail)
		case tenantfs.KindTooLarge:
			return fmt.Sprintf("这个技能超过了单次导入上限（4 MB）：%s", refusal.Path)
		}
	}
	return err.Error()
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
