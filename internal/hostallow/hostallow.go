// Package hostallow decides which hosts dross may attach a secret to.
//
// The problem it solves: `[remote].api_base` and `[board].base_url` are
// committed config, so the repo you cloned chooses them. Every authenticated
// request dross makes reads its token from the env var named by
// `[remote].auth_env` and sends it to whatever those keys point at. A repo that
// sets `api_base = "https://attacker.example"` therefore exfiltrates the
// user's forge token on the next `dross ship`, with no prompt and nothing
// unusual on screen.
//
// The allowlist is DERIVED, never configured. A `[remote].allowed_hosts` key
// would be self-authorizing — a hostile `.dross/` would simply set that too, and
// the check would gate config on config. So the allowed set is:
//
//	the authority of [remote].url   (the repo's own host: self-hosted forges
//	                                 share it between remote and api_base)
//	+ built-in known-SaaS hosts     (covers the github.com/api.github.com and
//	                                 gitlab.com/atlassian.net/youtrack splits)
//	+ Extra                         (machine-local, from the gitignored
//	                                 .dross/local.toml — see the locked
//	                                 escape_hatch decision)
//
// Extra is the only user-widened input and it cannot be cloned, which is what
// keeps the guarantee from being self-authorizing while a genuinely odd
// self-hosted install still has a door.
//
// Two properties are load-bearing and easy to lose in a refactor:
//
//   - There is no allow-all. A zero Policy resolves to the SaaS defaults, so a
//     caller that forgets to populate the field fails CLOSED. Every "unknown
//     means allow" default in a security check is eventually reached by the
//     code path nobody remembered to wire.
//   - Every refusal wraps ErrRefused. Callers must be able to tell a policy
//     refusal from a transient network error, because dross degrades gracefully
//     on the latter (mergeGate falls back to git ancestry) and must NOT degrade
//     on the former — silently downgrading under attack is the failure mode the
//     locked refusal_behaviour decision exists to prevent.
package hostallow

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
)

// ErrRefused is wrapped by every refusal Check returns. Callers distinguish a
// policy refusal from a transient failure with errors.Is.
var ErrRefused = errors.New("host not allowed")

// saasDefaults are the hosting and tracker hosts dross knows by name. They are
// here rather than in config because a built-in default cannot be rewritten by
// a repo, which is the entire point.
//
// A "*." entry matches one or more leading labels and requires the dot
// boundary: "*.atlassian.net" allows acme.atlassian.net, and refuses both
// evil-atlassian.net (no boundary) and atlassian.net.attacker.example (suffix,
// not prefix). Substring matching here would be a hole wide enough to drive any
// domain through.
var saasDefaults = []string{
	// forge / code hosting
	"github.com",
	"api.github.com",
	"gitlab.com",
	"bitbucket.org",
	"api.bitbucket.org",
	"codeberg.org",
	"dev.azure.com",       // azure devops services
	"vssps.dev.azure.com", // azure devops identities
	"*.visualstudio.com",  // legacy <org>.visualstudio.com and its vssps host
	// issue trackers
	"*.atlassian.net",   // jira cloud
	"*.youtrack.cloud",  // youtrack cloud
	"*.myjetbrains.com", // youtrack incloud
}

// Policy is the resolved allowlist for one repo. Fields only — the allowed set
// is computed on demand rather than cached at construction, so the zero value
// is the SaaS defaults instead of an empty set that would either refuse
// everything or, worse, be special-cased into allowing everything.
type Policy struct {
	// RemoteURL is [remote].url. Its authority joins the allowed set, and it
	// is named in refusals so the user can see what the set was derived from.
	RemoteURL string
	// Extra is the machine-local addition read from .dross/local.toml's
	// allow_hosts. Never read from a tracked file — see readAllowHosts.
	Extra []string
}

// Derive builds the policy for a repo. It is a constructor rather than a
// computation so that call sites read as "derive from the remote", making a
// site that passes something else — a synthetic board URL, say — visible.
func Derive(remoteURL string, extra []string) Policy {
	return Policy{RemoteURL: remoteURL, Extra: extra}
}

// Allowed returns the resolved allowlist, sorted, for display and for tests.
// Derive("", nil).Allowed() is the SaaS defaults — not everything.
func (p Policy) Allowed() []string {
	set := map[string]bool{}
	for _, h := range saasDefaults {
		// Normalized like every other entry, so the defaults become
		// port-explicit too. Comparing a bare "api.github.com" against a
		// port-explicit authority silently never matches — a whole allowlist
		// that refuses everything, which reads as "very secure" right up until
		// someone deletes the check to make dross work again.
		set[normalizeEntry(h)] = true
	}
	if a := authorityOf(p.RemoteURL); a != "" {
		set[a] = true
	}
	for _, e := range p.Extra {
		if a := normalizeEntry(e); a != "" {
			set[a] = true
		}
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// Check refuses unless rawURL's host is in the allowed set. kind names the
// setting under check ("[remote].api_base") so the refusal points at the line
// to fix; it is a label, not a role.
//
// It deliberately does NOT name a remedy command — that hint belongs to the CLI
// boundary (dross doctor), which knows how the user invokes dross. Keeping this
// package CLI-agnostic is what lets both internal/forge and internal/ship
// import it without either depending on internal/cmd.
func (p Policy) Check(kind, rawURL string) error {
	refuse := func(host, why string) error {
		return fmt.Errorf("refusing to contact host %q: %s %s (%w)", host, kind, why, ErrRefused)
	}

	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return fmt.Errorf("refusing to contact host %q: %s is not a parseable absolute URL (%w)",
			rawURL, kind, ErrRefused)
	}

	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if scheme != "https" {
		// Loopback over http is the one exception: it cannot leave the
		// machine, and refusing it would make every local-instance and
		// test-server setup unreachable. It is still subject to the allowlist
		// below, so this is not a bypass — a hostile api_base of
		// http://127.0.0.1 is refused unless the remote itself is loopback.
		if !(scheme == "http" && isLoopback(host)) {
			return refuse(host, fmt.Sprintf("uses scheme %q; a token may only be sent over https", scheme))
		}
	}

	authority := withDefaultPort(host, u.Port(), scheme)
	allowed := p.Allowed()
	for _, entry := range allowed {
		if matches(entry, authority, host) {
			return nil
		}
	}

	src := p.RemoteURL
	if src == "" {
		src = "(unset)"
	}
	return refuse(host, fmt.Sprintf(
		"resolves to %s, which is not in the allowlist derived from [remote].url %s plus the built-in known-SaaS hosts",
		authority, src))
}

// matches reports whether an allowlist entry covers an authority. Wildcard
// entries carry no port and match only the default https port, so a wildcard
// can never authorize an odd-port endpoint the user never saw.
func matches(entry, authority, host string) bool {
	if strings.HasPrefix(entry, "*.") {
		suffix := entry[1:] // ".atlassian.net"
		if !strings.HasSuffix(host, suffix) || len(host) <= len(suffix) {
			return false
		}
		return authority == host+":443"
	}
	return entry == authority
}

// authorityOf extracts a normalized host:port from a full URL. A bare host with
// no scheme is accepted too, because [remote].url is not always written as a
// URL in the wild.
func authorityOf(rawURL string) string {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		return normalizeEntry(raw)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return withDefaultPort(strings.ToLower(u.Hostname()), u.Port(), strings.ToLower(u.Scheme))
}

// normalizeEntry turns an allowlist entry — "git.corp.internal",
// "git.corp.internal:8443", "https://git.corp.internal/x", "*.atlassian.net" —
// into the form matches compares against. Wildcards pass through unchanged.
func normalizeEntry(entry string) string {
	e := strings.ToLower(strings.TrimSpace(entry))
	if e == "" {
		return ""
	}
	if strings.HasPrefix(e, "*.") {
		return e
	}
	if strings.Contains(e, "://") {
		return authorityOf(e)
	}
	e = strings.TrimSuffix(strings.SplitN(e, "/", 2)[0], ".")
	if h, p, err := net.SplitHostPort(e); err == nil {
		return h + ":" + p
	}
	return e + ":443"
}

// withDefaultPort makes the comparison port-explicit on both sides, so
// api.github.com:8443 does not match the api.github.com entry. An unexpected
// port is an unexpected endpoint even on a host the user trusts.
func withDefaultPort(host, port, scheme string) string {
	if port != "" {
		return host + ":" + port
	}
	if scheme == "http" {
		return host + ":80"
	}
	return host + ":443"
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
