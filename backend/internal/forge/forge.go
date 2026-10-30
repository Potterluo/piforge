// Package forge creates pull requests on a hosted git forge (PRD FR-12.6).
//
// There is no vendor SDK here on purpose. "Open a PR" is one HTTP call whose
// shape a project can describe (API root + repository + token), so the product
// supports the two dialects that cover the forges people actually point it at:
//
//   - github — GitHub, GitHub Enterprise, Gitea and Forgejo all accept
//     POST {api}/repos/{owner}/{repo}/pulls with a bearer token.
//   - gitlab — GitLab and self-hosted GitLab, whose merge requests live at
//     POST {api}/projects/{urlencoded}/merge_requests with PRIVATE-TOKEN.
//
// A third dialect would be a new function, not a new configuration knob: the
// request body, auth header and response shape all differ, and pretending a
// string template could cover them would turn a wrong guess into a 404 the user
// has to debug.
//
// Credentials never travel in a URL or a query string. The token is set as a
// request header, is not part of any error, and the caller is responsible for
// sealing it at rest (the control plane uses its AES-GCM secret box).
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kind selects the REST dialect.
type Kind string

const (
	GitHub Kind = "github"
	GitLab Kind = "gitlab"
)

// ErrNoToken means the client cannot authenticate.
var ErrNoToken = errors.New("forge: no token configured")

// maxResponseBytes bounds one forge response. A pull request is a small
// object; reading an unbounded body to report an error is how a gateway turns
// into a memory problem.
const maxResponseBytes = 1 << 20

// Config is one project's forge wiring.
type Config struct {
	Kind    string
	APIBase string
	Repo    string
	Token   string
	// HTTP is optional; nil means a 20s client. Tests inject a server's client.
	HTTP *http.Client
}

// PullRequest is the subset of a forge's PR object this product stores.
type PullRequest struct {
	Number int
	URL    string
	Title  string
	Head   string
	Base   string
	// Existing is true when this object was already open before the request;
	// the caller did not just create it.
	Existing bool
}

// CreateInput is what a person typed plus the refs involved.
type CreateInput struct {
	Title string
	Body  string
	Head  string
	Base  string
}

// APIError is a non-2xx answer from the forge. Message is the forge's own text
// (flattened when it is a field map), preserved verbatim because it is the only
// thing that explains *why*: "Validation Failed", "Another open merge request
// already exists for this source branch", "Bad credentials".
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("forge: HTTP %d: %s", e.Status, e.Message)
}

// Client speaks one dialect to one forge.
type Client struct {
	kind    Kind
	apiBase string
	repo    string
	token   string
	http    *http.Client
}

// New validates cfg and builds a client. Validation happens here, at save
// time, so a typo in the API base is refused by the settings form rather than
// discovered when someone tries to open their first PR.
func New(cfg Config) (*Client, error) {
	kind := Kind(strings.ToLower(strings.TrimSpace(cfg.Kind)))
	if kind == "" {
		kind = GitHub
	}
	if kind != GitHub && kind != GitLab {
		return nil, fmt.Errorf("forge: unknown kind %q (supported: github, gitlab)", cfg.Kind)
	}

	base := strings.TrimSpace(cfg.APIBase)
	if base == "" {
		return nil, errors.New("forge: api_base is required")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("forge: api_base %q is not a URL", cfg.APIBase)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("forge: api_base must be http or https")
	}
	if parsed.User != nil {
		return nil, errors.New("forge: api_base must not embed credentials; the token is stored separately")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("forge: api_base must not carry a query or fragment")
	}
	base = strings.TrimRight(parsed.String(), "/")

	repo := strings.TrimSpace(cfg.Repo)
	if repo == "" {
		return nil, errors.New("forge: repo is required (owner/name)")
	}
	if strings.ContainsAny(repo, "\x00\r\n\t ") {
		return nil, errors.New("forge: repo must not contain whitespace")
	}
	segments := strings.Split(strings.Trim(repo, "/"), "/")
	if kind == GitHub && (len(segments) != 2 || segments[0] == "" || segments[1] == "") {
		return nil, fmt.Errorf("forge: github repo must be owner/name, got %q", cfg.Repo)
	}
	for _, segment := range segments {
		if segment == "" {
			return nil, fmt.Errorf("forge: repo %q has an empty path segment", cfg.Repo)
		}
	}

	hc := cfg.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{kind: kind, apiBase: base, repo: strings.Join(segments, "/"),
		token: strings.TrimSpace(cfg.Token), http: hc}, nil
}

// KindOf normalises a configured kind for storage.
func KindOf(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), string(GitLab)) {
		return string(GitLab)
	}
	return string(GitHub)
}

// Create opens a pull request, or returns the one that already exists.
//
// Idempotency is not a nicety: a push is retried after a timeout all the time,
// and a second PR for the same branch is noise a reviewer has to clean up. The
// client therefore asks the forge first; if the create call still loses the
// race, a duplicate answer is resolved by asking again instead of surfacing the
// forge's 422 as a failure.
func (c *Client) Create(ctx context.Context, in CreateInput) (*PullRequest, error) {
	if c.token == "" {
		return nil, ErrNoToken
	}
	if existing, err := c.FindOpen(ctx, in.Head, in.Base); err == nil && existing != nil {
		existing.Existing = true
		return existing, nil
	}

	var pr *PullRequest
	var err error
	switch c.kind {
	case GitLab:
		pr, err = c.createGitLab(ctx, in)
	default:
		pr, err = c.createGitHub(ctx, in)
	}
	if err == nil {
		return pr, nil
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) || !duplicateStatus(apiErr.Status) {
		return nil, err
	}
	// The forge says a PR for this branch exists. Trust the list over the
	// error text: only return "already exists" when there is one to show.
	if existing, ferr := c.FindOpen(ctx, in.Head, in.Base); ferr == nil && existing != nil {
		existing.Existing = true
		return existing, nil
	}
	return nil, err
}

// duplicateStatus is the family of refusals that can mean "already open":
// GitHub answers 422, Gitea 409, GitLab 400 or 409 depending on version.
func duplicateStatus(status int) bool {
	return status == http.StatusUnprocessableEntity || status == http.StatusConflict ||
		status == http.StatusBadRequest
}

// FindOpen returns the open pull request for head→base, or nil.
func (c *Client) FindOpen(ctx context.Context, head, base string) (*PullRequest, error) {
	if c.token == "" {
		return nil, ErrNoToken
	}
	switch c.kind {
	case GitLab:
		return c.findGitLab(ctx, head, base)
	default:
		return c.findGitHub(ctx, head, base)
	}
}

// ---------------------------------------------------------------------------
// GitHub dialect
// ---------------------------------------------------------------------------

type githubPR struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
	Head    struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (c *Client) githubPath() string {
	segments := strings.Split(c.repo, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return "/repos/" + strings.Join(segments, "/") + "/pulls"
}

func (c *Client) findGitHub(ctx context.Context, head, base string) (*PullRequest, error) {
	query := url.Values{}
	query.Set("state", "open")
	query.Set("head", c.headRef(head))
	if base != "" {
		query.Set("base", base)
	}
	query.Set("per_page", "100")
	var list []githubPR
	if err := c.do(ctx, http.MethodGet, c.githubPath()+"?"+query.Encode(), nil, &list); err != nil {
		return nil, err
	}
	for _, pr := range list {
		if pr.Number > 0 && pr.HTMLURL != "" {
			return &PullRequest{Number: pr.Number, URL: pr.HTMLURL, Title: pr.Title,
				Head: pr.Head.Ref, Base: pr.Base.Ref, Existing: true}, nil
		}
	}
	return nil, nil
}

func (c *Client) createGitHub(ctx context.Context, in CreateInput) (*PullRequest, error) {
	payload := map[string]string{
		"title": in.Title,
		"body":  in.Body,
		"head":  c.headRef(in.Head),
		"base":  in.Base,
	}
	var pr githubPR
	if err := c.do(ctx, http.MethodPost, c.githubPath(), payload, &pr); err != nil {
		return nil, err
	}
	if pr.Number == 0 || pr.HTMLURL == "" {
		return nil, &APIError{Status: http.StatusBadGateway,
			Message: "the forge answered without a pull request number or URL"}
	}
	return &PullRequest{Number: pr.Number, URL: pr.HTMLURL, Title: pr.Title,
		Head: pr.Head.Ref, Base: pr.Base.Ref}, nil
}

// headRef is "owner:branch" for GitHub-compatible forges. The owner is the
// repository's first segment; a bare branch also works, but the qualified form
// is the one that also works when the head lives in a fork.
func (c *Client) headRef(branch string) string {
	if strings.Contains(branch, ":") {
		return branch
	}
	owner := strings.SplitN(c.repo, "/", 2)[0]
	if owner == "" {
		return branch
	}
	return owner + ":" + branch
}

// ---------------------------------------------------------------------------
// GitLab dialect
// ---------------------------------------------------------------------------

type gitlabMR struct {
	IID    int    `json:"iid"`
	WebURL string `json:"web_url"`
	Title  string `json:"title"`
	Source string `json:"source_branch"`
	Target string `json:"target_branch"`
}

func (c *Client) gitlabPath() string {
	// GitLab addresses a project by its full path URL-encoded into one segment:
	// /projects/group%2Fsub%2Fproj/merge_requests.
	return "/projects/" + url.PathEscape(c.repo) + "/merge_requests"
}

func (c *Client) findGitLab(ctx context.Context, head, base string) (*PullRequest, error) {
	query := url.Values{}
	query.Set("state", "opened")
	query.Set("source_branch", head)
	if base != "" {
		query.Set("target_branch", base)
	}
	var list []gitlabMR
	if err := c.do(ctx, http.MethodGet, c.gitlabPath()+"?"+query.Encode(), nil, &list); err != nil {
		return nil, err
	}
	for _, mr := range list {
		if mr.IID > 0 && mr.WebURL != "" {
			return &PullRequest{Number: mr.IID, URL: mr.WebURL, Title: mr.Title,
				Head: mr.Source, Base: mr.Target, Existing: true}, nil
		}
	}
	return nil, nil
}

func (c *Client) createGitLab(ctx context.Context, in CreateInput) (*PullRequest, error) {
	payload := map[string]string{
		"title":         in.Title,
		"description":   in.Body,
		"source_branch": in.Head,
		"target_branch": in.Base,
	}
	var mr gitlabMR
	if err := c.do(ctx, http.MethodPost, c.gitlabPath(), payload, &mr); err != nil {
		return nil, err
	}
	if mr.IID == 0 || mr.WebURL == "" {
		return nil, &APIError{Status: http.StatusBadGateway,
			Message: "the forge answered without a merge request id or URL"}
	}
	return &PullRequest{Number: mr.IID, URL: mr.WebURL, Title: mr.Title,
		Head: mr.Source, Base: mr.Target}, nil
}

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

// do performs one request. The token is only ever a header here: not in the
// URL, not in the body, not in an error.
func (c *Client) do(ctx context.Context, method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("forge: encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiBase+path, body)
	if err != nil {
		return fmt.Errorf("forge: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "piforge")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch c.kind {
	case GitLab:
		req.Header.Set("PRIVATE-TOKEN", c.token)
	default:
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}

	res, err := c.http.Do(req)
	if err != nil {
		// *url.Error repeats the URL; it cannot contain the token because the
		// token was never in the URL. It can contain a host and a path, which
		// is exactly what a person needs to fix a wrong api_base.
		return fmt.Errorf("forge: %s %s: %w", method, c.apiBase, err)
	}
	defer res.Body.Close()

	raw, readErr := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if readErr != nil {
		return fmt.Errorf("forge: read response: %w", readErr)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return &APIError{Status: res.StatusCode, Message: forgeMessage(raw)}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("forge: decode response: %w", err)
	}
	return nil
}

// forgeMessage keeps the forge's own words. GitHub and Gitea use a string
// message; GitLab sometimes uses an array; a validation failure can also be a
// field→messages map, which is flattened rather than dropped.
func forgeMessage(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "the forge returned an empty body"
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return truncate(trimmed)
	}
	message := flatten(obj["message"])
	if errs, ok := obj["errors"]; ok {
		detail := flatten(errs)
		if detail != "" {
			if message == "" {
				message = detail
			} else {
				message += " (" + detail + ")"
			}
		}
	}
	if message == "" {
		return truncate(trimmed)
	}
	return truncate(message)
}

// flatten renders JSON error values as one deterministic sentence.
func flatten(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if text := flatten(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "; ")
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			text := flatten(value[key])
			if text == "" {
				continue
			}
			parts = append(parts, key+": "+text)
		}
		return strings.Join(parts, "; ")
	case bool:
		return strconv.FormatBool(value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

func truncate(s string) string {
	const max = 2000
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "…"
}
