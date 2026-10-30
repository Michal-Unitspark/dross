package diag

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/project"
)

// RemoteCombination reports [remote] pairings that are individually valid but
// fail once ship runs. Empty and "none" providers stay silent: they mean "this
// repo has no remote", not a misconfigured one.
func RemoteCombination(r project.Remote) []string {
	var out []string
	provider, authUser := r.Provider, r.AuthUser
	prov := configenum.Normalize(provider)
	scheme := configenum.Normalize(r.AuthScheme)
	if prov == "" || prov == "none" {
		return nil
	}

	// A provider the tooling happily writes but ship cannot dispatch: the PR
	// step is the first thing to say so, at the end of a phase.
	if !configenum.ShipProviders.Has(prov) {
		out = append(out, fmt.Sprintf("[remote].provider = %q — ship cannot open a PR for it (expected %s); /dross-ship will fail at the PR step", provider, configenum.ShipProviders.List()))
	}

	// Azure DevOps sends Basic with an empty user and the PAT, or Bearer: it
	// has its own credential shape, so the Basic user:token warnings below
	// would misdirect, and its own warnings replace them.
	if prov == "azure-devops" {
		return append(out, azureDevOpsCombination(r, scheme)...)
	}

	// Basic auth is user:token on the wire, so a missing user sends
	// base64(:token) and 401s on every call — a guaranteed ship failure that
	// nothing else surfaces until the token looks to blame.
	if (prov == "bitbucket" || scheme == "basic") && strings.TrimSpace(authUser) == "" {
		out = append(out, "[remote].auth_user is not set but the credential is HTTP Basic user:token — every ship call will 401")
	}

	// Only bitbucket dispatches Basic: gitlab falls through to PRIVATE-TOKEN
	// and github ignores the scheme entirely, so setting it elsewhere is a
	// silent no-op that reads as configured.
	if scheme == "basic" && prov != "bitbucket" {
		out = append(out, fmt.Sprintf("[remote].auth_scheme = basic but the %s backend sends no Basic credential — the setting has no effect", prov))
	}
	return out
}

// azureDevOpsCombination is RemoteCombination's azure-devops arm.
//
// Only bearer changes what is sent: private-token and basic both fall to the
// default Basic :PAT, so either reads as configured while doing nothing.
// auth_user is never sent. And a Server's api_base must be the collection its
// url names — an api_base one level short addresses no project at all.
func azureDevOpsCombination(r project.Remote, scheme string) []string {
	var out []string
	if scheme == "private-token" || scheme == "basic" {
		out = append(out, fmt.Sprintf("[remote].auth_scheme = %s has no effect on azure-devops — it always sends HTTP Basic with an empty user and the PAT unless auth_scheme = bearer", scheme))
	}
	if user := strings.TrimSpace(r.AuthUser); user != "" {
		out = append(out, fmt.Sprintf("[remote].auth_user = %q is ignored by azure-devops — the PAT goes in Basic with an empty user", user))
	}
	// The scheme is not compared: a non-https api_base is refused by the host
	// allowlist with its own reason, and blaming the collection would misdirect.
	if ado, ok := project.ParseAzureDevOpsRemote(r.URL); ok && !ado.Services && r.APIBase != "" {
		_, want, _ := strings.Cut(ado.APIBase, "://")
		_, got, _ := strings.Cut(strings.TrimRight(strings.TrimSpace(r.APIBase), "/"), "://")
		if !strings.EqualFold(got, want) {
			out = append(out, fmt.Sprintf("[remote].api_base = %s is not the collection [remote].url names (%s) — every ship call will miss the project", r.APIBase, ado.APIBase))
		}
	}
	return out
}

// SortedStateMapKeys returns the [board].state_map keys in a stable order, so
// a project.toml with several bad keys reports them the same way every run.
func SortedStateMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// BoardCombination reports [board] pairings that pass every per-field check
// and still error at the first board op.
func BoardCombination(provider, milestoneMode, authUser string) []string {
	var out []string
	prov := configenum.Normalize(provider)

	// A mode outside the provider's own accept-set. Skipped when the mode is
	// globally invalid (already a hard failure above) or when the provider maps
	// milestones by some other means and never reads the field at all.
	if configenum.MilestoneModes.Has(milestoneMode) {
		if modes := configenum.MilestoneModesFor(prov); modes != nil && !modes.Has(milestoneMode) {
			out = append(out, fmt.Sprintf("[board].milestone_mode = %q is not supported by the %s backend (expected %s) — milestone sync will error", milestoneMode, prov, modes.List()))
		}
	}

	// Jira's REST credential is Basic email:token; auth_env alone authenticates
	// nothing.
	if prov == "jira" && strings.TrimSpace(authUser) == "" {
		out = append(out, "[board].auth_user is not set but Jira authenticates as Basic email:token — board ops will 401")
	}
	return out
}

// GitVersionAtLeast compares `git --version` output against a "MAJOR.MINOR"
// floor. It parses only the two leading components: git's suffixes vary by
// platform ("2.39.5 (Apple Git-154)", "2.44.0.windows.1"), and a stricter
// parser would report a false finding on a perfectly capable git — which is how
// a version check gets deleted.
func GitVersionAtLeast(raw, floor string) bool {
	nums := func(s string) (int, int, bool) {
		fields := strings.Fields(s)
		for _, f := range fields {
			parts := strings.SplitN(f, ".", 3)
			if len(parts) < 2 {
				continue
			}
			maj, err1 := strconv.Atoi(parts[0])
			min, err2 := strconv.Atoi(parts[1])
			if err1 == nil && err2 == nil {
				return maj, min, true
			}
		}
		return 0, 0, false
	}
	fMaj, fMin, ok := nums(floor)
	if !ok {
		return true // an unparseable floor must not fail every repo
	}
	gMaj, gMin, ok := nums(raw)
	if !ok {
		return true // an unreadable version is a warning above, not a finding
	}
	return gMaj > fMaj || (gMaj == fMaj && gMin >= fMin)
}
