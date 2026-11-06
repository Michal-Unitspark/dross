package cmd

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/Rivil/dross/internal/boardsync"
	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/forge"
	"github.com/Rivil/dross/internal/hostallow"
	"github.com/Rivil/dross/internal/localstore"
	"github.com/Rivil/dross/internal/project"
)

// doctor's [board] checks for provider = azure-devops (azure-boards-backend
// c-1, c-9). They replace the generic auth_env / base_url pair for this
// provider and add what only Azure Boards needs: project, the org or
// collection URL shape, the api_version and auth_scheme it reads, and — when
// area_path or iteration_path is set — a read-only probe that the path exists.
//
// Order is the security property: the host is checked against the allowlist
// before $auth_env is read, and a refused host is never sent the token or
// even has it read, so a project.toml pointing [board] at a host nobody
// allowed cannot get doctor to touch the credential.

// adoDoctorGetenv is the env read these checks make. A seam, so a test can
// prove a refused host leaves $auth_env unread.
var adoDoctorGetenv = os.Getenv

// adoBoardAPIVersionShape is the shape forge.NewAzureBoards accepts for
// [board].api_version: major.minor with an optional -preview[.n]. It is
// matched against the trimmed value, as forge does, so doctor and the client
// agree on every spelling.
var adoBoardAPIVersionShape = regexp.MustCompile(`^[0-9]+\.[0-9]+(-preview(\.[0-9]+)?)?$`)

// isADOBoard reports whether [board].provider selects Azure Boards. It lives
// here, not in doctor.go: doctor.go keys no branch on a provider name (its
// remote comparison goes by URL shape — TestDoctorCarriesNoProviderLiterals),
// and the board dispatch is this file's business.
func isADOBoard(provider string) bool {
	return configenum.Normalize(provider) == "azure-devops"
}

// adoBoardChecks is what checkADOBoard found: issues fail doctor, warnings
// are counted toward its tally.
type adoBoardChecks struct {
	issues, warnings int
}

// checkADOBoard prints the azure-devops [board] lines and returns their tally.
func checkADOBoard(p *project.Project, root, repoDir string) adoBoardChecks {
	var r adoBoardChecks
	b := p.Board
	fail := func(format string, args ...any) {
		Printf("  ✗ "+format+"\n", args...)
		r.issues++
	}

	if b.BaseURL == "" {
		fail("[board].base_url is not set (the org or collection URL, e.g. https://dev.azure.com/<org>)")
	}
	if b.Project == "" {
		fail("[board].project is not set (the Azure DevOps project name)")
	}
	if b.AuthEnv == "" {
		fail("[board].auth_env is not set (the env var holding the PAT or token)")
	}

	extra, err := localstore.ReadAllowHosts(root, repoDir)
	if err != nil {
		fail("cannot read allow_hosts: %v", err)
	}
	policy := hostallow.Derive(p.Remote.URL, extra)
	refused := false
	if b.BaseURL != "" {
		if err := policy.Check("[board].base_url", b.BaseURL); err != nil {
			refused = true
			unread := ""
			if b.AuthEnv != "" {
				unread = fmt.Sprintf(" — $%s was not read", b.AuthEnv)
			}
			fail("%v%s; if the host is yours, add it with `dross local set allow_hosts <host>`", err, unread)
		} else if _, err := forge.CheckADOBaseURL(b.BaseURL); err != nil {
			fail("%v", err)
		}
	}
	if b.AuthEnv != "" && !refused {
		if adoDoctorGetenv(b.AuthEnv) == "" {
			fail("[board].auth_env = %s but $%s is not set in this shell", b.AuthEnv, b.AuthEnv)
		}
	}

	if v := strings.TrimSpace(string(b.APIVersion)); v != "" && !adoBoardAPIVersionShape.MatchString(v) {
		fail("[board].api_version = %q is not an Azure DevOps api-version (e.g. 7.1, 6.0, 7.1-preview.1)", v)
	}
	if !configenum.AuthSchemes.Has(b.AuthScheme) {
		fail("[board].auth_scheme = %q is invalid (expected %s)", b.AuthScheme, configenum.AuthSchemes.List())
	}

	if r.issues == 0 && (b.AreaPath != "" || b.IterationPath != "") {
		r.warnings += probeADOPaths(p, extra, &r)
	}
	return r
}

// probeADOPaths reads each configured classification path back from the
// project: absent is an issue (every create would be refused with it), present
// a ✓, and an answer that cannot say either way — a 500, a sign-in page — a
// warning, never a verdict. It returns the warnings it printed.
func probeADOPaths(p *project.Project, extra []string, r *adoBoardChecks) int {
	b := p.Board
	client, err := forge.NewBoard(boardsync.Config(b, p.Remote.URL, extra))
	if err != nil {
		Printf("  ⚠ could not verify [board].area_path / iteration_path: %v\n", err)
		return 1
	}
	ado, ok := client.(*forge.AzureBoardsClient)
	if !ok {
		return 0 // unreachable: provider is azure-devops
	}
	warnings := 0
	for _, c := range []struct {
		key, path string
		kind      forge.ADOPathKind
	}{
		{"area_path", b.AreaPath, forge.ADOAreaPath},
		{"iteration_path", b.IterationPath, forge.ADOIterationPath},
	} {
		if c.path == "" {
			continue
		}
		exists, err := ado.PathExists(c.kind, c.path)
		switch {
		case err != nil:
			Printf("  ⚠ could not verify [board].%s = %q in project %s: %v\n", c.key, c.path, b.Project, err)
			warnings++
		case !exists:
			Printf("  ✗ [board].%s = %q does not exist in project %s — every work item create would be refused\n", c.key, c.path, b.Project)
			r.issues++
		default:
			Printf("  ✓ [board].%s = %q exists in project %s\n", c.key, c.path, b.Project)
		}
	}
	return warnings
}
