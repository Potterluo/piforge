package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/forge"
	"github.com/piforge/piforge/backend/internal/gitx"
)

// Open a pull request from a session (PRD FR-12.6).
//
// The loop this closes is "change → test → share": the workbench could produce
// commits and even push them, but the last step — a reviewable link — still
// happened in a browser tab. The flow is deliberately three named steps:
//
//	1. 配置   — which forge, repository and token this project uses
//	2. 推送   — the branch must exist on the remote before a PR can name it
//	3. 创建   — one HTTP call, whose refusal is shown verbatim
//
// Every failure names its step. A bare "failed" would hide that the fix for a
// missing remote is in the project form, while the fix for a non-fast-forward
// is a pull — two different places, two different actions.

// prPushTimeout bounds the push half. A push is bytes over the network and may
// legitimately be slow; the forge call after it is a small JSON exchange.
const (
	prPushTimeout  = 120 * time.Second
	prForgeTimeout = 30 * time.Second
	prReadTimeout  = 20 * time.Second
)

// prPreview is what the dialog renders before anything is pushed.
type prPreview struct {
	ProjectID   string `json:"project_id"`
	Branch      string `json:"branch"`
	BaseBranch  string `json:"base_branch"`
	Remote      bool   `json:"remote"`
	Ahead       int    `json:"ahead"`
	Behind      int    `json:"behind"`
	CommitCount int    `json:"commit_count"`
	// Commits is capped for display; CommitCount is exact.
	Commits   []contracts.GitCommit  `json:"commits"`
	Files     []contracts.PRFile     `json:"files"`
	Additions int                    `json:"additions"`
	Deletions int                    `json:"deletions"`
	Dirty     bool                   `json:"dirty"`
	Title     string                 `json:"title"`
	Body      string                 `json:"body"`
	Forge     *contracts.ForgeConfig `json:"forge,omitempty"`
	// PullRequest is set when a PR for this branch is already known, either
	// recorded by an earlier run or found on the forge.
	PullRequest *contracts.PullRequest `json:"pull_request,omitempty"`
	Warnings    []string               `json:"warnings,omitempty"`
}

// createPRRequest is the dialog's confirmation. Title is required; the body may
// be empty (a one-line PR is still a PR).
type createPRRequest struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	BaseBranch string `json:"base_branch,omitempty"`
}

// createPRResponse reports the PR and what the push moved. `created` is false
// when the flow found an existing PR instead of opening a second one.
type createPRResponse struct {
	PullRequest *contracts.PullRequest `json:"pull_request"`
	Created     bool                   `json:"created"`
	Pushed      contracts.RemoteResult `json:"pushed"`
	Warnings    []string               `json:"warnings,omitempty"`
}

// handlePRPreview answers "what would this PR contain?" without writing
// anything. It reads the checkout, the last recorded PR and (when a token is
// configured) the forge's open list for this branch.
func (s *server) handlePRPreview(w http.ResponseWriter, r *http.Request) {
	root, sess, ok := s.remoteRoot(w, r)
	if !ok {
		return
	}
	user := auth.MustUser(r.Context())
	ctx, cancel := detach(r.Context(), prReadTimeout)
	defer cancel()

	project, err := s.deps.Store.Project(ctx, user.ID, sess.ProjectID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	status, err := s.deps.Workspace.Status(ctx, root)
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInternal, "读取分支状态失败：%v", err))
		return
	}
	branch := strings.TrimSpace(status.Branch)
	base := prBaseBranch(project, "")
	if branch == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "这个 checkout 不在任何分支上（detached HEAD），PR 需要一条分支"))
		return
	}

	prRange, err := s.deps.Workspace.PRRange(ctx, root, base)
	if err != nil {
		if errors.Is(err, gitx.ErrBaseMissing) {
			s.fail(w, r, withStatusDetails(http.StatusBadRequest, contracts.ErrInvalidArgument,
				"基准分支 "+base+" 在这个 checkout 里不存在，无法计算 PR 的内容。检查项目的默认分支，或先拉取远端。",
				map[string]any{"step": "base", "base_branch": base}))
			return
		}
		s.fail(w, r, apiErr(contracts.ErrInternal, "计算 PR 内容失败：%v", err))
		return
	}

	out := prPreview{
		ProjectID: project.ID, Branch: branch, BaseBranch: base,
		CommitCount: prRange.CommitCount, Commits: prRange.Commits,
		Files: prRange.Files, Additions: prRange.Additions, Deletions: prRange.Deletions,
		Dirty: !status.Clean, Forge: project.Forge,
		Title: s.suggestedPRTitle(ctx, user.ID, sess),
	}
	if len(prRange.Commits) > 0 && strings.TrimSpace(prRange.Commits[0].Subject) != "" {
		out.Body = strings.TrimSpace(prRange.Commits[0].Subject)
	}
	if strings.TrimSpace(out.Title) == "" {
		out.Title = out.Body
	}

	// Ahead/behind is informational here: the counts say whether the push has
	// anything to do and whether it is likely to be refused.
	remote := true
	ahead, behind, rerr := s.deps.Workspace.AheadBehind(ctx, root)
	switch {
	case errors.Is(rerr, gitx.ErrNoRemote):
		remote = false
	case rerr != nil:
		out.Warnings = append(out.Warnings, "读取远端状态失败："+rerr.Error())
	}
	out.Remote, out.Ahead, out.Behind = remote, ahead, behind
	if !remote {
		out.Warnings = append(out.Warnings, "这个 checkout 没有远端（origin）。PR 需要先把分支推上去；先给项目填一个可访问的 repo_url。")
	}
	if behind > 0 {
		out.Warnings = append(out.Warnings, "远端还有 "+strconv.Itoa(behind)+" 个提交没有拉取，推送可能被拒绝；先拉取再创建更稳。")
	}
	if out.Dirty {
		out.Warnings = append(out.Warnings, "工作区还有未提交的改动，它们不会被包含在这次 PR 里。")
	}
	if branch == base {
		out.Warnings = append(out.Warnings, "当前就在基准分支 "+base+" 上；PR 需要一条与基准不同的分支。")
	}
	if out.CommitCount == 0 {
		out.Warnings = append(out.Warnings, "这条分支相对 "+base+" 没有新提交，PR 会是空的。")
	}

	// A receipt from an earlier run is shown immediately, even offline.
	if existing, lerr := s.deps.Store.PullRequestForBranch(ctx, user.ID, sess.ID, branch); lerr == nil && existing != nil {
		out.PullRequest = existing
	}

	// Ask the forge only when it can answer. A lookup failure is a warning, not
	// a failed preview: the dialog must still work while the forge is down.
	if project.Forge != nil && project.Forge.HasToken {
		client, cerr := s.forgeClient(ctx, user.ID, project)
		if cerr != nil {
			out.Warnings = append(out.Warnings, "无法读取已保存的 token："+cerr.Error())
		} else if found, ferr := client.FindOpen(ctx, branch, base); ferr != nil {
			out.Warnings = append(out.Warnings, "无法查询代码托管平台上已有的 PR："+ferr.Error())
		} else if found != nil {
			out.PullRequest = toContractPR(found)
		}
	}

	writeJSON(w, http.StatusOK, out)
}

// handleCreatePR pushes the branch, then opens the pull request.
func (s *server) handleCreatePR(w http.ResponseWriter, r *http.Request) {
	root, sess, ok := s.remoteRoot(w, r)
	if !ok {
		return
	}
	user := auth.MustUser(r.Context())
	var in createPRRequest
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		s.fail(w, r, prStepErr(http.StatusBadRequest, contracts.ErrInvalidArgument, "form",
			"Pull Request 标题不能为空"))
		return
	}

	ctx, cancel := detach(r.Context(), prPushTimeout+prForgeTimeout+prReadTimeout)
	defer cancel()

	project, err := s.deps.Store.Project(ctx, user.ID, sess.ProjectID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// Step 1: configuration.
	if project.Forge == nil || strings.TrimSpace(project.Forge.APIBase) == "" || strings.TrimSpace(project.Forge.Repo) == "" {
		s.fail(w, r, prStepErr(http.StatusBadRequest, contracts.ErrInvalidArgument, "config",
			"第 1 步（配置）失败：这个项目还没有配置代码托管平台。在「创建 Pull Request」里填 API 地址、仓库和 token，或在项目设置里保存后再重试。"))
		return
	}
	client, err := s.forgeClient(ctx, user.ID, project)
	if err != nil {
		s.fail(w, r, prStepErr(http.StatusBadRequest, contracts.ErrInvalidArgument, "config",
			"第 1 步（配置）失败："+err.Error()+"。重新保存代码托管平台配置后再试。"))
		return
	}

	base := prBaseBranch(project, in.BaseBranch)
	status, err := s.deps.Workspace.Status(ctx, root)
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInternal, "读取分支状态失败：%v", err))
		return
	}
	branch := strings.TrimSpace(status.Branch)
	if branch == "" {
		s.fail(w, r, prStepErr(http.StatusBadRequest, contracts.ErrInvalidArgument, "branch",
			"这个 checkout 不在任何分支上（detached HEAD），PR 需要一条分支。"))
		return
	}
	if branch == base {
		s.fail(w, r, prStepErr(http.StatusBadRequest, contracts.ErrInvalidArgument, "branch",
			"当前就在基准分支 "+base+" 上，不能把它 PR 给它自己。先在一条新分支上提交，再创建 PR。"))
		return
	}

	// Step 2: push. A PR names a ref on the remote, so the remote has to have it.
	pushCtx, pushCancel := detach(ctx, prPushTimeout)
	pushed, err := s.deps.Workspace.Push(pushCtx, root)
	pushCancel()
	if err != nil {
		s.fail(w, r, s.pushStepError(err))
		return
	}

	// Step 3: create. The forge's own refusal text is what the user gets.
	createCtx, createCancel := detach(ctx, prForgeTimeout)
	pr, created, err := s.openPullRequest(createCtx, client, title, in.Body, branch, base)
	createCancel()
	if err != nil {
		s.fail(w, r, forgeStepError(err))
		return
	}
	pr.Head, pr.Base = branch, base

	resp := createPRResponse{PullRequest: pr, Created: created, Pushed: pushed}
	if err := s.deps.Store.SavePullRequest(ctx, user.ID, sess.ID, project.ID, branch, base,
		project.Forge.Kind, pr); err != nil {
		// The PR exists on the forge; saying "failed" would invite a second one.
		// The link is returned with a warning about the unrecorded receipt.
		resp.Warnings = append(resp.Warnings, "PR 已创建，但本地记录没有保存（刷新后可能不再显示）："+err.Error())
		s.log.ErrorContext(r.Context(), "could not record the pull request", "error", err.Error())
	}
	s.recordGitEvent("pr.created", sess, branch+" → "+base+" "+pr.URL)

	statusCode := http.StatusCreated
	if !created {
		statusCode = http.StatusOK
	}
	writeJSON(w, statusCode, resp)
}

// openPullRequest asks the forge for an existing PR first and creates one only
// when there is none. Keeping that decision here, rather than in the client,
// is what makes the response able to say which of the two happened.
func (s *server) openPullRequest(ctx context.Context, client *forge.Client, title, body, head, base string) (*contracts.PullRequest, bool, error) {
	if existing, err := client.FindOpen(ctx, head, base); err == nil && existing != nil {
		existing.Existing = true
		return toContractPR(existing), false, nil
	} else if err != nil {
		// A failed lookup is not a failed create: the POST below either works or
		// gives the forge's own reason. The one exception is a bad token, which
		// the create call would repeat anyway.
		var apiErr *forge.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden) {
			return nil, false, err
		}
	}
	pr, err := client.Create(ctx, forge.CreateInput{Title: title, Body: body, Head: head, Base: base})
	if err != nil {
		return nil, false, err
	}
	return toContractPR(pr), !pr.Existing, nil
}

// forgeClient resolves the project's token and builds a client. The token is
// opened in memory, used as a header, and never returned or logged.
func (s *server) forgeClient(ctx context.Context, userID string, project *contracts.Project) (*forge.Client, error) {
	if project.Forge == nil {
		return nil, errors.New("这个项目还没有配置代码托管平台")
	}
	if s.deps.Secrets == nil {
		return nil, errors.New("这个部署没有配置密钥（无法解密已保存的 token）")
	}
	cipherBlob, err := s.deps.Store.ProjectForgeTokenCipher(ctx, userID, project.ID)
	if err != nil {
		return nil, err
	}
	if len(cipherBlob) == 0 {
		return nil, errors.New("还没有保存代码托管平台的 token")
	}
	token, err := s.deps.Secrets.OpenString(cipherBlob)
	if err != nil {
		return nil, errors.New("保存的 token 无法解密（部署的密钥变过？重新保存一次 token）")
	}
	return forge.New(forge.Config{
		Kind:    project.Forge.Kind,
		APIBase: project.Forge.APIBase,
		Repo:    project.Forge.Repo,
		Token:   token,
	})
}

// pushStepError names the push refusal a person can act on and keeps git's own
// words on the end. Each case points at a different place to fix.
func (s *server) pushStepError(err error) error {
	switch {
	case errors.Is(err, gitx.ErrNoRemote):
		return prStepErr(http.StatusBadRequest, contracts.ErrInvalidArgument, "push",
			"第 2 步（推送）失败：这个 checkout 没有远端（origin），PR 需要先把分支推到远端。给项目填一个可访问的 repo_url（会话从这里 clone），再重试。")
	case errors.Is(err, gitx.ErrPushRejected), errors.Is(err, gitx.ErrDiverged):
		return prStepErrDetails(http.StatusConflict, contracts.ErrConflict, "push",
			"第 2 步（推送）失败：远端有本地没有的提交，这次推送不是快进。先在「变更」面板点拉取（仅快进），或按提示在终端合并/变基后重试。原始错误："+gitDetail(err),
			map[string]any{"reason": "non_fast_forward", "original": gitDetail(err)})
	case errors.Is(err, gitx.ErrNoCredentials):
		return prStepErrDetails(http.StatusBadRequest, contracts.ErrInvalidArgument, "push",
			"第 2 步（推送）失败：远端拒绝了凭据。给 origin 配好 SSH key 或 HTTPS 凭据（代码托管平台的 token 不能代替 git push 的凭据），再重试。原始错误："+gitDetail(err),
			map[string]any{"reason": "no_credentials", "original": gitDetail(err)})
	default:
		return prStepErrDetails(http.StatusBadGateway, contracts.ErrForge, "push",
			"第 2 步（推送）失败："+gitDetail(err)+"。检查远端地址和网络后重试。",
			map[string]any{"original": gitDetail(err)})
	}
}

// forgeStepError mirrors the forge's status for a 4xx (a bad token or a bad
// base is the caller's to fix) and reports 502 for anything else. The forge's
// message is appended unchanged: translating it would hide the one sentence
// that says what the forge disliked.
func forgeStepError(err error) error {
	var apiErr *forge.APIError
	if errors.As(err, &apiErr) {
		status := apiErr.Status
		if status < 400 || status > 499 {
			status = http.StatusBadGateway
		}
		return prStepErrDetails(status, contracts.ErrForge, "create",
			"第 3 步（创建 PR）失败：代码托管平台返回 HTTP "+strconv.Itoa(apiErr.Status)+"："+apiErr.Message,
			map[string]any{"reason": "forge_error", "forge_status": apiErr.Status, "forge_message": apiErr.Message})
	}
	return prStepErrDetails(http.StatusBadGateway, contracts.ErrForge, "create",
		"第 3 步（创建 PR）失败："+err.Error()+"。检查 API 地址、仓库名与网络后重试。",
		map[string]any{"reason": "forge_unreachable"})
}

// ---------------------------------------------------------------------------
// Project forge configuration
// ---------------------------------------------------------------------------

// projectForgeRequest is a full replacement of the non-secret half plus an
// optional token. `token` omitted means "keep the saved one" (the form cannot
// read it back); an explicit empty string clears it.
type projectForgeRequest struct {
	Kind       string  `json:"kind"`
	APIBase    string  `json:"api_base"`
	Repo       string  `json:"repo"`
	BaseBranch string  `json:"base_branch,omitempty"`
	Token      *string `json:"token,omitempty"`
}

// handleSetProjectForge saves where this project opens pull requests.
func (s *server) handleSetProjectForge(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	projectID := pathID(r)
	var in projectForgeRequest
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}

	kind := forge.KindOf(in.Kind)
	// Validate the shape with a placeholder token: this is exactly the check the
	// create flow would otherwise discover after a push.
	if _, err := forge.New(forge.Config{Kind: kind, APIBase: in.APIBase, Repo: in.Repo, Token: "validate"}); err != nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "代码托管平台配置无效：%v", err))
		return
	}

	var tokenCipher []byte
	setToken := in.Token != nil
	if setToken && strings.TrimSpace(*in.Token) != "" {
		if s.deps.Secrets == nil {
			s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "这个部署没有配置密钥，无法安全保存 token"))
			return
		}
		sealed, err := s.deps.Secrets.SealString(strings.TrimSpace(*in.Token))
		if err != nil {
			s.fail(w, r, apiErr(contracts.ErrInternal, "保存 token 失败：%v", err))
			return
		}
		tokenCipher = sealed
	}

	cfg := contracts.ForgeConfig{
		Kind: kind, APIBase: strings.TrimSpace(in.APIBase),
		Repo: strings.TrimSpace(in.Repo), BaseBranch: strings.TrimSpace(in.BaseBranch),
	}
	project, err := s.deps.Store.SetProjectForge(r.Context(), user.ID, projectID, cfg, tokenCipher, setToken)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, projectResponse{Project: project})
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// prBaseBranch is the target branch: an explicit request wins, then the saved
// forge default, then the project's default branch.
func prBaseBranch(project *contracts.Project, requested string) string {
	if branch := strings.TrimSpace(requested); branch != "" {
		return branch
	}
	if project != nil && project.Forge != nil && strings.TrimSpace(project.Forge.BaseBranch) != "" {
		return strings.TrimSpace(project.Forge.BaseBranch)
	}
	if project != nil && strings.TrimSpace(project.DefaultBranch) != "" {
		return strings.TrimSpace(project.DefaultBranch)
	}
	return "main"
}

// suggestedPRTitle is the session's first user message, capped to a title. The
// session title is the fallback: it is the same text unless the user renamed
// the session, and a rename is a better title, not a worse one.
func (s *server) suggestedPRTitle(ctx context.Context, userID string, sess *contracts.Session) string {
	if text := s.firstUserText(ctx, userID, sess.ID); text != "" {
		return prTitle(text)
	}
	if title := strings.TrimSpace(sess.Title); title != "" {
		return prTitle(title)
	}
	return ""
}

// firstUserText reads the first user turn out of the transcript projection.
// A failure is silent: the title is a convenience and the dialog still has an
// input.
func (s *server) firstUserText(ctx context.Context, userID, sessionID string) string {
	events, err := s.deps.Store.ListEvents(ctx, userID, sessionID, 0, 200)
	if err != nil {
		return ""
	}
	for _, ev := range events {
		if ev == nil || ev.Type != contracts.EvMsgFinal {
			continue
		}
		var payload struct {
			Role string `json:"role"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			continue
		}
		if payload.Role != "user" {
			continue
		}
		if text := strings.TrimSpace(payload.Text); text != "" {
			return text
		}
	}
	return ""
}

// prTitle takes one line and caps it at 72 runes: long enough for a sentence,
// short enough to read in a list.
func prTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > 72 {
			return string(runes[:72]) + "…"
		}
		return line
	}
	return ""
}

// gitDetail keeps a wrapped gitx error readable without its package prefix.
func gitDetail(err error) string {
	if err == nil {
		return ""
	}
	return userMessage(err)
}

func toContractPR(pr *forge.PullRequest) *contracts.PullRequest {
	if pr == nil {
		return nil
	}
	return &contracts.PullRequest{
		Number: pr.Number, URL: pr.URL, Title: pr.Title,
		Head: pr.Head, Base: pr.Base, Existing: pr.Existing,
	}
}

// prStepErr is the one shape every refusal in this flow uses: a status, the
// typed code, the message, and `step` in details so the dialog can label it.
func prStepErr(status int, code, step, message string) error {
	return prStepErrDetails(status, code, step, message, nil)
}

func prStepErrDetails(status int, code, step, message string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	details["step"] = step
	return withStatusDetails(status, code, message, details)
}
