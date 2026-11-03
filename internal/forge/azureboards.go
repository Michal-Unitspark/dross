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
	"regexp"
	"strings"
	"time"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/redact"
	"github.com/Rivil/dross/internal/secretscan"
)

// AzureBoardsClient talks to Azure Boards — the work-item tracking half of
// Azure DevOps Services or an Azure DevOps Server — over its REST API. It is a
// sibling of JiraClient and YouTrackClient behind the BoardClient interface:
// work items are addressed by their numeric id as a string, states are the
// project process's own names, and every call carries an api-version.
//
// The credential is HTTP Basic with an EMPTY user and the PAT, or a bearer token
// for Entra ID under [board].auth_scheme = bearer. [board].auth_user is never
// read. Every setting comes from [board]; nothing falls back to [remote]
// (locked board_config).
//
// Construct with NewAzureBoards.
type AzureBoardsClient struct {
	baseURL string // org (Services) or collection (Server) URL, no trailing slash
	project string // decoded project name; escaped exactly once per URL
	authEnv string // env var name (kept for diagnostic error messages)
	scheme  string // [board].auth_scheme; only bearer changes the header
	token   string
	version string // api-version on every call

	// areaPath and iterationPath are the full classification paths, already
	// prefixed with the project (normalizeADOPath), or "" for the defaults.
	areaPath      string
	iterationPath string

	meta adoMetaCache // process metadata, read lazily (azureboards_meta.go)

	http *http.Client
}

// adoBoardsDefaultAPIVersion is sent when [board].api_version is empty.
const adoBoardsDefaultAPIVersion = "7.1"

// adoBoardsAPIVersionShape is what an api-version may look like: major.minor
// with an optional -preview[.n]. Anything else — an &, a #, a path — would be
// spliced into the query of every call, so it is refused before the token is
// read.
var adoBoardsAPIVersionShape = regexp.MustCompile(`^[0-9]+\.[0-9]+(-preview(\.[0-9]+)?)?$`)

// adoStatusError is a non-2xx answer that is not a sign-in page. Its message
// is the status line with the scrubbed body; a caller that branches on the
// status — PathExists reads a 404 as "missing" — uses errors.As, which reaches
// it through redact.Err's wrapper.
type adoStatusError struct {
	Status int
	msg    string
}

func (e *adoStatusError) Error() string { return e.msg }

// errADOBoardsSignIn is wrapped when Azure DevOps answers with a sign-in page,
// or a redirect to one, instead of an API response: the credential was not
// accepted.
var errADOBoardsSignIn = errors.New("azure devops answered with a sign-in page, not an API response")

const (
	adoServicesHost      = "dev.azure.com"
	adoLegacySuffix      = ".visualstudio.com"
	adoDefaultCollection = "DefaultCollection"
	adoServerVDir        = "tfs"
)

// NewAzureBoards validates config, resolves the token from the environment,
// and returns a ready client. It makes no request.
//
// The order is the guarantee. The three required fields (each by name) and
// the api_version shape are checked first, then the host allowlist, then the
// base_url shape; only then is the env var read. A misconfigured or refused
// board never causes the token to be touched, so no later error path can
// print it.
func NewAzureBoards(cfg Config) (*AzureBoardsClient, error) {
	if strings.TrimSpace(cfg.APIBase) == "" {
		return nil, errors.New("azure-devops board needs [board].base_url (the org or collection URL, e.g. https://dev.azure.com/<org>)")
	}
	project := strings.TrimSpace(cfg.Project)
	if project == "" {
		return nil, errors.New("azure-devops board needs [board].project (the Azure DevOps project name)")
	}
	if strings.TrimSpace(cfg.AuthEnv) == "" {
		return nil, errors.New("azure-devops board needs [board].auth_env (the env var holding the PAT)")
	}
	version := strings.TrimSpace(cfg.APIVersion)
	if version == "" {
		version = adoBoardsDefaultAPIVersion
	}
	if !adoBoardsAPIVersionShape.MatchString(version) {
		return nil, fmt.Errorf("[board].api_version %q is not an Azure DevOps api-version (e.g. 7.1, 6.0, 7.1-preview.1)", cfg.APIVersion)
	}
	// The host check runs ahead of the shape check, so a host nobody allowed
	// is reported as refused whatever its path — a bare https://attacker.example
	// is a refusal, not a "missing collection" hint inviting the user to fix it
	// up. Both still run before the token is read.
	if err := cfg.Hosts.Check("[board].base_url", cfg.APIBase); err != nil {
		return nil, err
	}
	base, err := CheckADOBaseURL(cfg.APIBase)
	if err != nil {
		return nil, err
	}
	token := os.Getenv(cfg.AuthEnv)
	if token == "" {
		return nil, fmt.Errorf("$%s is not set; run `dross env set %s` in your shell", cfg.AuthEnv, cfg.AuthEnv)
	}
	return &AzureBoardsClient{
		baseURL:       base,
		project:       project,
		authEnv:       cfg.AuthEnv,
		scheme:        cfg.AuthScheme,
		token:         token,
		version:       version,
		areaPath:      normalizeADOPath(project, cfg.AreaPath),
		iterationPath: normalizeADOPath(project, cfg.IterationPath),
		http: &http.Client{
			Timeout: 30 * time.Second,
			// A rejected credential is answered with a redirect to a sign-in
			// page; the Authorization header must not ride along to it.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// CheckADOBaseURL validates a [board].base_url as an Azure DevOps org or
// collection URL and returns it without a trailing slash. Exported so doctor
// judges the same shape the constructor does.
//
// Services: https://dev.azure.com/<org> exactly, or the legacy
// https://<org>.visualstudio.com with an optional /DefaultCollection. Server:
// https://<host>/<collection>, or https://<host>/tfs/<collection> — a bare
// Server host has no collection and every call under it would 404. A query or
// fragment is refused: it would end the path of every call early.
func CheckADOBaseURL(raw string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("[board].base_url %q is not an absolute https URL (want https://dev.azure.com/<org> or https://<host>/<collection>)", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(trimmed, "?#") {
		return "", fmt.Errorf("[board].base_url %q carries a query or fragment; give the bare org or collection URL", raw)
	}
	var segs []string
	if p := strings.Trim(u.EscapedPath(), "/"); p != "" {
		segs = strings.Split(p, "/")
	}
	for _, s := range segs {
		dec, derr := url.PathUnescape(s)
		if derr != nil || dec == "" || dec == "." || dec == ".." {
			return "", fmt.Errorf("[board].base_url %q has an empty or dot path segment", raw)
		}
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == adoServicesHost:
		if len(segs) != 1 || strings.EqualFold(segs[0], "_apis") {
			return "", fmt.Errorf("[board].base_url %q is not an Azure DevOps Services org URL; want https://dev.azure.com/<org> (the org alone, no project)", raw)
		}
	case strings.HasSuffix(host, adoLegacySuffix):
		if len(segs) > 1 || (len(segs) == 1 && !strings.EqualFold(segs[0], adoDefaultCollection)) {
			return "", fmt.Errorf("[board].base_url %q is not an org URL; want https://<org>.visualstudio.com (optionally /DefaultCollection, no project)", raw)
		}
	default:
		need := 1
		if len(segs) > 0 && strings.EqualFold(segs[0], adoServerVDir) {
			need = 2
		}
		if len(segs) < need {
			return "", fmt.Errorf("[board].base_url %q has no collection; an Azure DevOps Server URL is https://<host>/<collection> (or https://<host>/tfs/<collection>)", raw)
		}
	}
	return trimmed, nil
}

// normalizeADOPath makes an area or iteration path absolute within project, so
// doctor's existence probe and every create send the same full path. A value
// that is the project itself, or already starts with `<project>\`, is kept as
// written (ADO compares these case-insensitively); anything else is taken as
// relative to the project and gets it prefixed: `Web` on project Fabrikam is
// `Fabrikam\Web`. Stray leading or trailing backslashes are dropped. Empty
// stays empty: the project's own default applies.
func normalizeADOPath(project, path string) string {
	p := strings.Trim(strings.TrimSpace(path), `\`)
	if p == "" {
		return ""
	}
	if strings.EqualFold(p, project) || (len(p) > len(project) && strings.EqualFold(p[:len(project)+1], project+`\`)) {
		return p
	}
	return project + `\` + p
}

// projectAPI is <base_url>/<project>/_apis<suffix>. project is the DECODED
// name, escaped here exactly once, so a space is %20 (never %2520) and a # or ?
// cannot end the path early. suffix is appended verbatim.
func (c *AzureBoardsClient) projectAPI(suffix string) string {
	return c.baseURL + "/" + url.PathEscape(c.project) + "/_apis" + suffix
}

// adoJSONPatch is a work-item write body. doRaw sends it as
// application/json-patch+json — the only content type the work-item
// create/update endpoints accept — and every other body as application/json.
type adoJSONPatch []adoPatchOp

// adoPatchOp is one JSON Patch operation on a work-item field or relation.
type adoPatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// do performs one authenticated request with the credential removed from
// whatever error comes back. The wrap sits around the whole request, so the
// transport error, the status error and the decode error are all covered.
func (c *AzureBoardsClient) do(method, endpoint string, body, out any) error {
	return redact.Err(c.doRaw(method, endpoint, body, out), c.authEnv, c.token)
}

// doRaw is the unredacted request. Nothing outside do may call it.
//
// The body is screened before it is encoded; the api-version is set on the
// query, replacing any the endpoint carried, so exactly one goes out; the body
// that comes back is scrubbed before anything can interpolate it; and a
// sign-in answer — a 203 or any redirect, which is never followed — is an error
// naming the env var to check.
func (c *AzureBoardsClient) doRaw(method, endpoint string, body, out any) error {
	// Screened before any bytes are encoded or a request is built (criterion
	// c-2 of secret-detection). A nil body — every GET — passes untouched.
	if err := secretscan.ScanPayload(method+" "+endpoint, body); err != nil {
		return err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("api-version", c.version)
	u.RawQuery = q.Encode()

	var rdr io.Reader
	if body != nil {
		buf := new(bytes.Buffer)
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return err
		}
		rdr = buf
	}
	req, err := http.NewRequest(method, u.String(), rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		ct := "application/json"
		if _, ok := body.(adoJSONPatch); ok {
			ct = "application/json-patch+json"
		}
		req.Header.Set("Content-Type", ct)
	}
	// Asks for a 401 instead of a 302 to the sign-in page on a bad credential.
	req.Header.Set("X-TFS-FedAuthRedirect", "Suppress")
	// bearer sends an Entra ID token; every other scheme — empty, and the
	// private-token and basic values doctor flags as ineffective — sends Basic
	// with an empty user, the documented PAT form. auth_user is never read.
	if configenum.Normalize(c.scheme) == "bearer" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	} else {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(":"+c.token)))
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, u.Redacted(), err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	// Scrubbed HERE, where the body enters the package, so every use of it
	// below is safe by construction — including an echoed base64(":"+token).
	// Two scrubs, by use: the text an error quotes names the env var to check;
	// the bytes decoded into values get the bare marker, so no config value
	// (the env var's name) can ride a decoded field into a path a caller opens.
	respBody := []byte(redact.Scrub(string(raw), c.authEnv, c.token))
	decodeBody := []byte(redact.Scrub(string(raw), "", c.token))

	switch status := resp.StatusCode; {
	case status == http.StatusNonAuthoritativeInfo, status >= 300 && status < 400:
		return fmt.Errorf("%s %s: HTTP %d: %w — check the token in $%s and its scopes", method, u.Path, status, errADOBoardsSignIn, c.authEnv)
	case status >= 300:
		snippet := string(respBody)
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		hint := ""
		switch status {
		case http.StatusUnauthorized:
			hint = " (check $" + c.authEnv + " — the PAT may be expired or lack the Work Items scope)"
		case http.StatusForbidden:
			hint = " (the token lacks permission for this project or action)"
		case http.StatusNotFound:
			hint = fmt.Sprintf(" (project %q or endpoint not found — check [board].base_url and [board].project)", c.project)
		}
		return &adoStatusError{Status: status, msg: fmt.Sprintf("%s %s: HTTP %d%s: %s", method, u.Path, status, hint, snippet)}
	}
	if out != nil && len(decodeBody) > 0 {
		if err := json.Unmarshal(decodeBody, out); err != nil {
			return fmt.Errorf("decode %s response: %w", u.Path, err)
		}
	}
	return nil
}
