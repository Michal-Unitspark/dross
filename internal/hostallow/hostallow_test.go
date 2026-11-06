package hostallow

import (
	"errors"
	"strings"
	"testing"
)

const remote = "https://github.com/Rivil/dross"

// TestCheckRefusesOffAllowlistHost is the criterion in one assertion: a
// committed api_base pointing somewhere the derivation never reached gets a
// refusal, and the refusal names the host so the user can see what the repo
// tried to do.
func TestCheckRefusesOffAllowlistHost(t *testing.T) {
	p := Derive(remote, nil)
	err := p.Check("[remote].api_base", "https://attacker.example")
	if err == nil {
		t.Fatal("off-allowlist host was allowed")
	}
	if !strings.Contains(err.Error(), "refusing to contact host") {
		t.Errorf("refusal does not carry the pinned prefix: %v", err)
	}
	if !strings.Contains(err.Error(), "attacker.example") {
		t.Errorf("refusal does not name the host: %v", err)
	}
	if !strings.Contains(err.Error(), "[remote].api_base") {
		t.Errorf("refusal does not name the setting: %v", err)
	}
}

// TestCheckRefusalIsErrRefused pins the sentinel across every refusal path.
// This is what lets mergeGate tell "the repo is attacking you" apart from "the
// forge is down" — the first must surface, the second may degrade to git
// ancestry. Collapse them and an active attack looks like a flaky network.
func TestCheckRefusalIsErrRefused(t *testing.T) {
	p := Derive(remote, nil)
	for _, tc := range []struct {
		name string
		url  string
	}{
		{"off allowlist", "https://attacker.example"},
		{"plain http", "http://github.com"},
		{"odd port", "https://api.github.com:8443"},
		{"unparseable", "://nonsense"},
		{"empty", ""},
		{"no host", "https:///path"},
		{"wildcard near miss", "https://evil-atlassian.net"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := p.Check("[remote].api_base", tc.url)
			if err == nil {
				t.Fatalf("Check(%q) = nil, want a refusal", tc.url)
			}
			if !errors.Is(err, ErrRefused) {
				t.Errorf("refusal does not wrap ErrRefused: %v", err)
			}
		})
	}
}

// TestSuffixWildcardIsNotSubstring is the hole a strings.HasSuffix shortcut
// opens. "*.atlassian.net" must require the dot boundary and must anchor at the
// end of the host, or an attacker registers evil-atlassian.net (or points
// atlassian.net.attacker.example at themselves) and inherits every Jira token
// dross holds.
func TestSuffixWildcardIsNotSubstring(t *testing.T) {
	p := Derive(remote, nil)
	if err := p.Check("[board].base_url", "https://acme.atlassian.net"); err != nil {
		t.Errorf("a real Jira Cloud tenant must be allowed: %v", err)
	}
	for _, host := range []string{
		"https://evil-atlassian.net",
		"https://atlassian.net.attacker.example",
		"https://atlassian.net",
		"https://xatlassian.net",
		"https://acme.atlassian.net.attacker.example",
	} {
		if err := p.Check("[board].base_url", host); err == nil {
			t.Errorf("wildcard matched %s — the dot boundary is not being enforced", host)
		}
	}
}

// TestADOBuiltinHosts: Azure DevOps Services, its identities host and every
// legacy <org>.visualstudio.com are known by name under a remote that derives
// none of them. The wildcard keeps its dot boundary and its https-on-443 pin,
// and dev.azure.com is an exact host, not a wildcard.
func TestADOBuiltinHosts(t *testing.T) {
	p := Derive(remote, nil)
	for _, u := range []string{
		"https://dev.azure.com/o",
		"https://vssps.dev.azure.com/o",
		"https://o.visualstudio.com",
		"https://o.vssps.visualstudio.com",
	} {
		if err := p.Check("[remote].api_base", u); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	for _, u := range []string{
		"https://evil-visualstudio.com",
		"https://visualstudio.com.attacker.example",
		"https://visualstudio.com",
		"https://x.dev.azure.com",
		"https://o.visualstudio.com:8443",
		"http://dev.azure.com",
	} {
		if err := p.Check("[remote].api_base", u); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: want ErrRefused, got %v", u, err)
		}
	}
}

// TestCheckAllowsRemoteHost is the over-refusal gate: a self-hosted install
// where the remote and the API share a host must keep working with no Extra at
// all, or the derivation costs honest repos something and gets turned off.
func TestCheckAllowsRemoteHost(t *testing.T) {
	p := Derive("https://git.corp.internal/team/app", nil)
	for _, u := range []string{
		"https://git.corp.internal/api/v1",
		"https://git.corp.internal",
		"https://git.corp.internal:443/api/v4",
	} {
		if err := p.Check("[remote].api_base", u); err != nil {
			t.Errorf("self-hosted pair refused: %s: %v", u, err)
		}
	}
	// And the github.com / api.github.com split the SaaS defaults exist for.
	gh := Derive(remote, nil)
	if err := gh.Check("[remote].api_base", "https://api.github.com"); err != nil {
		t.Errorf("api.github.com refused under a github.com remote: %v", err)
	}
}

// TestCheckRefusesSchemeAndPort covers the two ways an allowed *name* is not an
// allowed *endpoint*: cleartext, which leaks the token to anyone on the path
// regardless of who is listening at the other end, and an unexpected port,
// which is a different service.
func TestCheckRefusesSchemeAndPort(t *testing.T) {
	p := Derive(remote, nil)
	if err := p.Check("[remote].api_base", "http://api.github.com"); err == nil {
		t.Error("http on an allowed host must be refused — the token would go in cleartext")
	}
	if err := p.Check("[remote].api_base", "https://api.github.com:8443"); err == nil {
		t.Error("an unexpected port on an allowed host must be refused")
	}
	// Loopback is the deliberate exception: it cannot leave the machine, and
	// refusing it would make local instances unreachable. It is still gated by
	// the allowlist, so it is an exception to the scheme rule, not to the
	// policy.
	if err := p.Check("[remote].api_base", "http://127.0.0.1:9999"); err == nil {
		t.Error("loopback must still be subject to the allowlist")
	}
	local := Derive("http://127.0.0.1:9999/team/app", nil)
	if err := local.Check("[remote].api_base", "http://127.0.0.1:9999/api/v1"); err != nil {
		t.Errorf("a loopback remote must allow its own loopback API base: %v", err)
	}
}

// TestDeriveEmptyIsNotAllowAll is the fail-closed gate. A caller that forgets
// to populate Hosts gets the SaaS defaults, not a pass — because a zero value
// that means "unrestricted" is reached by exactly the code path nobody
// remembered to wire up.
func TestDeriveEmptyIsNotAllowAll(t *testing.T) {
	empty := Derive("", nil)
	if err := empty.Check("[remote].api_base", "https://attacker.example"); err == nil {
		t.Fatal("Derive(\"\", nil) allowed an arbitrary host")
	}
	var zero Policy
	if err := zero.Check("[remote].api_base", "https://attacker.example"); err == nil {
		t.Fatal("the zero Policy allowed an arbitrary host")
	}
	// It is the SaaS defaults, not an empty set: an empty set would refuse
	// github.com too, and get worked around rather than fixed.
	if err := empty.Check("[remote].api_base", "https://api.github.com"); err != nil {
		t.Errorf("Derive(\"\", nil) must still carry the SaaS defaults: %v", err)
	}
	if got := empty.Allowed(); len(got) != len(saasDefaults) {
		t.Errorf("Allowed() = %v, want exactly the %d SaaS defaults", got, len(saasDefaults))
	}
}

// TestExtraWidensTheSet covers the escape hatch from the locked escape_hatch
// decision. Extra comes from the gitignored, machine-local local.toml, which a
// hostile repo cannot ship — that is what stops the allowlist from being
// self-authorizing.
func TestExtraWidensTheSet(t *testing.T) {
	p := Derive(remote, []string{"git.corp.internal", "odd.example:8443", "https://third.example/x"})
	for _, u := range []string{
		"https://git.corp.internal/api/v1",
		"https://odd.example:8443/api",
		"https://third.example",
	} {
		if err := p.Check("[remote].api_base", u); err != nil {
			t.Errorf("Extra entry not honoured for %s: %v", u, err)
		}
	}
	// Widened, not opened: an unlisted host is still refused, and an Extra
	// entry pinned to a port does not authorize the same host on another one.
	if err := p.Check("[remote].api_base", "https://attacker.example"); err == nil {
		t.Error("Extra must widen the set, not disable it")
	}
	if err := p.Check("[remote].api_base", "https://odd.example"); err == nil {
		t.Error("a port-pinned Extra entry must not authorize the default port")
	}
}

// TestHTTPDefaultsToPort80 drives withDefaultPort's http arm, which the rest of
// the suite never reaches: Check refuses non-https before normalizing, so the
// only live route to it is the loopback exception and an allowlist entry written
// as an http:// URL. Left dark, a mutant collapsing that arm into :443 survives —
// and the damage is not theoretical. Both sides normalize, so an entry meaning
// "the local instance on :80" would start comparing equal to :443 and quietly
// authorize an endpoint the user never named.
//
// Everything here goes through the public Check/Allowed rather than
// withDefaultPort directly, so the branch is proven reachable from the API
// callers actually use.
func TestHTTPDefaultsToPort80(t *testing.T) {
	// An http remote must normalize port-explicitly to :80, not :443.
	p := Derive("http://localhost/team/app", nil)
	found := false
	for _, a := range p.Allowed() {
		if a == "localhost:80" {
			found = true
		}
	}
	if !found {
		t.Fatalf("an http remote did not normalize to localhost:80: %v", p.Allowed())
	}
	if err := p.Check("[remote].api_base", "http://localhost/api/v1"); err != nil {
		t.Errorf("an http loopback API base on the http remote's own host must be allowed: %v", err)
	}
	// The half that makes the arm load-bearing: :80 and :443 are different
	// endpoints, so trusting one must not carry over to the other. Without the
	// distinct default the https twin would ride in on the http entry.
	if err := p.Check("[remote].api_base", "https://localhost/api/v1"); err == nil {
		t.Error("an http-derived entry authorized its https twin — the default ports collapsed")
	}

	// And the reverse direction, so a mutant that flipped the arm the other way
	// (defaulting https to :80) is caught too.
	s := Derive("https://localhost/team/app", nil)
	if err := s.Check("[remote].api_base", "http://localhost/api/v1"); err == nil {
		t.Error("an https-derived entry authorized its http twin — the default ports collapsed")
	}

	// An http:// entry supplied through the machine-local escape hatch takes the
	// same arm via normalizeEntry, which is the other live route to it.
	e := Derive(remote, []string{"http://127.0.0.1"})
	if err := e.Check("[remote].api_base", "http://127.0.0.1/api/v1"); err != nil {
		t.Errorf("an http:// allow_hosts entry did not authorize its own endpoint: %v", err)
	}
	if err := e.Check("[remote].api_base", "http://127.0.0.1:8080/api/v1"); err == nil {
		t.Error("an unported http entry authorized an explicit odd port")
	}
}
