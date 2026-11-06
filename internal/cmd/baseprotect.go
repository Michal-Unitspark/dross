package cmd

import (
	"fmt"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/project"
	"github.com/Rivil/dross/internal/protect"
	"github.com/Rivil/dross/internal/ship"
)

// baseProtection is whether origin's base refuses direct pushes, as the one
// forge-aware read routeBaseChores routes on.
type baseProtection struct {
	// Known: the read succeeded. A read that failed is never "unprotected".
	Known     bool
	Protected bool
	// Reason and Fix say why an unknown read is unknown and what to do.
	Reason, Fix string
}

// protectionReadable reports whether dross reads push protection from the
// forge provider names.
func protectionReadable(provider string) bool {
	switch configenum.Normalize(provider) {
	case "github", "azure-devops":
		return true
	}
	return false
}

// readBaseProtection is the protection dispatch: github reads the branch's
// rules (BranchRulesFunc) and asks protect.RequiresPR; azure-devops reads its
// branch policies (ship.ADOBranchProtectionFunc — any enabled blocking policy
// scoped to the branch refuses a direct push); any other forge is unchecked.
func readBaseProtection(p *project.Project, opts ship.OpenOpts, base string) baseProtection {
	switch configenum.Normalize(p.Remote.Provider) {
	case "github":
		res := ship.BranchRulesFunc(opts, base)
		return baseProtection{
			Known:     res.Known,
			Protected: res.Known && protect.RequiresPR(res.Rules),
			Reason:    res.Reason,
			Fix:       "run `gh auth login`, or check [remote] in .dross/project.toml",
		}
	case "azure-devops":
		res := ship.ADOBranchProtectionFunc(opts, base)
		pat := "set [remote].auth_env to the env var holding a PAT"
		if env := p.Remote.AuthEnv; env != "" {
			pat = fmt.Sprintf("check the PAT in $%s ([remote].auth_env)", env)
		}
		return baseProtection{
			Known:     res.Known,
			Protected: res.Known && res.Protected,
			Reason:    res.Reason,
			Fix:       pat + " — it needs Code (Read) to list branch policies — or [remote] in .dross/project.toml",
		}
	}
	// A forge dross reads no protection from: callers gate on
	// protectionReadable first, so this zero value — unknown — is never
	// routed on; were it, unknown refuses the push rather than guessing.
	return baseProtection{}
}
