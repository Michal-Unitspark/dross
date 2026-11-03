package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Rivil/dross/internal/configenum"
)

// CommentKind is where on a PR a comment was left.
type CommentKind string

const (
	CommentConversation CommentKind = "conversation" // the PR's own discussion
	CommentInline       CommentKind = "inline"       // anchored to a file and line
	CommentReview       CommentKind = "review"       // a review's summary text
)

// AuthorClass is whether the forge says a comment's author is a bot. Unknown is
// a forge that does not say, never a guess from the login.
type AuthorClass string

const (
	AuthorBot     AuthorClass = "bot"
	AuthorHuman   AuthorClass = "human"
	AuthorUnknown AuthorClass = "unknown"
)

// CommentAuthor is who wrote a comment. ID is the forge's account id, the one
// identity that cannot be copied by picking the same display name.
type CommentAuthor struct {
	ID    string
	Login string
	Bot   AuthorClass
}

// PRComment is one comment on a PR. ID is the forge's own id, unique only
// within its Kind. Path and Line are set for an inline comment. Body is
// untrusted third-party text.
type PRComment struct {
	ID     string
	Kind   CommentKind
	Author CommentAuthor
	Path   string
	Line   int
	URL    string
	Body   string
}

// PRHead is a PR's head branch and state. CrossRepo is a head that lives in
// another repository, whose branch name says nothing about this repo's phases.
type PRHead struct {
	Ref       string
	CrossRepo bool
	Open      bool
	URL       string
}

// Account is the account the forge credentials authenticate as.
type Account struct {
	ID    string
	Login string
}

// ListPRComments reads every conversation comment, inline comment and review
// summary on PR n. A failure is always (nil, err), never the part that was read.
func ListPRComments(opts OpenOpts, n int) ([]PRComment, error) {
	if n <= 0 {
		return nil, fmt.Errorf("PR number %d is not a valid number", n)
	}
	switch configenum.Normalize(opts.Provider) {
	case "github":
		return gitHubPRComments(opts, n)
	case "forgejo", "gitea":
		return forgejoPRComments(opts, n)
	case "bitbucket":
		return bitbucketPRComments(opts, n)
	case "gitlab":
		return gitlabPRComments(opts, n)
	case "azure-devops":
		return azureDevOpsPRComments(opts, n)
	default:
		return nil, fmt.Errorf("unsupported provider %q (expected %s)", opts.Provider, configenum.ShipProviders.List())
	}
}

// PRHeadOf reads PR n's head branch, whether it is cross-repo, and whether it
// is still open.
func PRHeadOf(opts OpenOpts, n int) (PRHead, error) {
	if n <= 0 {
		return PRHead{}, fmt.Errorf("PR number %d is not a valid number", n)
	}
	switch configenum.Normalize(opts.Provider) {
	case "github":
		return gitHubPRHead(opts, n)
	case "forgejo", "gitea":
		return forgejoPRHead(opts, n)
	case "bitbucket":
		return bitbucketPRHead(opts, n)
	case "gitlab":
		return gitlabPRHead(opts, n)
	case "azure-devops":
		return azureDevOpsPRHead(opts, n)
	default:
		return PRHead{}, fmt.Errorf("unsupported provider %q (expected %s)", opts.Provider, configenum.ShipProviders.List())
	}
}

// AuthenticatedUser reads the account the forge credentials act as — the
// author of anything dross posts.
func AuthenticatedUser(opts OpenOpts) (Account, error) {
	switch configenum.Normalize(opts.Provider) {
	case "github":
		return gitHubAuthenticatedUser(opts)
	case "forgejo", "gitea":
		return forgejoAuthenticatedUser(opts)
	case "bitbucket":
		return bitbucketAuthenticatedUser(opts)
	case "gitlab":
		return gitlabAuthenticatedUser(opts)
	case "azure-devops":
		return azureDevOpsAuthenticatedUser(opts)
	default:
		return Account{}, fmt.Errorf("unsupported provider %q (expected %s)", opts.Provider, configenum.ShipProviders.List())
	}
}

// ListPRCommentsFunc, PRHeadOfFunc and AuthenticatedUserFunc are the exported,
// overridable seams cmd-package callers use (and cmd-package tests stub) to
// read a PR thread without a forge — the unexported ghCommand seam is
// unreachable from package cmd. Production code calls these, not the functions.
var (
	ListPRCommentsFunc    = ListPRComments
	PRHeadOfFunc          = PRHeadOf
	AuthenticatedUserFunc = AuthenticatedUser
)

// prThreadPageSize is the per_page every list read asks for. A page shorter
// than it is the last one.
const prThreadPageSize = 100

// prThreadMaxPages bounds a list read. A run of this many full pages may not be
// the end, and is refused rather than returned as if it were. Tests lower it.
var prThreadMaxPages = 10

// ghUser is an account as GitHub's REST API reports it. Type is GitHub's own
// "Bot" / "User" flag.
type ghUser struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
}

// ghThreadRecord is one record from any of the three thread lists — an issue
// comment, a review comment or a review. Each list fills the fields it has.
type ghThreadRecord struct {
	ID           int64   `json:"id"`
	User         *ghUser `json:"user"`
	Body         string  `json:"body"`
	HTMLURL      string  `json:"html_url"`
	Path         string  `json:"path"`
	Line         *int    `json:"line"`
	OriginalLine *int    `json:"original_line"`
	State        string  `json:"state"`
}

// ghPull is the part of pulls/{n} the head binding reads.
type ghPull struct {
	State   string    `json:"state"`
	HTMLURL string    `json:"html_url"`
	Head    ghPullEnd `json:"head"`
	Base    ghPullEnd `json:"base"`
}

// ghPullEnd is a PR's head or base. Repo is null when the head's fork is gone.
type ghPullEnd struct {
	Ref  string      `json:"ref"`
	Repo *ghRepoName `json:"repo"`
}

type ghRepoName struct {
	FullName string `json:"full_name"`
}

// ghGetJSON GETs a GitHub API endpoint and decodes the answer into a T. A
// failure is ghAPI's fixed prose, or fixed prose of its own for an answer
// that is not JSON — never gh's output.
//
// The decoded record is cleared here, once, for every reader downstream: the
// exec-taint engine follows a field's taint program-wide, so a prtriage reader
// of PRComment.Body or CommentAuthor.Login would otherwise carry gh's stream
// into a digest, a writer or an error.
func ghGetJSON[T any](repo ghRepo, endpoint string) (T, error) {
	var zero T
	out, err := ghAPI(repo, "GET", endpoint, nil)
	if err != nil {
		return zero, err
	}
	var raw T
	if json.Unmarshal(out, &raw) != nil {
		return zero, errors.New("GitHub's answer is not the JSON it promises")
	}
	//dross:taint-cleared a decoded GitHub REST record: ids, logins, states, urls and comment text are forge data, not gh's prose; comment bodies stay untrusted and prtriage redacts and fences them before anything prints them
	decoded := raw
	return decoded, nil
}

// ghListAll reads every page of a GitHub list endpoint.
func ghListAll[T any](repo ghRepo, endpoint string) ([]T, error) {
	var all []T
	for page := 1; page <= prThreadMaxPages; page++ {
		batch, err := ghGetJSON[[]T](repo, fmt.Sprintf("%s?per_page=%d&page=%d", endpoint, prThreadPageSize, page))
		if err != nil {
			return nil, err
		}
		if batch == nil {
			return nil, errors.New("GitHub's answer is not the JSON list it promises")
		}
		all = append(all, batch...)
		if len(batch) < prThreadPageSize {
			return all, nil
		}
	}
	return nil, fmt.Errorf("still full after %d pages of %d, so the list may be truncated", prThreadMaxPages, prThreadPageSize)
}

func gitHubPRComments(opts OpenOpts, n int) ([]PRComment, error) {
	repo, err := githubRepo(opts)
	if err != nil {
		return nil, err
	}
	pr := repo.path + "/issues/" + strconv.Itoa(n)
	pull := repo.path + "/pulls/" + strconv.Itoa(n)
	lists := []struct {
		kind     CommentKind
		what     string
		endpoint string
	}{
		{CommentConversation, "conversation comments", pr + "/comments"},
		{CommentInline, "inline comments", pull + "/comments"},
		{CommentReview, "reviews", pull + "/reviews"},
	}
	var out []PRComment
	for _, l := range lists {
		records, err := ghListAll[ghThreadRecord](repo, l.endpoint)
		if err != nil {
			return nil, fmt.Errorf("read PR #%d %s: %w", n, l.what, err)
		}
		for _, r := range records {
			c, keep, err := gitHubComment(l.kind, r)
			if err != nil {
				return nil, fmt.Errorf("read PR #%d %s: %w", n, l.what, err)
			}
			if keep {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// gitHubComment maps one record of a kind's list. A review is kept only when
// it was submitted and says something: an approval with no text is a status,
// not a comment, and a PENDING review is the reviewer's unsent draft.
func gitHubComment(kind CommentKind, r ghThreadRecord) (PRComment, bool, error) {
	if r.ID <= 0 {
		return PRComment{}, false, errors.New("a record carries no id")
	}
	if kind == CommentReview && (r.State == "PENDING" || strings.TrimSpace(r.Body) == "") {
		return PRComment{}, false, nil
	}
	c := PRComment{
		ID:     strconv.FormatInt(r.ID, 10),
		Kind:   kind,
		Author: gitHubAuthor(r.User),
		URL:    r.HTMLURL,
		Body:   r.Body,
	}
	if kind == CommentInline {
		c.Path = r.Path
		switch {
		case r.Line != nil:
			c.Line = *r.Line
		case r.OriginalLine != nil:
			// An outdated comment: the line it was left on no longer exists in
			// the diff, so GitHub nulls line and keeps original_line.
			c.Line = *r.OriginalLine
		}
	}
	return c, true, nil
}

// gitHubAuthor reads the bot flag from GitHub's account type, never the login:
// `renovate[bot]` is a name anyone can register on a User account.
func gitHubAuthor(u *ghUser) CommentAuthor {
	if u == nil {
		return CommentAuthor{Bot: AuthorUnknown}
	}
	a := CommentAuthor{Login: u.Login, Bot: AuthorUnknown}
	if u.ID > 0 {
		a.ID = strconv.FormatInt(u.ID, 10)
	}
	switch u.Type {
	case "Bot":
		a.Bot = AuthorBot
	case "User":
		a.Bot = AuthorHuman
	}
	return a
}

func gitHubPRHead(opts OpenOpts, n int) (PRHead, error) {
	repo, err := githubRepo(opts)
	if err != nil {
		return PRHead{}, err
	}
	pull, err := ghGetJSON[ghPull](repo, repo.path+"/pulls/"+strconv.Itoa(n))
	if err != nil {
		return PRHead{}, fmt.Errorf("read PR #%d: %w", n, err)
	}
	if pull.Head.Ref == "" {
		return PRHead{}, fmt.Errorf("read PR #%d: GitHub's answer names no head branch", n)
	}
	return PRHead{
		Ref:       pull.Head.Ref,
		CrossRepo: !sameGitHubRepo(pull.Head.Repo, pull.Base.Repo),
		Open:      pull.State == "open",
		URL:       pull.HTMLURL,
	}, nil
}

// sameGitHubRepo is a head and base in one repository. A head whose fork was
// deleted has no repo, and is not this one.
func sameGitHubRepo(head, base *ghRepoName) bool {
	return head != nil && base != nil && head.FullName != "" && strings.EqualFold(head.FullName, base.FullName)
}

func gitHubAuthenticatedUser(opts OpenOpts) (Account, error) {
	repo, err := githubRepo(opts)
	if err != nil {
		return Account{}, err
	}
	u, err := ghGetJSON[ghUser](repo, "user")
	if err != nil {
		return Account{}, fmt.Errorf("read the authenticated account: %w", err)
	}
	if u.Login == "" || u.ID <= 0 {
		return Account{}, errors.New("read the authenticated account: GitHub's answer names no account")
	}
	return Account{ID: strconv.FormatInt(u.ID, 10), Login: u.Login}, nil
}
