package project

import (
	"net/url"
	"strings"
)

const (
	adoServicesHost  = "dev.azure.com"
	adoSSHHost       = "ssh.dev.azure.com"
	adoLegacySSHHost = "vs-ssh.visualstudio.com"
	adoLegacySuffix  = ".visualstudio.com"

	// Collection markers: as the first segment before _git — tfs on a Server,
	// DefaultCollection on <org>.visualstudio.com — the name is base, never
	// the project (adoBaseDepth).
	adoServerVDir        = "tfs"
	adoDefaultCollection = "DefaultCollection"
)

// AzureDevOpsRemote is an Azure Repos remote split into what every Azure DevOps
// call is addressed by. None of it is a config field of its own: it all derives
// from the remote url around its _git segment (the repo_identity lock).
type AzureDevOpsRemote struct {
	// URL is the canonical https remote, https://<host>[/<base>]/<project>/_git/<repo>,
	// on origin's own web host: an ssh form maps to its https twin, and a legacy
	// <org>.visualstudio.com is never rewritten to dev.azure.com.
	URL string
	// APIBase is the org (Services) or collection (Server) URL, https://<host>[/<base>].
	APIBase string
	// Base is the escaped path between the host and the project: the org on
	// dev.azure.com, the collection path on a Server, and empty on
	// <org>.visualstudio.com, whose org is the host's first label.
	Base string
	// Project and Repo are the decoded names ("My Project", not My%20Project).
	Project, Repo string
	// Services is true on Azure DevOps Services (dev.azure.com and
	// <org>.visualstudio.com) and false on an Azure DevOps Server.
	Services bool
}

// ParseAzureDevOpsRemote parses any Azure Repos remote form — https (with or
// without user@), ssh v3 on ssh.dev.azure.com and vs-ssh.visualstudio.com, and
// a Server's https or ssh://host:port remote — into its canonical identity.
//
// Everything before <project>/_git/<repo> is the org/collection base. A single
// segment before _git is the base alone, with repo == project. On
// <org>.visualstudio.com the org is the host's label, so the base is counted
// after it. A Server's leading /tfs and a lone DefaultCollection on
// <org>.visualstudio.com mark a collection, never a project (adoBaseDepth).
// Percent-encoded names survive: segments are decoded once into Project/Repo
// and escaped once into URL/APIBase.
//
// ok is false for anything that is not that shape — no _git, an empty or dot
// segment, a Services remote with no org — so a caller never sees half a parse.
func ParseAzureDevOpsRemote(raw string) (AzureDevOpsRemote, bool) {
	host, path, ssh := parseGitRemote(raw)
	name, port := splitPort(strings.ToLower(host))
	if name == "" {
		return AzureDevOpsRemote{}, false
	}
	if ssh {
		// An ssh port names the ssh daemon, never the web host.
		port = ""
	}
	segs, ok := adoSegments(strings.TrimSuffix(path, ".git"))
	if !ok {
		return AzureDevOpsRemote{}, false
	}

	var base []string
	var project, repo string
	switch name {
	case adoSSHHost, adoLegacySSHHost:
		// v3/<org>/<project>/<repo>
		if len(segs) != 4 || !strings.EqualFold(segs[0], "v3") {
			return AzureDevOpsRemote{}, false
		}
		org := segs[1]
		project, repo = segs[2], segs[3]
		if name == adoSSHHost {
			name, base = adoServicesHost, []string{org}
		} else {
			name = strings.ToLower(org) + adoLegacySuffix
			if visualStudioOrg(name) == "" {
				return AzureDevOpsRemote{}, false
			}
		}
	default:
		i := len(segs) - 2
		if i < 0 || !strings.EqualFold(segs[i], "_git") {
			return AzureDevOpsRemote{}, false
		}
		pre := segs[:i]
		for _, s := range pre {
			if strings.EqualFold(s, "_git") {
				return AzureDevOpsRemote{}, false
			}
		}
		repo, project = segs[i+1], segs[i+1]
		orgHost := visualStudioOrg(name) != ""
		depth := adoBaseDepth(name, pre)
		if len(pre) > depth {
			project, pre = pre[len(pre)-1], pre[:len(pre)-1]
		}
		base = pre
		switch {
		case name == adoServicesHost && len(base) != 1, // dev.azure.com/<org>, exactly
			orgHost && len(base) > 1, // <org>.visualstudio.com[/DefaultCollection]
			len(base) < depth:        // a Server always has a collection, /tfs/<coll> under /tfs
			return AzureDevOpsRemote{}, false
		}
	}

	escaped := make([]string, len(base))
	for i, s := range base {
		escaped[i] = url.PathEscape(s)
	}
	r := AzureDevOpsRemote{
		Base:     strings.Join(escaped, "/"),
		Project:  project,
		Repo:     repo,
		Services: azureServicesHost(name),
	}
	if port != "" {
		name += ":" + port
	}
	r.APIBase = "https://" + name
	if r.Base != "" {
		r.APIBase += "/" + r.Base
	}
	r.URL = r.APIBase + "/" + url.PathEscape(project) + "/_git/" + url.PathEscape(repo)
	return r, true
}

// adoBaseDepth is how many leading segments of pre (the path before _git) are
// always base, never the project: none on <org>.visualstudio.com, whose org is
// the host's label, unless the first is DefaultCollection; two on a Server
// whose path starts at /tfs, which needs the collection after it; one — the
// org, or a Server's collection — otherwise. host is lowercase, without a port.
func adoBaseDepth(host string, pre []string) int {
	first := ""
	if len(pre) > 0 {
		first = pre[0]
	}
	switch {
	case visualStudioOrg(host) != "":
		if strings.EqualFold(first, adoDefaultCollection) {
			return 1
		}
		return 0
	case host != adoServicesHost && strings.EqualFold(first, adoServerVDir):
		return 2
	}
	return 1
}

// adoSegments splits an escaped remote path and decodes each segment once. An
// empty, . or .. segment (escaped or not) refuses the whole path: it would
// address something other than the repo once it is joined into an API URL.
func adoSegments(path string) ([]string, bool) {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		dec, err := url.PathUnescape(s)
		if err != nil || dec == "" || dec == "." || dec == ".." {
			return nil, false
		}
		segs[i] = dec
	}
	return segs, true
}

// azureServicesHost reports whether host (lowercase, no port) is an Azure
// DevOps Services web host: dev.azure.com, or <org>.visualstudio.com.
func azureServicesHost(host string) bool {
	return host == adoServicesHost || visualStudioOrg(host) != ""
}

// visualStudioOrg returns the org of a legacy <org>.visualstudio.com host, or ""
// for any other host. The org must be exactly one DNS label, so neither
// evil-visualstudio.com nor visualstudio.com.evil.example carries one.
func visualStudioOrg(host string) string {
	org, ok := strings.CutSuffix(host, adoLegacySuffix)
	if !ok || !isDNSLabel(org) {
		return ""
	}
	return org
}

// isDNSLabel reports whether s is one lowercase hostname label.
func isDNSLabel(s string) bool {
	if s == "" || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// splitPort splits a trailing :port off an authority's host. A bracketed IPv6
// literal keeps its inner colons.
func splitPort(host string) (name, port string) {
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
		return host[:i], host[i+1:]
	}
	return host, ""
}
