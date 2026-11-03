package cmd

import (
	"path/filepath"
	"time"

	"github.com/Rivil/dross/internal/boardsync"
	"github.com/Rivil/dross/internal/forge"
	"github.com/Rivil/dross/internal/prtriage"
	"github.com/Rivil/dross/internal/render"
	"github.com/Rivil/dross/internal/ship"
	"github.com/Rivil/dross/internal/watch"
	"github.com/spf13/cobra"
)

// watchDigest is the read-only snapshot `dross watch` emits: board issues split
// into new-since-last-tick vs carried, the drifting phases, and the single
// suggested next command. board_ok is false when the board was off, unreachable,
// or misconfigured (the digest is drift-only in that case).
type watchDigest struct {
	New       []watch.Item       `json:"new"`
	Current   []watch.Item       `json:"current"`
	Drift     []watch.PhaseDrift `json:"drift"`
	Suggested string             `json:"suggested_command"`
	BoardOK   bool               `json:"board_ok"`
	// Stranded is the count of board mirrors whose artefact finished but whose
	// card did not — the other half of the locked prompt_edge decision, which
	// keeps the sweep off every prompt's hot path and surfaces the debt as a
	// drift signal instead.
	//
	// omitempty on purpose: zero is not a finding, and a digest printed on a
	// loop should stay quiet about a board that is clean. It is also omitted
	// when the board was not reached, where a zero would read as "clean"
	// rather than "unknown".
	Stranded int `json:"stranded,omitempty"`
	// BotPRs and ShipPRs are the forge's open PRs: bot-authored ones, and ones
	// whose head is a dross phase or milestone branch. Information only — they
	// never reach suggestedCommand (pr_suggestion) and are never persisted.
	//
	// Pointers, so the digest can say three things rather than two: nil is
	// omitted (the forge could not be asked — unknown), and a pointer to an
	// empty list prints [] (asked, and none is open).
	BotPRs  *[]watch.BotPR  `json:"bot_prs,omitempty"`
	ShipPRs *[]watch.ShipPR `json:"ship_prs,omitempty"`
}

// Watch is the read-only `dross watch` command: it surfaces what changed on the
// board and in the phase spine since the last tick and never mutates anything
// but .dross/watch.state.json. Designed to be run on a `/loop` interval.
func Watch() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "watch",
		Short: "Read-only digest of board inbound, phase drift and open bot/ship PRs since the last tick",
		RunE: func(_ *cobra.Command, _ []string) error {
			dstate, statePath, err := loadState()
			if err != nil {
				return err
			}
			root := filepath.Dir(statePath)

			// Board inbound feed (read-only, mark-free). Board disabled,
			// misconfigured, or unreachable all degrade to a drift-only digest —
			// watch must never error out on a loop.
			var (
				feed         []watch.Item
				boardReached bool
				stranded     int
			)
			if ctx, enabled, oerr := openBoard(); oerr == nil && enabled {
				if issues, lerr := boardsync.CollectInbound(ctx, forge.IssueFilter{State: "open"}); lerr == nil {
					boardReached = true
					for _, iss := range issues {
						feed = append(feed, watch.Item{ID: iss.Key, State: iss.State, Title: iss.Title})
					}
				}
				// Read-only, and degrading the same way the feed does: a
				// classify that fails leaves the count at zero and the line
				// unprinted rather than failing the tick. watch runs on a
				// timer; it must never be the thing that breaks.
				if plan, _, cerr := boardsync.Inventory(ctx, nil); cerr == nil {
					stranded = len(plan.Cards)
				}
			}

			wPath := watch.FilePath(root)
			wst, err := watch.Load(wPath)
			if err != nil {
				return err
			}
			diff := wst.Diff(feed)
			if diff.New == nil {
				diff.New = []watch.Item{}
			}
			if diff.Current == nil {
				diff.Current = []watch.Item{}
			}

			drift, err := watch.ClassifyDrift(root, dstate)
			if err != nil {
				return err
			}
			if drift == nil {
				drift = []watch.PhaseDrift{}
			}

			digest := watchDigest{
				New:       diff.New,
				Current:   diff.Current,
				Drift:     drift,
				Suggested: suggestedCommand(drift, len(diff.New), reconcilableCount(root)),
				BoardOK:   boardReached,
				Stranded:  stranded,
			}
			digest.BotPRs, digest.ShipPRs = openPRDigest()

			// Persist the seen-set ONLY when the board was actually reached, so an
			// off/unreachable tick preserves the prior baseline instead of
			// re-flagging the whole backlog as new on the next healthy tick.
			if boardReached {
				wst.Update(feed)
				if err := wst.Save(wPath); err != nil {
					return err
				}
			}

			if asJSON {
				out, err := render.MarshalJSON(digest)
				if err != nil {
					return err
				}
				Print(string(out))
				return nil
			}
			renderWatchHuman(digest)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit the digest as JSON (for /dross-watch)")
	return c
}

// openPRDigest asks the forge for its open PRs and splits them for the digest.
// The lister gets the full [remote] opts — host policy, api_base, credential
// env, api_version — since a REST forge (Azure DevOps) needs every one of
// them. Any reason the answer is unknown — no [remote] provider or url, a
// project that will not load, a host policy that will not resolve, an
// unsupported provider, gh missing, unauthenticated or offline, a failed or
// sign-in answer, a page too full to trust — returns (nil, nil), so both
// fields are omitted rather than printed empty. watch runs on a timer; the
// forge is one more thing that must never fail the tick.
func openPRDigest() (*[]watch.BotPR, *[]watch.ShipPR) {
	p, _, err := loadProject()
	if err != nil || p.Remote.Provider == "" || p.Remote.URL == "" {
		return nil, nil
	}
	root, err := FindRoot()
	if err != nil {
		return nil, nil
	}
	repoDir := filepath.Dir(root)
	hosts, err := remotePolicy(root, repoDir, p)
	if err != nil {
		return nil, nil
	}
	opts := buildOpenOpts(p, hosts)
	prs, err := ship.ListOpenPRsFunc(opts)
	if err != nil {
		return nil, nil
	}
	bots, ships := watch.SplitPRs(prs, time.Now())
	countUntriaged(opts, repoDir, prs, ships)
	return &bots, &ships
}

// countUntriaged sets each phase ship PR's untriaged review-comment count —
// prtriage.Pending over the PR's comments and its phase's record — so its
// line can point at /dross-respond. A head that does not map to a phase
// (prtriage.ParsePhaseHead), a record that cannot be read, a caller or
// comment fetch that fails: each leaves that PR's count at zero, shown as no
// count at all, and never fails the tick. The caller is looked up once per
// tick, and only when there is a phase PR to count.
func countUntriaged(opts ship.OpenOpts, repoDir string, prs []ship.OpenPRRecord, ships []watch.ShipPR) {
	cross := make(map[int]bool, len(prs))
	for _, pr := range prs {
		cross[pr.Number] = pr.IsCrossRepository
	}
	var self *ship.Account
	for i := range ships {
		id, err := prtriage.ParsePhaseHead(ships[i].Head, cross[ships[i].Number])
		if err != nil {
			continue
		}
		if self == nil {
			acct, err := ship.AuthenticatedUserFunc(opts)
			if err != nil {
				return
			}
			self = &acct
		}
		rec, known := prtriage.LoadForPhase(repoDir, id)
		if !known {
			continue
		}
		comments, err := ship.ListPRCommentsFunc(opts, ships[i].Number)
		if err != nil {
			continue
		}
		ships[i].Untriaged = len(prtriage.Pending(prtriage.Items(comments, *self), rec))
	}
}

// suggestedCommand ranks the single next command per the locked
// suggestion_precedence decision: advance an in-flight phase (verify a
// complete-but-unverified phase, then ship a verified-but-unshipped one) before
// pulling in new board intake, and fall back to status when nothing presses.
// Returns exactly one command, never empty.
func suggestedCommand(drift []watch.PhaseDrift, newCount, reconcilable int) string {
	var hasComplete, hasVerified bool
	for _, d := range drift {
		switch d.Kind {
		case watch.DriftCompleteUnverified:
			hasComplete = true
		case watch.DriftVerifiedUnshipped:
			hasVerified = true
		}
	}
	switch {
	case hasComplete:
		return "/dross-verify"
	case hasVerified:
		return "/dross-ship"
	case reconcilable > 1:
		// Finished work whose branches are still lying around. It outranks new
		// board intake — cleanup that is already earned beats work not started
		// — and it is named as ONE verb rather than leaked as a chore list.
		return "dross phase reconcile"
	case newCount > 0:
		return "/dross-inbox"
	default:
		return "/dross-status"
	}
}

func renderWatchHuman(d watchDigest) {
	Printf("watch — %d new, %d carried, %d phase(s) drifting\n", len(d.New), len(d.Current), len(d.Drift))
	if !d.BoardOK {
		Print("  board: off/unreachable — drift only\n")
	}
	if d.Stranded > 0 {
		Printf("  stranded: %d board mirror(s) whose artefact finished\n", d.Stranded)
	}
	for _, it := range d.New {
		Printf("  new:   %s %s\n", it.ID, it.Title)
	}
	for _, dr := range d.Drift {
		Printf("  drift: %s (%s)\n", dr.Phase, dr.Kind)
	}
	// PR titles and authors are never printed: the bot line is a count, and a
	// ship line names only number, head and checks.
	if d.BotPRs != nil {
		if line := watch.BotSummary(*d.BotPRs); line != "" {
			Printf("  %s\n", line)
		}
	}
	if d.ShipPRs != nil {
		for _, pr := range *d.ShipPRs {
			Printf("  %s\n", watch.ShipPRLine(pr))
		}
	}
	Printf("  next:  %s\n", d.Suggested)
}
