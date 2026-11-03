package cmd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// doctor's azure-devops [board] arm (azure-boards-backend c-1, c-9).

const adoDoctorPATEnv = "DROSS_TEST_ADO_DOCTOR_PAT"

// adoDoctorRun is a dross repo with an azure-devops [board] — the Services
// org URL, project Fabrikam, a set token — with fields applied over it (""
// deletes one) and before run ahead of doctor, which it then runs.
func adoDoctorRun(t *testing.T, fields map[string]string, before func(dir string)) (string, error) {
	t.Helper()
	dir := t.TempDir()
	gitInit(t, dir, "https://gitlab.com/Rivil/dross.git")
	chdir(t, dir)
	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}
	all := map[string]string{
		"board.provider": "azure-devops",
		"board.base_url": "https://dev.azure.com/acme",
		"board.auth_env": adoDoctorPATEnv,
		"board.project":  "Fabrikam",
		"board.enabled":  "true",
	}
	for k, v := range fields {
		if v == "" {
			delete(all, k)
			continue
		}
		all[k] = v
	}
	for k, v := range all {
		setProjectFieldEvenIfInvalid(t, dir, k, v)
	}
	t.Setenv(adoDoctorPATEnv, "secret")
	if before != nil {
		before(dir)
	}
	var out string
	err := runCmdCapturing(t, &out, Doctor())
	return out, err
}

// boardLines is the Board: section of doctor's output.
func boardLines(out string) string {
	i := strings.Index(out, "Board:")
	if i < 0 {
		return ""
	}
	rest := out[i:]
	if j := strings.Index(rest, "\n\n"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// noNetwork fails any request made through http.DefaultTransport — which the
// Azure Boards client uses — and counts them.
func noNetwork(t *testing.T) *int {
	t.Helper()
	var mu sync.Mutex
	n := 0
	prev := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		n++
		mu.Unlock()
		return nil, errors.New("no network in this test: " + r.URL.String())
	})
	t.Cleanup(func() { http.DefaultTransport = prev })
	return &n
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestDoctorADOBoardNamesMissingFields: each missing required field is its
// own ✗ line and its own issue.
func TestDoctorADOBoardNamesMissingFields(t *testing.T) {
	out, err := adoDoctorRun(t, map[string]string{"board.base_url": "", "board.project": "", "board.auth_env": ""}, nil)
	board := boardLines(out)
	for _, field := range []string{"[board].base_url is not set", "[board].project is not set", "[board].auth_env is not set"} {
		if !strings.Contains(board, "✗ "+field) {
			t.Errorf("no ✗ line for %s:\n%s", field, board)
		}
	}
	if n := strings.Count(board, "✗"); n != 3 {
		t.Errorf("%d ✗ lines, want 3:\n%s", n, board)
	}
	if err == nil || !strings.Contains(err.Error(), "3 project-level issue(s)") {
		t.Errorf("err = %v; want 3 issues and a non-zero exit", err)
	}
}

// TestDoctorADORefusedHostSkipsToken: a base_url on a host nobody allowed is
// refused before $auth_env is read, and nothing is sent anywhere.
func TestDoctorADORefusedHostSkipsToken(t *testing.T) {
	requests := noNetwork(t)
	const unset = "DROSS_TEST_ADO_UNSET_PAT"
	reads := 0
	prev := adoDoctorGetenv
	adoDoctorGetenv = func(k string) string {
		if k == unset {
			reads++
		}
		return prev(k)
	}
	t.Cleanup(func() { adoDoctorGetenv = prev })

	out, err := adoDoctorRun(t, map[string]string{
		"board.base_url": "https://attacker.example", "board.auth_env": unset, "board.area_path": `Fabrikam\Web`,
	}, nil)
	board := boardLines(out)
	if !strings.Contains(board, "✗") || !strings.Contains(board, "attacker.example") {
		t.Errorf("no ✗ naming attacker.example:\n%s", board)
	}
	if strings.Contains(out, "is not set in this shell") {
		t.Errorf("the token was looked up for a refused host:\n%s", board)
	}
	if reads != 0 {
		t.Errorf("$%s was read %d times for a refused host", unset, reads)
	}
	if *requests != 0 {
		t.Errorf("%d HTTP requests for a refused host", *requests)
	}
	if err == nil {
		t.Error("a refused board host passed doctor")
	}
}

// TestDoctorADOBoardURLForms: every URL form Azure DevOps uses passes on an
// allowed host; a project URL in base_url is refused naming the org form.
func TestDoctorADOBoardURLForms(t *testing.T) {
	noNetwork(t)
	for _, c := range []struct {
		name, url string
		before    func(dir string)
	}{
		{"services", "https://dev.azure.com/acme", nil},
		{"legacy", "https://acme.visualstudio.com", nil},
		{"server via allow_hosts", "https://tfs.corp.example/tfs/DefaultCollection", func(string) {
			if err := runCmd(t, Local(), "set", "allow_hosts", "tfs.corp.example"); err != nil {
				t.Fatal(err)
			}
		}},
		{"server via the remote url", "https://tfs.corp.example/tfs/DefaultCollection", func(dir string) {
			setProjectFieldEvenIfInvalid(t, dir, "remote.url", "https://tfs.corp.example/tfs/DefaultCollection/Fabrikam/_git/r")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, _ := adoDoctorRun(t, map[string]string{"board.base_url": c.url}, c.before)
			if want := "✓ [board] is well-formed (azure-devops @ " + c.url + ")"; !strings.Contains(out, want) {
				t.Errorf("want %q:\n%s", want, boardLines(out))
			}
		})
	}
	t.Run("a project URL", func(t *testing.T) {
		out, err := adoDoctorRun(t, map[string]string{"board.base_url": "https://dev.azure.com/acme/Proj"}, nil)
		board := boardLines(out)
		if !strings.Contains(board, "✗") || !strings.Contains(board, "https://dev.azure.com/<org>") {
			t.Errorf("want a ✗ naming the <org> form:\n%s", board)
		}
		if err == nil {
			t.Error("a project URL in base_url passed doctor")
		}
	})
}

// TestDoctorADOBoardKeyValues: a malformed api_version or auth_scheme is an
// issue; the combinations t-11 warns about stay warnings.
func TestDoctorADOBoardKeyValues(t *testing.T) {
	noNetwork(t)
	for key, val := range map[string]string{"board.api_version": "7.1&x=1", "board.auth_scheme": "token"} {
		t.Run(key, func(t *testing.T) {
			out, err := adoDoctorRun(t, map[string]string{key: val}, nil)
			if !strings.Contains(boardLines(out), "✗") || !strings.Contains(boardLines(out), val) {
				t.Errorf("no ✗ naming %q:\n%s", val, boardLines(out))
			}
			if err == nil {
				t.Errorf("%s = %q passed doctor", key, val)
			}
		})
	}
	for _, c := range []struct{ key, val, warning string }{
		{"board.auth_scheme", "private-token", "[board].auth_scheme = private-token has no effect on azure-devops"},
		{"board.auth_user", "me@example.com", `[board].auth_user = "me@example.com" is ignored by azure-devops`},
		{"board.milestone_mode", "agile", `[board].milestone_mode = "agile" is ignored by azure-devops`},
	} {
		t.Run(c.key+" warns", func(t *testing.T) {
			out, err := adoDoctorRun(t, map[string]string{c.key: c.val}, nil)
			if err != nil {
				t.Errorf("%s = %q failed doctor: %v\n%s", c.key, c.val, err, out)
			}
			if !strings.Contains(out, "⚠ "+c.warning) || strings.Contains(boardLines(out), "✗") {
				t.Errorf("%s = %q: want the warning %q and no ✗:\n%s", c.key, c.val, c.warning, out)
			}
		})
	}
	t.Run("a padded api_version passes, as the client trims it", func(t *testing.T) {
		out, err := adoDoctorRun(t, map[string]string{"board.api_version": " 7.1 "}, nil)
		if err != nil || strings.Contains(boardLines(out), "api_version") {
			t.Errorf("err = %v; a padded api_version the client accepts was flagged:\n%s", err, boardLines(out))
		}
	})
}

// adoPathServer is an Azure DevOps Server answering every classification-node
// read with status, recording each. doctor's stranded-mirror advisory also
// queries the board; its WIQL is answered with no items.
func adoPathServer(t *testing.T, status int) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var mu sync.Mutex
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/_apis/wit/wiql"):
			_, _ = w.Write([]byte(`{"workItems":[]}`))
			return
		case !strings.Contains(r.URL.Path, "/_apis/wit/classificationnodes/"):
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		seen = append(seen, r)
		mu.Unlock()
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"id":7,"name":"Nope","structureType":"area"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// TestDoctorADOFlagsMissingPath: a configured area_path the project lacks is
// an issue naming it; one it has is a ✓; an answer that cannot say is a
// warning, never a verdict; no path, no probe.
func TestDoctorADOFlagsMissingPath(t *testing.T) {
	run := func(t *testing.T, status int, fields map[string]string) (string, error, []*http.Request) {
		srv, seen := adoPathServer(t, status)
		all := map[string]string{"board.base_url": srv.URL + "/tfs/DefaultCollection", "board.area_path": `Fabrikam\Nope`}
		for k, v := range fields {
			all[k] = v
		}
		out, err := adoDoctorRun(t, all, func(string) {
			if err := runCmd(t, Local(), "set", "allow_hosts", srv.Listener.Addr().String()); err != nil {
				t.Fatal(err)
			}
		})
		return out, err, *seen
	}
	t.Run("404 is missing", func(t *testing.T) {
		out, err, seen := run(t, http.StatusNotFound, nil)
		board := boardLines(out)
		if !strings.Contains(board, `✗ [board].area_path = "Fabrikam\\Nope" does not exist in project Fabrikam`) {
			t.Errorf("want a ✗ naming the path and the project:\n%s", board)
		}
		if err == nil || len(seen) != 1 {
			t.Errorf("err = %v with %d probes; want a failing doctor after one probe", err, len(seen))
		}
	})
	t.Run("200 exists", func(t *testing.T) {
		out, err, _ := run(t, http.StatusOK, nil)
		if !strings.Contains(boardLines(out), `✓ [board].area_path = "Fabrikam\\Nope" exists`) || err != nil {
			t.Errorf("err = %v; want a ✓ for the path:\n%s", err, boardLines(out))
		}
	})
	for _, status := range []int{http.StatusInternalServerError, http.StatusNonAuthoritativeInfo} {
		t.Run(http.StatusText(status)+" cannot say", func(t *testing.T) {
			out, err, _ := run(t, status, nil)
			board := boardLines(out)
			if !strings.Contains(board, "⚠ could not verify [board].area_path") {
				t.Errorf("want a could-not-verify warning:\n%s", board)
			}
			if strings.Contains(board, "✗") || strings.Contains(board, "exists in project") || err != nil {
				t.Errorf("err = %v; an unreadable probe became a verdict:\n%s", err, board)
			}
		})
	}
	t.Run("api_version rides the probe", func(t *testing.T) {
		_, _, seen := run(t, http.StatusOK, map[string]string{"board.api_version": "6.0"})
		if len(seen) != 1 || seen[0].URL.Query().Get("api-version") != "6.0" {
			t.Errorf("probes = %d; want one carrying api-version=6.0", len(seen))
		}
	})
	t.Run("no path, no probe", func(t *testing.T) {
		out, err, seen := run(t, http.StatusOK, map[string]string{"board.area_path": ""})
		if len(seen) != 0 || err != nil {
			t.Errorf("err = %v with %d probes and no path set:\n%s", err, len(seen), boardLines(out))
		}
	})
}
