package ship

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/hostallow"
	"github.com/Rivil/dross/internal/redact"
	"github.com/Rivil/dross/internal/secretscan"
)

// OpenOpts is everything OpenPR needs across providers.
type OpenOpts struct {
	Provider   string // one of configenum.ShipProviders
	URL        string // canonical https URL of the repo
	APIBase    string // forgejo/gitea/gitlab/bitbucket/azure-devops: base of the REST API; ignored for github
	AuthEnv    string // env var name holding the token; only used for forgejo/gitea/gitlab/bitbucket/azure-devops
	AuthUser   string // bitbucket: account user for HTTP Basic auth (user:token)
	AuthScheme string // gitlab: "private-token" (default) | "bearer"; azure-devops: "bearer" or Basic :PAT
	ProjectID  string // gitlab: numeric project-id override; empty = derive from URL
	APIVersion string // azure-devops: api-version sent on every call; empty = 7.1
	HeadBranch string // e.g. "pr/01-x"
	BaseBranch string // e.g. "main"
	Title      string
	Body       string
	Reviewers  []string
	Draft      bool
	PRNumber   int // GetPRStatus: the PR/MR number whose merged status to look up

	// Hosts is the API host allowlist APIBase is checked against before the
	// token is read. The zero value is not unrestricted — it resolves to
	// hostallow's SaaS defaults — so a caller that forgets it fails closed.
	Hosts hostallow.Policy
}

// OpenResult is the minimal successful response shape.
type OpenResult struct {
	Number int    // PR number on the host
	URL    string // browser URL
}

// OpenPR dispatches to the right backend based on Provider.
func OpenPR(opts OpenOpts) (*OpenResult, error) {
	switch configenum.Normalize(opts.Provider) {
	case "github":
		return openGitHubPR(opts)
	case "forgejo", "gitea":
		return openForgejoPR(opts)
	case "gitlab":
		return openGitLabPR(opts)
	case "bitbucket":
		return openBitbucketPR(opts)
	case "azure-devops":
		return openAzureDevOpsPR(opts)
	default:
		return nil, fmt.Errorf("unsupported provider %q (expected %s)", opts.Provider, configenum.ShipProviders.List())
	}
}

// OpenPRFunc is the overridable seam cmd-package callers use, and cmd-package
// tests stub, to open a PR without gh or a network — the unexported
// ghCommand seam is unreachable from package cmd. Production code calls
// OpenPRFunc, not OpenPR directly.
var OpenPRFunc = OpenPR

// --- GitHub via gh ---

// ghCommand is overridable from tests.
//
//dross:exec-exempt gh is the forge API client; every argv reaching it is built by this package and fenced by argfence, and gh runs no repo-authored line
var ghCommand = func(args ...string) *exec.Cmd { return exec.Command("gh", args...) }

// screenedGH is the only way this package reaches ghCommand. It runs the
// secret scan over the argv first (criterion c-2 of secret-detection): a
// credential in a PR body or title must never reach `gh`, and the seam stays
// a plain test double with nothing to remember. TestNoRawGhCommandCallOutsideTheSeam
// pins that no call site goes around it.
func screenedGH(args ...string) (*exec.Cmd, error) {
	if err := secretscan.ScanArgv("gh", args); err != nil {
		return nil, err
	}
	return ghCommand(args...), nil
}

// ghStderr is where a failed gh invocation's own output goes: this process's
// stderr. Tests swap it to capture what a user would see.
var ghStderr io.Writer = os.Stderr

// ghFailed reports a failed gh invocation. gh's own output — which can carry
// an API response body — goes to stderr, where the user is already looking,
// and the error names the subcommand and gh's exit status only. An error is
// never the terminal: it can reach telemetry or a persisted record.
func ghFailed(what string, err error, out []byte) error {
	if len(out) > 0 {
		fmt.Fprintf(ghStderr, "%s: gh said:\n%s\n", what, bytes.TrimRight(out, "\n"))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// ghUnparseable reports gh output that is not the JSON it promised: the raw
// output goes to stderr, the error is fixed prose.
func ghUnparseable(what string, out []byte) error {
	fmt.Fprintf(ghStderr, "%s: gh printed:\n%s\n", what, bytes.TrimRight(out, "\n"))
	return fmt.Errorf("%s: gh's output is not the JSON it promises (printed above)", what)
}

func openGitHubPR(opts OpenOpts) (*OpenResult, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, errors.New("github backend needs the `gh` CLI on PATH (https://cli.github.com)")
	}
	// Every derived value here sits in the VALUE SLOT of its own flag, which is
	// the other way to fence one: pflag consumes the argument after a string
	// flag verbatim, so a --title of "--json" is a title, not a flag. There is
	// no positional in this argv at all, which is why it carries no `--` — a
	// separator would have nothing to protect and would demote the flags dross
	// chose into positionals. The property that has to hold is that no value
	// ever escapes its slot; TestOpenPRArgvWalk asserts it by index.
	args := []string{
		"pr", "create",
		"--title", opts.Title,
		"--body", opts.Body,
		"--head", opts.HeadBranch,
		"--base", opts.BaseBranch,
	}
	if opts.Draft {
		args = append(args, "--draft")
	}
	for _, r := range opts.Reviewers {
		args = append(args, "--reviewer", r)
	}
	cmd, err := screenedGH(args...)
	if err != nil {
		return nil, err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, ghFailed("gh pr create", err, out)
	}
	// gh prints the URL on the last line; pick that to be safe.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	prURL := strings.TrimSpace(lines[len(lines)-1])
	return &OpenResult{
		Number: parsePRNumber(prURL),
		URL:    prURL,
	}, nil
}

// --- helpers ---

// parsePRNumber extracts the trailing integer from a PR URL like
// https://github.com/o/r/pull/123. Returns 0 on failure.
func parsePRNumber(url string) int {
	idx := strings.LastIndex(url, "/")
	if idx < 0 {
		return 0
	}
	tail := url[idx+1:]
	n := 0
	for _, c := range tail {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// jsonPost POSTs JSON with a token auth header. Returns parsed
// response body (or the raw bytes via "_raw") on success.
func jsonPost(endpoint, authEnv, token string, body any) (map[string]any, error) {
	// Screened before the encoder runs (criterion c-2 of secret-detection):
	// a credential in a PR body or comment must never leave the process.
	if err := secretscan.ScanPayload("POST "+endpoint, body); err != nil {
		return nil, err
	}
	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(body); err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", endpoint, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+token)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	// Scrubbed HERE, at the one place the body enters the package, rather than at
	// each Errorf that interpolates it. Every caller's `string(respBody)` is then
	// safe by construction — including the ones that are not about HTTP status at
	// all ("response missing iid"), which a per-error-site scrub would miss.
	respBody = []byte(redact.Scrub(string(respBody), authEnv, token))
	if resp.StatusCode >= 300 {
		// The one bare `HTTP %d: %s` in ship, and the one that mirrors back an
		// Authorization header this function set itself a few lines above.
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	out := map[string]any{}
	if len(respBody) > 0 {
		_ = json.Unmarshal(respBody, &out)
	}
	return out, nil
}
