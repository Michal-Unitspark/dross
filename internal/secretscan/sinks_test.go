package secretscan

import (
	"regexp"
	"strings"
	"testing"
)

// TestTransportRegistryValidate feeds Validate synthetic malformed entries and
// requires one specific error for each; the real registry must produce none.
func TestTransportRegistryValidate(t *testing.T) {
	if errs := Validate(Transports()); len(errs) != 0 {
		t.Fatalf("the real registry is malformed: %v", errs)
	}

	cases := []struct {
		name string
		in   []Transport
		want string
	}{
		{"no disposition", []Transport{{Package: "ship", Func: "x"}}, "no disposition"},
		{"two dispositions", []Transport{{Package: "ship", Func: "x", Screened: &Screened{Call: "ScanPayload"}, ReadOnly: &ReadOnly{Why: "w"}}}, "2 dispositions set"},
		{"empty Func", []Transport{{Package: "ship", Screened: &Screened{Call: "ScanPayload"}}}, "empty Func"},
		{"empty Package", []Transport{{Func: "x", Screened: &Screened{Call: "ScanPayload"}}}, "empty Package"},
		{"bad screen call", []Transport{{Package: "ship", Func: "x", Screened: &Screened{Call: "Scan"}}}, "must name ScanPayload or ScanArgv"},
		{"empty ReadOnly.Why", []Transport{{Package: "ship", Func: "x", ReadOnly: &ReadOnly{}}}, "read-only with no Why"},
		{"empty Seam.ScreenedCaller", []Transport{{Package: "ship", Func: "x", Seam: &Seam{}}}, "seam with no ScreenedCaller"},
		{"seam caller not screened", []Transport{
			{Package: "ship", Func: "x", Seam: &Seam{ScreenedCaller: "ship.y"}},
			{Package: "ship", Func: "y", ReadOnly: &ReadOnly{Why: "w"}},
		}, "is not a Screened entry"},
		{"duplicate", []Transport{
			{Package: "ship", Func: "x", Screened: &Screened{Call: "ScanPayload"}},
			{Package: "ship", Func: "x", Screened: &Screened{Call: "ScanPayload"}},
		}, "declared twice"},
	}
	for _, c := range cases {
		errs := Validate(c.in)
		if len(errs) != 1 {
			t.Errorf("%s: want exactly one error, got %d: %v", c.name, len(errs), errs)
			continue
		}
		if !strings.Contains(errs[0].Error(), c.want) {
			t.Errorf("%s: error %q does not mention %q", c.name, errs[0], c.want)
		}
	}
}

// TestPublishSinkFuncsAreQualified pins the registry's shape so it cannot
// silently shrink or drift: exactly eleven entries, every one over {ship, forge},
// every Func a bare name or Recv.Method.
func TestPublishSinkFuncsAreQualified(t *testing.T) {
	ts := Transports()
	if len(ts) != 11 {
		t.Fatalf("registry holds %d transports, want 11", len(ts))
	}
	funcShape := regexp.MustCompile(`^[A-Za-z_]\w*(\.[A-Za-z_]\w*)?$`)
	pkgs := map[string]int{}
	for _, tr := range ts {
		pkgs[tr.Package]++
		if !funcShape.MatchString(tr.Func) {
			t.Errorf("%s: Func %q is not pkg-relative func, Recv.Method or var", tr.Name(), tr.Func)
		}
		if strings.Contains(tr.Func, "(") || strings.Contains(tr.Func, "*") {
			t.Errorf("%s: Func carries receiver syntax; write Recv.Method without the star", tr.Name())
		}
	}
	if len(pkgs) != 2 || pkgs["ship"] == 0 || pkgs["forge"] == 0 {
		t.Errorf("packages covered = %v, want exactly {ship, forge}", pkgs)
	}
	if pkgs["forge"] != 4 {
		t.Errorf("forge declares %d transports, want the 4 backend doRaw methods", pkgs["forge"])
	}
}
