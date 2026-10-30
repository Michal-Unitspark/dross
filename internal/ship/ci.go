package ship

import "errors"

// CIVerdict is one read of a PR's CI at its watched head, reduced to what
// ship.md §5 acts on. The registration window is the prompt's: a read here is
// one-shot and never waits.
type CIVerdict string

const (
	CIPass    CIVerdict = "pass"    // every counted check succeeded
	CIFail    CIVerdict = "fail"    // a counted check failed; Link names it
	CIPending CIVerdict = "pending" // running, or queued — Queued says which
	CINone    CIVerdict = "none"    // nothing applicable has registered
	CIAsk     CIVerdict = "ask"     // a state dross will not read either way; Reason says which
)

// CIResult is a CI read. Link is the failing check's page on a fail. Reason is
// set whenever the verdict needs one to act on — an ask, or a pending that is
// not about CI at all (a head the PR does not carry yet). Queued tells a
// pending that has not started from one that is running.
type CIResult struct {
	Verdict CIVerdict
	Link    string
	Reason  string
	Queued  bool
}

// ErrCIUnsupported is returned for a provider whose CI dross ship ci does not
// read: its ship.md §5 recipe watches CI instead.
var ErrCIUnsupported = errors.New("dross ship ci is not supported for this provider — use the ship.md recipe")
