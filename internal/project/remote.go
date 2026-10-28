package project

import (
	"net/url"
	"strings"
)

// KnownHostProviders maps hostname → provider for hosts dross can identify
// without asking. Anything not in this list is unknown — caller should ask
// the user (likely a self-hosted instance).
var KnownHostProviders = map[string]string{
	"github.com":    "github",
	"codeberg.org":  "forgejo",
	"gitlab.com":    "gitlab",
	"bitbucket.org": "bitbucket",
}

// DetectRemote parses a git remote URL (https or ssh form) and returns a
// best-effort Remote with URL, Provider, APIBase pre-filled.
//
// Recognised provider hosts get Public=true; unknown hosts (likely
// self-hosted Forgejo / Gitea / GitLab) leave Provider="" and Public=false so
// the caller knows to prompt.
func DetectRemote(remoteURL string) Remote {
	r := Remote{}
	host, path, _ := parseGitRemote(remoteURL)
	if host == "" {
		return r
	}

	r.URL = "https://" + host + "/" + strings.TrimSuffix(path, ".git")

	if provider, ok := KnownHostProviders[host]; ok {
		r.Provider = provider
		r.Public = true
		switch provider {
		case "github":
			r.APIBase = "https://api.github.com"
		case "forgejo", "gitea":
			r.APIBase = "https://" + host + "/api/v1"
		case "gitlab":
			r.APIBase = "https://" + host + "/api/v4"
		case "bitbucket":
			r.APIBase = "https://api.bitbucket.org/2.0"
		}
	}
	return r
}

// parseGitRemote extracts host and "owner/repo" from any common git URL form:
//   - https://host/owner/repo(.git)
//   - https://user@host/owner/repo(.git)
//   - git@host:owner/repo(.git)
//   - ssh://git@host/owner/repo(.git)
//
// The path stays escaped as origin wrote it (My%20Repo, never a raw space), so
// a percent-encoded name round-trips. ssh reports the scp-like and ssh://
// forms, whose port (if any) names the ssh daemon rather than the web host.
// Returns empty strings on parse failure.
func parseGitRemote(raw string) (host, path string, ssh bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}

	scp := !strings.Contains(raw, "://") && strings.Contains(raw, "@") && strings.Contains(raw, ":")
	//dross:taint-cleared the remote with its userinfo (user:token@), the one part that carries a credential, cut off by withoutUserinfo; the raw URL stays unmarked in init.go
	rest := withoutUserinfo(raw, scp)

	// scp-like form: git@host:owner/repo
	if scp {
		colon := strings.Index(rest, ":")
		if colon < 0 {
			return "", "", false
		}
		return rest[:colon], rest[colon+1:], true
	}

	u, err := url.Parse(rest)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	return u.Host, strings.TrimPrefix(u.EscapedPath(), "/"), u.Scheme == "ssh"
}

// withoutUserinfo cuts the user[:password]@ off a git remote before anything
// parses it: a remote can carry a token there (https://user:token@host/…), and
// what is derived from it is persisted to project.toml. An scp-form remote
// loses everything up to its first @; a URL loses its authority's userinfo, up
// to the LAST @ as url.Parse reads it.
func withoutUserinfo(raw string, scp bool) string {
	if scp {
		return raw[strings.Index(raw, "@")+1:]
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return raw
	}
	authority, tail, hasTail := strings.Cut(rest, "/")
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		authority = authority[at+1:]
	}
	out := scheme + "://" + authority
	if hasTail {
		out += "/" + tail
	}
	return out
}
