package secretscan

import (
	"fmt"
	"strings"
)

// The transport registry: every function in internal/ship and internal/forge
// that puts bytes on the wire, and what its disposition toward the secret
// scanner is (criterion c-4 of secret-detection).
//
// Mirrors internal/toolfence and internal/pathfence deliberately: registry
// plus residual, judged in both directions by a walker in internal/cmd. A
// function that calls an outbound primitive (http.NewRequest*, http.Post,
// http.Get, (*http.Client).Do, exec.Command, or a registered seam) with no
// entry here is an undeclared transport; an entry naming a function that no
// longer exists is a stale declaration. The walker lives in internal/cmd for
// the same reason the other two do — it must parse the transport packages,
// which import this one.
//
// THE SCOPE IS OUTBOUND PUBLISH TRANSPORTS: the bodies dross composes and
// sends to a forge or board. Reads are declared too — as ReadOnly — so that a
// GET helper that grows a body is caught by the arm that checks its method
// literal, rather than by nobody.

// Dispositions. Exactly one is non-nil on a well-formed entry.
type (
	// Screened marks a transport that runs the scanner over its payload
	// BEFORE the request is built. Call names which scanner entry point —
	// ScanPayload for a JSON body, ScanArgv for a subprocess argv — and the
	// walker requires that call to precede the primitive in source order.
	Screened struct {
		Call string
	}

	// ReadOnly marks a transport that can carry no composed body: its
	// http.NewRequest call has a literal "GET" method and a nil body. Why
	// records the claim so it can be judged rather than trusted.
	ReadOnly struct {
		Why string
	}

	// Seam marks the bare primitive a test double replaces — a package var
	// bound to a func literal, with no screen of its own. ScreenedCaller
	// names the ONE Screened function allowed to call it; every other call
	// to the seam identifier is a bypass.
	Seam struct {
		ScreenedCaller string
	}
)

// Transport is one declared outbound seam.
type Transport struct {
	Package string // "ship" | "forge"
	Func    string // "jsonPost", "Client.doRaw" (receiver without the star), "ghCommand" (a package var)

	Screened *Screened
	ReadOnly *ReadOnly
	Seam     *Seam
}

// Name is the "pkg.Func" key used for dedupe, cross-reference and reporting.
func (t Transport) Name() string { return t.Package + "." + t.Func }

// Transports returns the registry. The walker judges it in both directions.
func Transports() []Transport { return append([]Transport(nil), transports...) }

// ScreenCalls are the scanner entry points a Screened entry may name.
var ScreenCalls = map[string]bool{"ScanPayload": true, "ScanArgv": true}

// Validate reports every way in which a registry is malformed.
//
// It takes the slice rather than reading the package var so a test can feed it
// synthetic bad entries; asserting only that the real registry is clean would
// pass against a Validate that checked nothing.
func Validate(in []Transport) []error {
	var errs []error
	seen := map[string]bool{}
	screened := map[string]bool{}
	for _, t := range in {
		if t.Screened != nil {
			screened[t.Name()] = true
		}
	}

	for _, t := range in {
		name := t.Name()
		if strings.TrimSpace(t.Package) == "" {
			errs = append(errs, fmt.Errorf("entry %q: empty Package", name))
		}
		if strings.TrimSpace(t.Func) == "" {
			errs = append(errs, fmt.Errorf("entry %q: empty Func", name))
		}

		set := 0
		for _, on := range []bool{t.Screened != nil, t.ReadOnly != nil, t.Seam != nil} {
			if on {
				set++
			}
		}
		switch {
		case set == 0:
			errs = append(errs, fmt.Errorf("entry %q: no disposition — every transport is screened, read-only, or a seam behind a screened caller", name))
		case set > 1:
			errs = append(errs, fmt.Errorf("entry %q: %d dispositions set, want exactly one", name, set))
		case t.Screened != nil:
			if !ScreenCalls[t.Screened.Call] {
				errs = append(errs, fmt.Errorf("entry %q: screened with Call %q — must name ScanPayload or ScanArgv", name, t.Screened.Call))
			}
		case t.ReadOnly != nil:
			if strings.TrimSpace(t.ReadOnly.Why) == "" {
				errs = append(errs, fmt.Errorf("entry %q: read-only with no Why — an unfalsifiable claim", name))
			}
		case t.Seam != nil:
			switch caller := strings.TrimSpace(t.Seam.ScreenedCaller); {
			case caller == "":
				errs = append(errs, fmt.Errorf("entry %q: seam with no ScreenedCaller — then nothing pins who may reach it", name))
			case !screened[caller]:
				errs = append(errs, fmt.Errorf("entry %q: seam names ScreenedCaller %q, which is not a Screened entry", name, caller))
			}
		}

		if seen[name] {
			errs = append(errs, fmt.Errorf("entry %q: declared twice", name))
		}
		seen[name] = true
	}
	return errs
}

var transports = []Transport{
	// ---- forge: one doRaw per backend; every publish method funnels here --
	{Package: "forge", Func: "Client.doRaw", Screened: &Screened{Call: "ScanPayload"}},
	{Package: "forge", Func: "GitHubClient.doRaw", Screened: &Screened{Call: "ScanPayload"}},
	{Package: "forge", Func: "JiraClient.doRaw", Screened: &Screened{Call: "ScanPayload"}},
	{Package: "forge", Func: "YouTrackClient.doRaw", Screened: &Screened{Call: "ScanPayload"}},

	// ---- ship: REST providers ------------------------------------------
	{Package: "ship", Func: "jsonPost", Screened: &Screened{Call: "ScanPayload"}},
	{Package: "ship", Func: "bbRequest", Screened: &Screened{Call: "ScanPayload"}},
	{Package: "ship", Func: "gitlabReq", Screened: &Screened{Call: "ScanPayload"}},
	{Package: "ship", Func: "adoRequest", Screened: &Screened{Call: "ScanPayload"}},
	{Package: "ship", Func: "jsonGet", ReadOnly: &ReadOnly{
		Why: "Forgejo/Gitea list-pulls lookup: method is the literal \"GET\" and the body is nil, so no composed text can travel in it",
	}},

	// ---- ship: GitHub via gh -------------------------------------------
	// screenedGH is the one screened route to the gh argv; ghCommand is the
	// bare exec primitive bound to a package var so tests can stub it.
	{Package: "ship", Func: "screenedGH", Screened: &Screened{Call: "ScanArgv"}},
	{Package: "ship", Func: "ghCommand", Seam: &Seam{ScreenedCaller: "ship.screenedGH"}},
}
