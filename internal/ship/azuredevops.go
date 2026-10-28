package ship

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/hostallow"
	"github.com/Rivil/dross/internal/project"
	"github.com/Rivil/dross/internal/redact"
	"github.com/Rivil/dross/internal/secretscan"
)

// Azure DevOps (Azure Repos) transport.
//
// Two things set Azure DevOps apart from the other REST backends. Its
// credential is HTTP Basic with an EMPTY user and the PAT — or a bearer token
// for Entra ID — so [remote].auth_user is ignored rather than required. And
// every call names an api-version: Services takes 7.1, while a Server caps it by
// release, which is what [remote].api_version is for.
//
// The repo identity — org/collection base, project and repo — derives from
// [remote].url around its _git segment (project.ParseAzureDevOpsRemote); there
// are no identity fields of its own.

// adoDefaultAPIVersion is sent when [remote].api_version is empty.
const adoDefaultAPIVersion = "7.1"

// adoAPIVersionShape is what an api-version may look like: major.minor with an
// optional -preview[.n]. Anything else — an &, a #, a path — would be spliced
// into the query of every call, so it is refused before the token is read.
var adoAPIVersionShape = regexp.MustCompile(`^[0-9]+\.[0-9]+(-preview(\.[0-9]+)?)?$`)

// errADOSignIn is wrapped when Azure DevOps answers with a sign-in page, or a
// redirect to one, instead of an API response: the credential was not accepted.
var errADOSignIn = errors.New("azure devops answered with a sign-in page, not an API response")

// adoConn is one entry point's resolved connection: which repo, where its API
// lives, the credential, and the api-version every call carries.
type adoConn struct {
	repo    project.AzureDevOpsRemote
	apiBase string
	authEnv string
	scheme  string
	token   string
	version string
}

// adoOpenConn resolves the connection for an OpenOpts entry point. AuthUser is
// not read: Azure DevOps' Basic credential has an empty user.
func adoOpenConn(opts OpenOpts) (adoConn, error) {
	return adoConnect(opts.URL, opts.APIBase, opts.AuthEnv, opts.AuthScheme, opts.APIVersion, opts.Hosts)
}

// adoCommentConn is adoOpenConn for a CommentOpts entry point.
func adoCommentConn(opts CommentOpts) (adoConn, error) {
	return adoConnect(opts.URL, opts.APIBase, opts.AuthEnv, opts.AuthScheme, opts.APIVersion, opts.Hosts)
}

// adoConnect resolves an Azure DevOps connection. Every config check runs
// before resolveToken, and resolveToken checks the host before it reads the env
// var, so a misconfigured or refused remote never causes the token to be read.
func adoConnect(repoURL, apiBase, authEnv, scheme, apiVersion string, policy hostallow.Policy) (adoConn, error) {
	if strings.TrimSpace(apiBase) == "" {
		return adoConn{}, errors.New("azure-devops backend needs [remote].api_base (the org or collection URL)")
	}
	if strings.TrimSpace(authEnv) == "" {
		return adoConn{}, errors.New("azure-devops backend needs [remote].auth_env (the env var holding the PAT)")
	}
	version := apiVersion
	if version == "" {
		version = adoDefaultAPIVersion
	}
	if !adoAPIVersionShape.MatchString(version) {
		return adoConn{}, fmt.Errorf("[remote].api_version %q is not an Azure DevOps api-version (e.g. 7.1, 6.0, 7.1-preview.1)", apiVersion)
	}
	repo, ok := project.ParseAzureDevOpsRemote(repoURL)
	if !ok {
		return adoConn{}, fmt.Errorf("[remote].url %q is not an Azure Repos remote (.../<project>/_git/<repo>)", repoURL)
	}
	token, err := resolveToken(apiBase, authEnv, policy)
	if err != nil {
		return adoConn{}, err
	}
	return adoConn{
		repo:    repo,
		apiBase: strings.TrimRight(apiBase, "/"),
		authEnv: authEnv,
		scheme:  scheme,
		token:   token,
		version: version,
	}, nil
}

// repoAPI is the git repository endpoint every PR call hangs off.
func (c adoConn) repoAPI() string {
	return adoRepoEndpoint(c.apiBase, c.repo.Project, c.repo.Repo)
}

// adoRepoEndpoint is <api_base>/<project>/_apis/git/repositories/<repo>.
// project and repo are the DECODED names, escaped here exactly once, so a space
// is %20 (never %2520) and a # or ? cannot end the path early.
func adoRepoEndpoint(apiBase, project, repo string) string {
	return strings.TrimRight(apiBase, "/") + "/" + url.PathEscape(project) +
		"/_apis/git/repositories/" + url.PathEscape(repo)
}

// adoAuthHeader sets Azure DevOps' one credential header. bearer sends the
// token as an Entra ID bearer; every other scheme — empty, and the
// private-token and basic values doctor flags as ineffective — sends Basic with
// an empty user, the documented PAT form.
func adoAuthHeader(req *http.Request, scheme, token string) {
	if configenum.Normalize(scheme) == "bearer" {
		req.Header.Set("Authorization", "Bearer "+token)
		return
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(":"+token)))
}

// adoRequest is the only Azure DevOps HTTP entry. body is screened before it
// is encoded, the api-version is set on the query (replacing any the endpoint
// carried), and redirects are never followed: a rejected credential is answered
// with a sign-in page or a redirect to one, and the Authorization header must
// not ride along to it. A sign-in answer and any non-2xx status are errors;
// the body is scrubbed before anything can interpolate it.
func adoRequest(c adoConn, method, endpoint string, body any) ([]byte, int, error) {
	// Screened before the encoder runs (criterion c-2 of secret-detection).
	// A nil body — every GET here — passes untouched.
	if err := secretscan.ScanPayload(method+" "+endpoint, body); err != nil {
		return nil, 0, err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, 0, err
	}
	q := u.Query()
	q.Set("api-version", c.version)
	u.RawQuery = q.Encode()

	var buf io.Reader
	if body != nil {
		b := new(bytes.Buffer)
		if err := json.NewEncoder(b).Encode(body); err != nil {
			return nil, 0, err
		}
		buf = b
	}
	req, err := http.NewRequest(method, u.String(), buf)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	// Asks for a 401 instead of a 302 to the sign-in page on a bad credential.
	req.Header.Set("X-TFS-FedAuthRedirect", "Suppress")
	adoAuthHeader(req, c.scheme, c.token)

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	// Scrubbed HERE, at the one place the body enters the package, so every
	// caller's `string(respBody)` is safe by construction. Scrub also catches
	// the token inside an echoed base64(":"+token) Basic credential.
	respBody = []byte(redact.Scrub(string(respBody), c.authEnv, c.token))
	switch status := resp.StatusCode; {
	case status == http.StatusNonAuthoritativeInfo, status >= 300 && status < 400:
		return nil, status, fmt.Errorf("HTTP %d: %w — check the token in $%s and its scopes", status, errADOSignIn, c.authEnv)
	case status >= 300:
		return nil, status, fmt.Errorf("HTTP %d: %s", status, string(respBody))
	}
	return respBody, resp.StatusCode, nil
}
