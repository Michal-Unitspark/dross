// Package forge talks to a repository's issue tracker so dross can mirror
// its planning artefacts onto a board: milestones, phase issues (with a
// task checklist), and standalone quick-task issues.
//
// It mirrors internal/ship's provider-dispatch shape (forgejo | gitea |
// github) but covers issues/milestones/labels instead of pull requests.
// Only the Forgejo/Gitea REST backend is implemented today; github methods
// return ErrNotImplemented until someone wires `gh issue`.
package forge

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/hostallow"
	"github.com/Rivil/dross/internal/redact"
	"github.com/Rivil/dross/internal/secretscan"
)

// ErrNotImplemented is returned by every Client method when the configured
// provider has no board backend yet (currently: github).
var ErrNotImplemented = errors.New("issue-board sync is not implemented for this provider yet (forgejo/gitea only)")

// defaultLabelColor is applied to dross-created labels. Users can recolour
// them in the board UI; the value only matters at creation time.
const defaultLabelColor = "#7057ff"

// Client talks to one repo's issue tracker. Construct with New. A single
// concrete type serves every REST backend; provider-specific wire differences
// (GitLab's /projects path, PRIVATE-TOKEN auth, iid/description/state_event
// shapes) are branched per method off the `provider` field rather than behind
// an interface — matching internal/ship's concrete shape.
type Client struct {
	provider   string // "forgejo" | "gitea" | "gitlab"
	owner      string
	repo       string
	apiBase    string
	token      string
	authEnv    string // env var name (kept for diagnostic error messages)
	authScheme string // one of configenum.AuthSchemes; empty = private-token
	authUser   string // basic: the Basic-auth username paired with token
	projectID  string // gitlab: numeric project-id override (else derived from owner/repo)

	http     *http.Client
	labelIDs map[string]int // name -> id cache, lazily populated
}

// Config is the subset of [remote] settings the forge client needs. It maps
// 1:1 onto project.toml's Remote so callers can pass them straight through.
type Config struct {
	Provider   string // forgejo | gitea | gitlab | github
	URL        string // canonical https URL of the repo
	APIBase    string // REST base (forgejo/gitea: .../api/v1; gitlab: .../api/v4)
	AuthEnv    string // env var name holding the token (never the value)
	AuthScheme string // gitlab: "private-token" (default) | "bearer"
	ProjectID  string // gitlab: numeric project-id override; empty = derive from URL
	Project    string // youtrack: project short-name (e.g. "PROJ"); jira: project key; github: "owner/repo"; ignored by forge backends
	AuthUser   string // jira: account email for HTTP Basic auth (email:token)
	BoardID    string // github: Projects v2 board node id to add created issues to (empty = repo issues only)

	// APIVersion is the azure-devops api-version every Boards call carries;
	// empty = 7.1. AreaPath and IterationPath are the System.AreaPath and
	// System.IterationPath every created work item gets; empty = the project's
	// defaults. All three come from [board] alone, never from [remote].
	APIVersion    string
	AreaPath      string
	IterationPath string

	// Fields overrides the tracker-native field names sync writes to. Every
	// entry is optional; an empty one falls back to the provider's own literal,
	// so a zero-value Config behaves exactly as it did before this existed.
	Fields Fields

	// Hosts is the API host allowlist every constructor checks APIBase
	// against before it reads the token out of the environment.
	//
	// The zero value is NOT unrestricted — it resolves to hostallow's built-in
	// SaaS defaults — so a caller that forgets to populate this field fails
	// closed rather than silently reopening the hole. See internal/hostallow.
	Hosts hostallow.Policy
}

// Fields names the tracker-native fields board sync writes to, so a project
// that renamed one (or runs a non-English tracker UI) syncs without a code
// change. Each zero value means "use this provider's default literal" —
// resolution lives with the provider that knows those literals, not here.
type Fields struct {
	State       string // youtrack: State custom field (default "State")
	Type        string // youtrack: issue-type custom field (default "Type")
	FixVersions string // youtrack: version bundle field (default "Fix versions")
}

// New validates config, resolves the token from the environment, and returns
// a ready Client. It errors early on the same conditions the ship backend
// checks: missing APIBase/AuthEnv, unset token, unparseable repo URL.
func New(cfg Config) (*Client, error) {
	provider := configenum.Normalize(cfg.Provider)
	switch provider {
	case "forgejo", "gitea", "gitlab":
		// supported below
	case "github":
		return nil, ErrNotImplemented
	default:
		return nil, fmt.Errorf("unsupported provider %q (expected %s)", cfg.Provider, configenum.ForgeRESTProviders.List())
	}
	// backendName makes config errors carry the active provider so telemetry
	// classifies them under "provider" (see telemetry.ClassifyError).
	backendName := "forgejo"
	if provider == "gitlab" {
		backendName = "gitlab"
	}
	if cfg.APIBase == "" {
		return nil, fmt.Errorf("%s backend needs APIBase (set [remote].api_base)", backendName)
	}
	if cfg.AuthEnv == "" {
		return nil, fmt.Errorf("%s backend needs AuthEnv (set [remote].auth_env)", backendName)
	}
	// The basic scheme is half a credential without its username: sending
	// Basic base64(:token) would 401 with nothing the user can act on, so fail
	// here and name the setting instead.
	if configenum.Normalize(cfg.AuthScheme) == "basic" && strings.TrimSpace(cfg.AuthUser) == "" {
		return nil, fmt.Errorf("%s backend: auth_scheme = basic needs an auth_user (set [board].auth_user or [remote].auth_user)", backendName)
	}
	// Before the Getenv, not after. The ordering is the guarantee: a token that
	// has been read is a token that exists in this process, and every later
	// error path is one wrapping mistake away from printing it. Checking first
	// means a refused host never causes the secret to be touched at all.
	if err := cfg.Hosts.Check("[remote].api_base", cfg.APIBase); err != nil {
		return nil, err
	}
	token := os.Getenv(cfg.AuthEnv)
	if token == "" {
		return nil, fmt.Errorf("$%s is not set; run `dross env set %s` in your shell", cfg.AuthEnv, cfg.AuthEnv)
	}
	owner, repo, err := splitOwnerRepo(cfg.URL)
	if err != nil {
		return nil, err
	}
	return &Client{
		provider:   provider,
		owner:      owner,
		repo:       repo,
		apiBase:    strings.TrimRight(cfg.APIBase, "/"),
		token:      token,
		authEnv:    cfg.AuthEnv,
		authScheme: cfg.AuthScheme,
		authUser:   strings.TrimSpace(cfg.AuthUser),
		projectID:  strings.TrimSpace(cfg.ProjectID),
		http:       &http.Client{Timeout: 30 * time.Second},
		labelIDs:   map[string]int{},
	}, nil
}

// isGitLab reports whether this client targets a GitLab instance.
func (c *Client) isGitLab() bool { return c.provider == "gitlab" }

// --- public types ---

// BoardClient is the issue-board surface shared by every backend: the forge
// REST Client (forgejo/gitea/gitlab) and the YouTrackClient. Issues are
// addressed by their readable string id (a forge issue number "42" or a
// YouTrack idReadable "PROJ-7"), so board.json and every call site speak one
// id type regardless of backend.
type BoardClient interface {
	EnsureMilestone(title, description string) (string, error)
	CreateIssue(in IssueInput) (*Issue, error)
	GetIssue(key string) (*Issue, error)
	UpdateIssue(key string, patch IssuePatch) (*Issue, error)
	CloseIssue(key string) error
	ListIssues(f IssueFilter) ([]Issue, error)
}

var _ BoardClient = (*Client)(nil)

// Every backend with a column model is a StateWriter; GitHubClient is not (see
// github.go). Pinned as compile-time assertions so removing an implementation
// fails the build rather than only the undo path at runtime.
var (
	_ StateWriter = (*Client)(nil)
	_ StateWriter = (*YouTrackClient)(nil)
	_ StateWriter = (*JiraClient)(nil)
)

// ErrNoLinkType means the tracker exposes no issue-link type to relate two
// issues with. It is a capability gap, not an outage: the caller warns, keeps
// whatever label carries the relationship instead, and continues. Callers must
// be able to tell it apart from a real HTTP failure, which is why it is a
// sentinel rather than a generic error string.
var ErrNoLinkType = errors.New("tracker exposes no issue-link type")

// IssueLinker is the optional capability of relating two issues to each other.
// It is deliberately NOT part of BoardClient: GitHub's REST issues API has no
// generic issue-link primitive, so that backend must fail an interface
// assertion rather than satisfy the method with a no-op — a silent stub would
// make the "no link possible here" path untestable and invisible.
//
// Callers assert for it (`linker, ok := client.(IssueLinker)`) and fall back to
// a label when the assertion fails.
type IssueLinker interface {
	// LinkIssues relates `from` to `to`. Idempotent: re-linking an existing
	// pair is a no-op, so it is safe on every re-sync.
	LinkIssues(from, to string) error
}

// ParentLinker is the optional capability of putting one issue UNDER another
// — a parent-child (hierarchy) link, as distinct from IssueLinker's peer
// relation. It sits beside IssueLinker rather than on BoardClient for the same
// reason: a tracker without a hierarchy (GitHub's REST issues) fails the
// assertion instead of satisfying it with a lossy stub, and the caller keeps
// whatever label carries the relationship.
type ParentLinker interface {
	// LinkParent makes parent the parent of child, replacing a different
	// parent child already has. Idempotent: a child already under parent is
	// left untouched.
	LinkParent(parent, child string) error
}

// StateWriter is the optional capability of writing a LITERAL tracker state
// onto an issue, bypassing the dross lifecycle state map.
//
// Every existing write is mapped: CloseIssueAs and SetState take a dross
// lifecycle status and resolve it through defaultYouTrackStateMap /
// defaultJiraStateMap, and the forge Client closes via
// UpdateIssue(IssuePatch{State}). That is right for the forward lifecycle,
// where dross knows which stage a card has reached — and useless for putting a
// card BACK: restoring an arbitrary prior column such as "In Review" is
// unimplementable through a map keyed by dross's own statuses.
//
// It sits alongside BoardClient rather than on it, following the IssueLinker
// precedent above and for the same two reasons. GitHubClient deliberately does
// NOT implement it: GitHub issues have no column model, so the honest answer is
// to fail the interface assertion and let the caller refuse by provider name,
// rather than satisfy the method with a lossy reopen that lands an "In Review"
// card in `open` and reports success. And keeping it off BoardClient leaves
// every existing fake — including the two in internal/cmd — satisfying the
// interface unchanged.
type StateWriter interface {
	// SetStateRaw writes `state` verbatim as the issue's tracker-native state
	// and verifies the read-back, erroring (naming the key) when the tracker
	// still reports something else. A write the workflow silently refused is
	// otherwise indistinguishable from a successful one — the same trap
	// CloseIssueAs's read-back exists to close.
	SetStateRaw(key, state string) error
}

// NewBoard returns the board client for the configured provider: the YouTrack
// backend for provider=youtrack, the Jira backend for provider=jira, the GitHub
// (repo-issues + Projects v2) backend for provider=github, otherwise the forge
// REST Client (forgejo/gitea/gitlab). It is the single entry point board
// operations use to resolve a client from the [board] config.
func NewBoard(cfg Config) (BoardClient, error) {
	switch configenum.Normalize(cfg.Provider) {
	case "youtrack":
		return NewYouTrack(cfg)
	case "jira":
		return NewJira(cfg)
	case "github":
		return NewGitHubProjects(cfg)
	}
	return New(cfg)
}

// Issue is the minimal shape dross cares about across operations.
type Issue struct {
	Number    int
	Key       string // tracker-native readable id (YouTrack idReadable, e.g. "PROJ-7"); forge backends leave this empty until the string-id migration
	Title     string
	Body      string
	State     string // "open" | "closed"
	Labels    []string
	Milestone string // milestone title, "" if none
	URL       string // html_url

	// WorkflowState is the tracker's OWN state name — the column a card sits
	// in — as distinct from State, which is normalised to open/closed.
	//
	// Empty where the backend has no such concept: forgejo, gitea and gitlab
	// boards are open/closed and nothing else, and GitHub's Projects v2 status
	// is a single-select field behind a GraphQL path dross does not have. A
	// caller that needs to know which column a card was dragged to must treat
	// empty as "this board cannot answer", not as "it did not move".
	WorkflowState string

	// Resolved is the tracker's own verdict that this issue is done, read back
	// from the tracker rather than inferred from what dross just wrote.
	//
	// It exists because "the close request returned 200" is not the same claim
	// as "the issue is closed": on YouTrack a State write can succeed against a
	// workflow that refuses the transition, leaving the issue open while ship
	// prints a confident "(closed)". A verify-on-read-back needs a field the
	// tracker fills in — this one.
	Resolved bool
}

// IssueInput is the create payload. Labels are names; missing ones are
// created on the fly. Milestone is a milestone id (0 = unassigned).
type IssueInput struct {
	Title     string
	Body      string
	Labels    []string
	Milestone int
}

// IssuePatch is a partial update. Nil fields are left unchanged. Labels, when
// non-nil, replace the issue's full label set (names; missing ones created).
type IssuePatch struct {
	Title     *string
	Body      *string
	State     *string // "open" | "closed"
	Labels    *[]string
	Milestone *int
}

// IssueFilter selects issues for ListIssues. State defaults to "open".
type IssueFilter struct {
	State  string   // "open" | "closed" | "all"
	Labels []string // label names; empty = any
}

// LabelSpec describes a label to ensure-exists. Color/Description are only
// used when the label has to be created.
type LabelSpec struct {
	Name        string
	Color       string // "#rrggbb"; defaults to defaultLabelColor
	Description string
}

// FilterKnownLabels splits `requested` against the tracker's `known` label
// index, preserving request order in both halves.
//
// A label the tracker has never heard of cannot match anything, and on some
// providers an unknown name makes the whole query error. Dropping it lets a
// partly-stale filter still do useful work — but the caller must name the
// dropped labels (see WarnDroppedLabels), because a silent drop reads as
// "nothing matched", which is the zero-versus-failure confusion c-2 exists to
// kill.
func FilterKnownLabels(requested, known []string) (kept, dropped []string) {
	index := make(map[string]bool, len(known))
	for _, k := range known {
		index[k] = true
	}
	for _, r := range requested {
		if index[r] {
			kept = append(kept, r)
			continue
		}
		dropped = append(dropped, r)
	}
	return kept, dropped
}

// The identity-label prefixes: each one, followed by an artefact id, names the
// single dross artefact a card mirrors. boardsync stamps them and reap reads
// them back; this is the one list both sides take them from.
const (
	IdentityTaskPrefix     = "dross/task:"
	IdentityPhasePrefix    = "dross/phase:"
	IdentityDeferredPrefix = "dross/deferred:"
	IdentityTargetPrefix   = "dross/target:"
)

var identityLabelPrefixes = []string{
	IdentityTaskPrefix,
	IdentityPhasePrefix,
	IdentityDeferredPrefix,
	IdentityTargetPrefix,
}

// IdentityLabelPrefixes returns a copy of the identity-label prefixes.
func IdentityLabelPrefixes() []string {
	return append([]string(nil), identityLabelPrefixes...)
}

// IsIdentityLabel reports whether label is a dross identity label. The dross/
// prefix alone is not identity: dross/status:* and dross/quick are shared by
// many cards, so they filter a query rather than look one card up.
func IsIdentityLabel(label string) bool {
	for _, p := range identityLabelPrefixes {
		if strings.HasPrefix(label, p) {
			return true
		}
	}
	return false
}

// WarnDroppedLabels names labels dropped by FilterKnownLabels on stderr. A
// no-op when nothing was dropped.
//
// Identity labels are dropped silently. A tracker that does not know
// dross/task:p/t-1 has no card for it yet, which is the expected answer on a
// first sync, not a stale filter. Every new task, phase or deferred item would
// otherwise print a warning that reads as a lookup failure.
func WarnDroppedLabels(provider string, dropped []string) {
	var named []string
	for _, d := range dropped {
		if !IsIdentityLabel(d) {
			named = append(named, d)
		}
	}
	if len(named) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: %s does not know the label(s) %s — dropped from the query\n",
		provider, strings.Join(named, ", "))
}

// --- milestones ---

// EnsureMilestone returns the id of the milestone titled `title`, creating it
// if absent. Idempotent: safe to call on every milestone-sync.
func (c *Client) EnsureMilestone(title, description string) (string, error) {
	var existing []struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
	}
	// Forgejo/Gitea need ?state=all to include closed milestones; GitLab returns
	// every state by default and rejects state=all, so omit the filter there.
	listQuery := "?state=all"
	if c.isGitLab() {
		listQuery = ""
	}
	if err := c.do("GET", c.path("/milestones")+listQuery, nil, &existing); err != nil {
		return "", fmt.Errorf("list milestones: %w", err)
	}
	for _, m := range existing {
		if m.Title == title {
			return strconv.Itoa(m.ID), nil
		}
	}
	var created struct {
		ID int `json:"id"`
	}
	if err := c.do("POST", c.path("/milestones"), map[string]any{
		"title":       title,
		"description": description,
	}, &created); err != nil {
		return "", fmt.Errorf("create milestone %q: %w", title, err)
	}
	return strconv.Itoa(created.ID), nil
}

// --- labels ---

// EnsureLabels makes sure every named label exists, creating missing ones
// with the given color/description, and returns a name->id map for all of
// them. Results are cached on the client for the rest of its lifetime.
func (c *Client) EnsureLabels(specs []LabelSpec) (map[string]int, error) {
	if err := c.loadLabels(); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, s := range specs {
		if id, ok := c.labelIDs[s.Name]; ok {
			out[s.Name] = id
			continue
		}
		color := s.Color
		if color == "" {
			color = defaultLabelColor
		}
		var created struct {
			ID int `json:"id"`
		}
		if err := c.do("POST", c.path("/labels"), map[string]any{
			"name":        s.Name,
			"color":       color,
			"description": s.Description,
		}, &created); err != nil {
			return nil, fmt.Errorf("create label %q: %w", s.Name, err)
		}
		c.labelIDs[s.Name] = created.ID
		out[s.Name] = created.ID
	}
	return out, nil
}

func (c *Client) loadLabels() error {
	if len(c.labelIDs) > 0 {
		return nil // already populated this run
	}
	var labels []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := c.do("GET", c.path("/labels")+"?limit=100", nil, &labels); err != nil {
		return fmt.Errorf("list labels: %w", err)
	}
	for _, l := range labels {
		c.labelIDs[l.Name] = l.ID
	}
	return nil
}

// resolveLabelIDs maps label names to ids, creating any that don't exist with
// the default color.
func (c *Client) resolveLabelIDs(names []string) ([]int, error) {
	specs := make([]LabelSpec, len(names))
	for i, n := range names {
		specs[i] = LabelSpec{Name: n}
	}
	byName, err := c.EnsureLabels(specs)
	if err != nil {
		return nil, err
	}
	ids := make([]int, len(names))
	for i, n := range names {
		ids[i] = byName[n]
	}
	return ids, nil
}

// --- issues ---

// CreateIssue opens a new issue and returns it.
func (c *Client) CreateIssue(in IssueInput) (*Issue, error) {
	if c.isGitLab() {
		body := map[string]any{"title": in.Title, "description": in.Body}
		if in.Milestone > 0 {
			body["milestone_id"] = in.Milestone
		}
		if len(in.Labels) > 0 {
			// GitLab takes labels as a comma-joined string and auto-creates
			// any that don't exist — no id resolution needed.
			body["labels"] = strings.Join(in.Labels, ",")
		}
		var raw gitlabIssueResponse
		if err := c.do("POST", c.path("/issues"), body, &raw); err != nil {
			return nil, fmt.Errorf("create issue: %w", err)
		}
		return raw.toIssue(), nil
	}
	body := map[string]any{"title": in.Title, "body": in.Body}
	if in.Milestone > 0 {
		body["milestone"] = in.Milestone
	}
	if len(in.Labels) > 0 {
		ids, err := c.resolveLabelIDs(in.Labels)
		if err != nil {
			return nil, err
		}
		body["labels"] = ids
	}
	var raw issueResponse
	if err := c.do("POST", c.path("/issues"), body, &raw); err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}
	return raw.toIssue(), nil
}

// UpdateIssue applies a partial patch. Label changes go through the dedicated
// labels endpoint (a full replace); everything else rides the issue PATCH.
func (c *Client) UpdateIssue(key string, patch IssuePatch) (*Issue, error) {
	number, err := strconv.Atoi(key)
	if err != nil {
		return nil, fmt.Errorf("invalid forge issue id %q (expected a number): %w", key, err)
	}
	if c.isGitLab() {
		// GitLab updates the whole issue in one PUT: state via state_event,
		// labels as a comma-joined string (full replace), no separate endpoint.
		body := map[string]any{}
		if patch.Title != nil {
			body["title"] = *patch.Title
		}
		if patch.Body != nil {
			body["description"] = *patch.Body
		}
		if patch.State != nil {
			if *patch.State == "closed" {
				body["state_event"] = "close"
			} else {
				body["state_event"] = "reopen"
			}
		}
		if patch.Milestone != nil {
			body["milestone_id"] = *patch.Milestone
		}
		if patch.Labels != nil {
			body["labels"] = strings.Join(*patch.Labels, ",")
		}
		var raw gitlabIssueResponse
		if len(body) > 0 {
			if err := c.do("PUT", c.path(fmt.Sprintf("/issues/%d", number)), body, &raw); err != nil {
				return nil, fmt.Errorf("update issue !%d: %w", number, err)
			}
		}
		return raw.toIssue(), nil
	}
	body := map[string]any{}
	if patch.Title != nil {
		body["title"] = *patch.Title
	}
	if patch.Body != nil {
		body["body"] = *patch.Body
	}
	if patch.State != nil {
		body["state"] = *patch.State
	}
	if patch.Milestone != nil {
		body["milestone"] = *patch.Milestone
	}
	var raw issueResponse
	if len(body) > 0 {
		if err := c.do("PATCH", c.path(fmt.Sprintf("/issues/%d", number)), body, &raw); err != nil {
			return nil, fmt.Errorf("update issue #%d: %w", number, err)
		}
	}
	if patch.Labels != nil {
		ids, err := c.resolveLabelIDs(*patch.Labels)
		if err != nil {
			return nil, err
		}
		// PUT replaces the issue's label set wholesale. Forgejo/Gitea respond
		// with the resulting LabelList ([]Label), NOT the issue — so we can't
		// decode into &raw without tripping "cannot unmarshal array into ...
		// issueResponse". The caller (issue phase-sync) discards the returned
		// *Issue, so dropping the response is the right call; if a future
		// caller needs the post-update issue state it should do a GetIssue
		// follow-up.
		if err := c.do("PUT", c.path(fmt.Sprintf("/issues/%d/labels", number)),
			map[string]any{"labels": ids}, nil); err != nil {
			return nil, fmt.Errorf("set labels on issue #%d: %w", number, err)
		}
	}
	return raw.toIssue(), nil
}

// CloseIssue is a convenience for the ship step.
func (c *Client) CloseIssue(key string) error {
	closed := "closed"
	_, err := c.UpdateIssue(key, IssuePatch{State: &closed})
	return err
}

// SetStateRaw writes a literal state onto a forge/GitLab issue and verifies the
// read-back.
//
// These boards have no column model — their state vocabulary is "open" and
// "closed" and nothing else — so `state` is passed through verbatim rather than
// mapped. That is exactly what makes it correct here: a value read back out of
// the reap ledger for one of these boards IS "open" or "closed", and anything
// else the tracker will refuse, which the read-back then reports rather than
// swallowing.
func (c *Client) SetStateRaw(key, state string) error {
	if _, err := c.UpdateIssue(key, IssuePatch{State: &state}); err != nil {
		return fmt.Errorf("set state %s on %s: %w", state, key, err)
	}
	iss, err := c.GetIssue(key)
	if err != nil {
		return fmt.Errorf("set state on %s: read back: %w", key, err)
	}
	if iss == nil || iss.State != state {
		got := ""
		if iss != nil {
			got = iss.State
		}
		return fmt.Errorf("set state on %s: wrote state %q but the issue still reads %q", key, state, got)
	}
	return nil
}

// GetIssue fetches a single issue by its readable id (GitLab: project-relative
// iid). The forge readable id is the issue number as a string.
func (c *Client) GetIssue(key string) (*Issue, error) {
	number, err := strconv.Atoi(key)
	if err != nil {
		return nil, fmt.Errorf("invalid forge issue id %q (expected a number): %w", key, err)
	}
	if c.isGitLab() {
		var raw gitlabIssueResponse
		if err := c.do("GET", c.path(fmt.Sprintf("/issues/%d", number)), nil, &raw); err != nil {
			return nil, fmt.Errorf("get issue !%d: %w", number, err)
		}
		return raw.toIssue(), nil
	}
	var raw issueResponse
	if err := c.do("GET", c.path(fmt.Sprintf("/issues/%d", number)), nil, &raw); err != nil {
		return nil, fmt.Errorf("get issue #%d: %w", number, err)
	}
	return raw.toIssue(), nil
}

// ListIssues returns issues matching the filter. PRs are excluded (the
// Forgejo/Gitea issues endpoint otherwise returns both) so inbound triage
// never surfaces pull requests as "new work".
//
// Labels are OR'd: forgejo, gitea and gitlab all intersect a comma-joined
// `labels=` param, so several labels in one request match only issues carrying
// every one of them. One request per label, unioned by issue number, is the
// only way to express "either label" against these APIs.
//
// Labels the instance does not know are dropped and named on stderr first (see
// FilterKnownLabels), and when every requested label is unknown the call
// returns nothing rather than degrading into a whole-project list.
func (c *Client) ListIssues(f IssueFilter) ([]Issue, error) {
	// One pass with no label param when unfiltered; otherwise one per label.
	// An empty filter reads no label index at all — `dross watch` polls this
	// unlabelled on a timer, and a label-index blip must not fail a heartbeat.
	queries := []string{""}
	if len(f.Labels) > 0 {
		if err := c.loadLabels(); err != nil {
			return nil, err
		}
		known := make([]string, 0, len(c.labelIDs))
		for name := range c.labelIDs {
			known = append(known, name)
		}
		kept, dropped := FilterKnownLabels(f.Labels, known)
		WarnDroppedLabels(c.provider, dropped)
		if len(kept) == 0 {
			return nil, nil
		}
		queries = kept
	}

	out := []Issue{}
	seen := make(map[int]bool, len(queries))
	for _, label := range queries {
		batch, err := c.listIssuesByLabel(f.State, label)
		if err != nil {
			return nil, err
		}
		for _, iss := range batch {
			if seen[iss.Number] {
				continue // already unioned in under an earlier label
			}
			seen[iss.Number] = true
			out = append(out, iss)
		}
	}
	return out, nil
}

// listIssuesByLabel runs one issue query, scoped to a single label (or none
// when label is empty).
func (c *Client) listIssuesByLabel(filterState, label string) ([]Issue, error) {
	if c.isGitLab() {
		// GitLab spells the open state "opened" and serves issues on a
		// dedicated endpoint (no PRs mixed in, so no type filter needed).
		state := filterState
		switch state {
		case "", "open":
			state = "opened"
		}
		q := url.Values{}
		q.Set("state", state)
		q.Set("per_page", "50")
		if label != "" {
			q.Set("labels", label)
		}
		var raw []gitlabIssueResponse
		if err := c.do("GET", c.path("/issues")+"?"+q.Encode(), nil, &raw); err != nil {
			return nil, fmt.Errorf("list issues: %w", err)
		}
		out := make([]Issue, 0, len(raw))
		for i := range raw {
			out = append(out, *raw[i].toIssue())
		}
		return out, nil
	}
	state := filterState
	if state == "" {
		state = "open"
	}
	q := url.Values{}
	q.Set("state", state)
	q.Set("type", "issues") // exclude PRs
	q.Set("limit", "50")
	if label != "" {
		q.Set("labels", label)
	}
	var raw []issueResponse
	if err := c.do("GET", c.path("/issues")+"?"+q.Encode(), nil, &raw); err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	out := make([]Issue, 0, len(raw))
	for i := range raw {
		// Defensive: some instances ignore type=issues on older versions.
		if raw[i].PullRequest != nil {
			continue
		}
		out = append(out, *raw[i].toIssue())
	}
	return out, nil
}

// --- wire types ---

type issueResponse struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	State       string `json:"state"`
	HTMLURL     string `json:"html_url"`
	PullRequest *struct {
		Merged bool `json:"merged"`
	} `json:"pull_request"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Milestone *struct {
		Title string `json:"title"`
	} `json:"milestone"`
}

func (r *issueResponse) toIssue() *Issue {
	iss := &Issue{
		Number: r.Number,
		Key:    strconv.Itoa(r.Number),
		Title:  r.Title,
		Body:   r.Body,
		State:  r.State,
		URL:    r.HTMLURL,
	}
	for _, l := range r.Labels {
		iss.Labels = append(iss.Labels, l.Name)
	}
	if r.Milestone != nil {
		iss.Milestone = r.Milestone.Title
	}
	return iss
}

// gitlabIssueResponse is GitLab's issue shape: the project-relative `iid` is the
// user-facing number, `description` is the body, `web_url` the browser link,
// labels are plain strings, and the open state is spelled "opened".
type gitlabIssueResponse struct {
	IID         int      `json:"iid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	WebURL      string   `json:"web_url"`
	Labels      []string `json:"labels"`
	Milestone   *struct {
		Title string `json:"title"`
	} `json:"milestone"`
}

func (r *gitlabIssueResponse) toIssue() *Issue {
	state := r.State
	if state == "opened" {
		state = "open" // normalise to dross's open/closed vocabulary
	}
	iss := &Issue{
		Number: r.IID,
		Key:    strconv.Itoa(r.IID),
		Title:  r.Title,
		Body:   r.Description,
		State:  state,
		Labels: r.Labels,
		URL:    r.WebURL,
	}
	if r.Milestone != nil {
		iss.Milestone = r.Milestone.Title
	}
	return iss
}

// --- low-level REST ---

// path builds the project-scoped API path for a suffix. GitLab uses
// /projects/{ref} (ref = URL-encoded owner/repo, or a numeric project-id
// override); Forgejo/Gitea use /repos/{owner}/{repo}.
func (c *Client) path(suffix string) string {
	if c.isGitLab() {
		return c.apiBase + "/projects/" + c.projectRef() + suffix
	}
	return c.apiBase + fmt.Sprintf("/repos/%s/%s", c.owner, c.repo) + suffix
}

// projectRef is the GitLab project identifier: a numeric project-id override
// when set, otherwise the URL-encoded "owner/repo" path (owner%2Frepo).
func (c *Client) projectRef() string {
	if c.projectID != "" {
		return c.projectID
	}
	return url.PathEscape(c.owner + "/" + c.repo)
}

// do performs a token-authenticated JSON request, with the credential removed
// from whatever error comes back.
//
// The wrap sits HERE, around the whole request, rather than at each Errorf
// inside doRaw. Every error this method can produce is downstream of a request
// that carried the token: the body snippet on a non-2xx (the obvious one), the
// transport failure, and the JSON decode error, which interpolates response
// bytes of its own. One wrap covers all three and cannot be forgotten at a
// return added later.
func (c *Client) do(method, endpoint string, body any, out any) error {
	return redact.Err(c.doRaw(method, endpoint, body, out), c.authEnv, c.token)
}

// doRaw is the unredacted request. Nothing outside do may call it.
func (c *Client) doRaw(method, endpoint string, body any, out any) error {
	// The payload is screened before any bytes are encoded or a request is
	// built (criterion c-2 of secret-detection): a credential in an issue body must
	// never leave the process, and one line here covers every method that
	// routes through do — now and later.
	if err := secretscan.ScanPayload(method+" "+endpoint, body); err != nil {
		return err
	}
	var rdr io.Reader
	if body != nil {
		buf := new(bytes.Buffer)
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return err
		}
		rdr = buf
	}
	req, err := http.NewRequest(method, endpoint, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Exactly one auth header is set. basic is checked first and wins outright:
	// pairing it with GitLab's PRIVATE-TOKEN would send two credentials, and the
	// scheme is an explicit choice the caller made over the provider default.
	switch {
	case configenum.Normalize(c.authScheme) == "basic":
		cred := base64.StdEncoding.EncodeToString([]byte(c.authUser + ":" + c.token))
		req.Header.Set("Authorization", "Basic "+cred)
	case c.isGitLab():
		if configenum.Normalize(c.authScheme) == "bearer" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		} else {
			req.Header.Set("PRIVATE-TOKEN", c.token)
		}
	default:
		req.Header.Set("Authorization", "token "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		snippet := string(respBody)
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		hint := ""
		switch resp.StatusCode {
		case 401:
			hint = " (check $" + c.authEnv + " — token may be expired or wrong scope)"
		case 403:
			hint = " (token lacks permission for this repo or action)"
		case 404:
			hint = fmt.Sprintf(" (repo %s/%s or endpoint not found — check [remote].url and .api_base)", c.owner, c.repo)
		}
		return fmt.Errorf("%s %s: HTTP %d%s: %s", method, endpoint, resp.StatusCode, hint, snippet)
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// splitOwnerRepo parses a canonical https://host/owner/repo URL. Duplicated
// from internal/ship (unexported there) to keep the packages decoupled.
func splitOwnerRepo(repoURL string) (owner, repo string, err error) {
	u, perr := url.Parse(repoURL)
	if perr != nil || u.Host == "" {
		return "", "", fmt.Errorf("bad repo URL %q", repoURL)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("URL %q does not look like /owner/repo", repoURL)
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}
