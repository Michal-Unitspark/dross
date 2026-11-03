package forge

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Rivil/dross/internal/hostallow"
)

// adoBoardsTokenEnv is the env var the Azure Boards fixtures name. The token is
// long enough for redact.Scrub to act on it.
const (
	adoBoardsTokenEnv = "MOCK_ADO_BOARDS_TOKEN"
	adoBoardsPAT      = "adoboardspat0123456789abcdefghijklmnop" // dross:allow-secret
)

// adoBoardsServer is an httptest server that counts every hit and hands each
// request to h (which may be nil for a server that only counts).
func adoBoardsServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if h != nil {
			h(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// adoBoardsConfig is a valid Config pointing at srv's /acme collection.
func adoBoardsConfig(srv *httptest.Server) Config {
	return Config{
		Provider: "azure-devops",
		APIBase:  srv.URL + "/acme",
		Project:  "Fabrikam",
		AuthEnv:  adoBoardsTokenEnv,
		Hosts:    allowingSelf(srv.URL),
	}
}

// newTestAzureBoards builds a client against srv with cfg tweaked by mod.
func newTestAzureBoards(t *testing.T, srv *httptest.Server, mod func(*Config)) *AzureBoardsClient {
	t.Helper()
	t.Setenv(adoBoardsTokenEnv, adoBoardsPAT)
	cfg := adoBoardsConfig(srv)
	if mod != nil {
		mod(&cfg)
	}
	c, err := NewAzureBoards(cfg)
	if err != nil {
		t.Fatalf("NewAzureBoards: %v", err)
	}
	return c
}

// TestNewAzureBoardsNamesMissingFields: each required [board] field is named
// when it is missing, and a malformed api_version is refused by name — all of
// it before the env var is read (it is unset here, so a check that ran after
// Getenv would surface as "is not set") and without a single request.
func TestNewAzureBoardsNamesMissingFields(t *testing.T) {
	srv, hits := adoBoardsServer(t, nil)
	t.Setenv(adoBoardsTokenEnv, "")
	for _, tc := range []struct {
		name string
		mod  func(*Config)
		want string
	}{
		{"no base_url", func(c *Config) { c.APIBase = "" }, "[board].base_url"},
		{"blank base_url", func(c *Config) { c.APIBase = "   " }, "[board].base_url"},
		{"no project", func(c *Config) { c.Project = "" }, "[board].project"},
		{"no auth_env", func(c *Config) { c.AuthEnv = "" }, "[board].auth_env"},
		{"api_version with &", func(c *Config) { c.APIVersion = "7.1&x=1" }, "[board].api_version"},
		{"api_version with #", func(c *Config) { c.APIVersion = "7.1#x" }, "[board].api_version"},
		{"api_version path", func(c *Config) { c.APIVersion = "../7.1" }, "[board].api_version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := adoBoardsConfig(srv)
			tc.mod(&cfg)
			c, err := NewAzureBoards(cfg)
			if err == nil || c != nil {
				t.Fatalf("NewAzureBoards accepted %s: client=%v err=%v", tc.name, c, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not name %s: %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "is not set") {
				t.Errorf("the env var was read before the config check: %v", err)
			}
		})
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("constructor made %d requests, want 0", n)
	}
}

// TestNewAzureBoardsRefusesHostBeforeToken: a base_url outside the allowlist is
// refused as a policy refusal before the env var is touched. With the env unset
// the error is the refusal, never "is not set"; with a sentinel set, the
// sentinel never appears in the error.
func TestNewAzureBoardsRefusesHostBeforeToken(t *testing.T) {
	cfg := Config{
		Provider: "azure-devops",
		APIBase:  "https://attacker.example",
		Project:  "Fabrikam",
		AuthEnv:  hostileEnv,
		Hosts:    hostallow.Derive("https://dev.azure.com/acme/Fabrikam/_git/app", nil),
	}
	t.Setenv(hostileEnv, "")
	_, err := NewAzureBoards(cfg)
	if !errors.Is(err, hostallow.ErrRefused) {
		t.Fatalf("want a hostallow refusal, got %v", err)
	}
	if strings.Contains(err.Error(), "is not set") {
		t.Errorf("the env var was read before the host check: %v", err)
	}

	t.Setenv(hostileEnv, sentinelToken)
	_, err = NewAzureBoards(cfg)
	if !errors.Is(err, hostallow.ErrRefused) {
		t.Fatalf("want a hostallow refusal with the env set, got %v", err)
	}
	if strings.Contains(err.Error(), sentinelToken) {
		t.Errorf("refusal leaked the token: %v", err)
	}
}

// TestADOBoardBaseURLShapes: every Services form and a Server collection URL
// are accepted and keep their collection path; a Services URL with no org or
// with a project, and a Server host with no collection, are refused naming the
// form to use.
func TestADOBoardBaseURLShapes(t *testing.T) {
	for raw, want := range map[string]string{
		"https://dev.azure.com/acme":                          "https://dev.azure.com/acme",
		"https://dev.azure.com/acme/":                         "https://dev.azure.com/acme",
		"https://acme.visualstudio.com":                       "https://acme.visualstudio.com",
		"https://acme.visualstudio.com/DefaultCollection":     "https://acme.visualstudio.com/DefaultCollection",
		"https://tfs.corp.example/tfs/DefaultCollection":      "https://tfs.corp.example/tfs/DefaultCollection",
		"https://tfs.corp.example/DefaultCollection":          "https://tfs.corp.example/DefaultCollection",
		"https://tfs.corp.example:8443/tfs/DefaultCollection": "https://tfs.corp.example:8443/tfs/DefaultCollection",
	} {
		got, err := CheckADOBaseURL(raw)
		if err != nil {
			t.Errorf("CheckADOBaseURL(%q) refused: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("CheckADOBaseURL(%q) = %q, want %q", raw, got, want)
		}
	}
	for raw, form := range map[string]string{
		"https://dev.azure.com":              "https://dev.azure.com/<org>",
		"https://dev.azure.com/acme/Proj":    "https://dev.azure.com/<org>",
		"https://tfs.corp.example":           "<host>/<collection>",
		"https://tfs.corp.example/tfs":       "<host>/<collection>",
		"https://acme.visualstudio.com/Proj": "https://<org>.visualstudio.com",
		"https://dev.azure.com/acme?x=1":     "query or fragment",
		"https://dev.azure.com/acme#frag":    "query or fragment",
		"https://dev.azure.com/acme/%2e%2e":  "dot path segment",
		"dev.azure.com/acme":                 "absolute https URL",
	} {
		_, err := CheckADOBaseURL(raw)
		if err == nil {
			t.Errorf("CheckADOBaseURL(%q) accepted it", raw)
			continue
		}
		if !strings.Contains(err.Error(), form) || !strings.Contains(err.Error(), "[board].base_url") {
			t.Errorf("CheckADOBaseURL(%q) = %v, want it to name [board].base_url and %q", raw, err, form)
		}
	}

	// The constructor runs the same check: a bare Server host on an allowed
	// host is refused for its shape, before the token is read.
	t.Setenv(adoBoardsTokenEnv, "")
	_, err := NewAzureBoards(Config{
		Provider: "azure-devops",
		APIBase:  "https://tfs.corp.example",
		Project:  "Fabrikam",
		AuthEnv:  adoBoardsTokenEnv,
		Hosts:    hostallow.Derive("https://tfs.corp.example/tfs/DefaultCollection/Fabrikam/_git/app", nil),
	})
	if err == nil || !strings.Contains(err.Error(), "<host>/<collection>") {
		t.Errorf("NewAzureBoards on a bare Server host: want the <host>/<collection> refusal, got %v", err)
	}
}

// TestADOBoardURLEscapingAndOneAPIVersion: the project name is escaped exactly
// once (a space is %20, never %2520), and an endpoint that already carries an
// api-version goes out with exactly one — the configured one.
func TestADOBoardURLEscapingAndOneAPIVersion(t *testing.T) {
	var gotPath string
	var gotVersions []string
	srv, _ := adoBoardsServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotVersions = r.URL.Query()["api-version"]
		w.Write([]byte(`{}`))
	})
	c := newTestAzureBoards(t, srv, func(cfg *Config) { cfg.Project = "Scratch project" })

	endpoint := c.projectAPI("/wit/workitems/7")
	if !strings.HasSuffix(endpoint, "/acme/Scratch%20project/_apis/wit/workitems/7") {
		t.Errorf("projectAPI = %q, want .../acme/Scratch%%20project/_apis/wit/workitems/7", endpoint)
	}
	if err := c.do(http.MethodGet, endpoint+"?api-version=5.0", nil, nil); err != nil {
		t.Fatalf("do: %v", err)
	}
	if gotPath != "/acme/Scratch%20project/_apis/wit/workitems/7" {
		t.Errorf("request path = %q, want /acme/Scratch%%20project/_apis/wit/workitems/7", gotPath)
	}
	if strings.Contains(gotPath, "%2520") {
		t.Errorf("project name was double-escaped: %q", gotPath)
	}
	if len(gotVersions) != 1 || gotVersions[0] != "7.1" {
		t.Errorf("api-version = %q, want exactly [7.1]", gotVersions)
	}
}

// TestADOBoardRequestShape pins the credential and the headers every request
// carries. default, basic and private-token all send Basic with an EMPTY user —
// even with auth_user set, which Azure DevOps never reads — and bearer sends
// the token as a bearer. api-version is 7.1 by default and the configured one
// when set; X-TFS-FedAuthRedirect asks for a 401 rather than a sign-in redirect.
func TestADOBoardRequestShape(t *testing.T) {
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+adoBoardsPAT))
	for _, tc := range []struct {
		scheme, version, wantAuth, wantVersion string
	}{
		{"", "", basic, "7.1"},
		{"basic", "", basic, "7.1"},
		{"private-token", "6.0", basic, "6.0"},
		{"bearer", "", "Bearer " + adoBoardsPAT, "7.1"},
		{"Bearer", "7.0", "Bearer " + adoBoardsPAT, "7.0"},
	} {
		t.Run("scheme="+tc.scheme, func(t *testing.T) {
			var r0 *http.Request
			srv, _ := adoBoardsServer(t, func(w http.ResponseWriter, r *http.Request) {
				r0 = r.Clone(r.Context())
				w.Write([]byte(`{}`))
			})
			c := newTestAzureBoards(t, srv, func(cfg *Config) {
				cfg.AuthScheme = tc.scheme
				cfg.AuthUser = "someone@example.com"
				cfg.APIVersion = tc.version
			})
			if err := c.do(http.MethodGet, c.projectAPI("/wit/workitems/1"), nil, nil); err != nil {
				t.Fatalf("do: %v", err)
			}
			if got := r0.Header.Get("Authorization"); got != tc.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, tc.wantAuth)
			}
			if got := r0.URL.Query()["api-version"]; len(got) != 1 || got[0] != tc.wantVersion {
				t.Errorf("api-version = %q, want [%s]", got, tc.wantVersion)
			}
			if got := r0.Header.Get("X-TFS-FedAuthRedirect"); got != "Suppress" {
				t.Errorf("X-TFS-FedAuthRedirect = %q, want Suppress", got)
			}
		})
	}
}

// TestADOBoardContentType: a work-item write (adoJSONPatch) goes out as
// application/json-patch+json, any other body as application/json.
func TestADOBoardContentType(t *testing.T) {
	var ct string
	srv, _ := adoBoardsServer(t, func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		w.Write([]byte(`{}`))
	})
	c := newTestAzureBoards(t, srv, nil)
	patch := adoJSONPatch{{Op: "add", Path: "/fields/System.Title", Value: "t"}}
	if err := c.do(http.MethodPost, c.projectAPI("/wit/workitems/$Task"), patch, nil); err != nil {
		t.Fatalf("do patch: %v", err)
	}
	if ct != "application/json-patch+json" {
		t.Errorf("work-item write Content-Type = %q, want application/json-patch+json", ct)
	}
	if err := c.do(http.MethodPost, c.projectAPI("/wit/wiql"), map[string]string{"query": "x"}, nil); err != nil {
		t.Fatalf("do json: %v", err)
	}
	if ct != "application/json" {
		t.Errorf("plain JSON Content-Type = %q, want application/json", ct)
	}
}

// TestADOBoardCredentialNeverLeaks: an error body echoing the PAT, raw and as
// the base64 Basic credential, yields an error carrying neither; a redirect is
// never followed (the second server sees no hit, so the Authorization header
// never reaches it); a 203 sign-in answer is an error naming the env var.
func TestADOBoardCredentialNeverLeaks(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte(":" + adoBoardsPAT))

	t.Run("400 echo", func(t *testing.T) {
		srv, _ := adoBoardsServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"bad token ` + adoBoardsPAT + ` / Basic ` + b64 + `"}`))
		})
		c := newTestAzureBoards(t, srv, nil)
		err := c.do(http.MethodGet, c.projectAPI("/wit/workitems/1"), nil, nil)
		if err == nil {
			t.Fatal("a 400 was not an error")
		}
		if strings.Contains(err.Error(), adoBoardsPAT) || strings.Contains(err.Error(), b64) {
			t.Errorf("error leaked the credential: %v", err)
		}
		if !strings.Contains(err.Error(), "HTTP 400") {
			t.Errorf("error does not carry the status: %v", err)
		}
	})

	t.Run("302 not followed", func(t *testing.T) {
		other, otherHits := adoBoardsServer(t, nil)
		srv, _ := adoBoardsServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/_signin", http.StatusFound)
		})
		c := newTestAzureBoards(t, srv, nil)
		err := c.do(http.MethodGet, c.projectAPI("/wit/workitems/1"), nil, nil)
		if !errors.Is(err, errADOBoardsSignIn) {
			t.Errorf("a 302 is not a sign-in error: %v", err)
		}
		if n := atomic.LoadInt32(otherHits); n != 0 {
			t.Errorf("the redirect target saw %d hits, want 0", n)
		}
	})

	t.Run("203 sign-in", func(t *testing.T) {
		srv, _ := adoBoardsServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNonAuthoritativeInfo)
			w.Write([]byte(`<html>Sign in</html>`))
		})
		c := newTestAzureBoards(t, srv, nil)
		err := c.do(http.MethodGet, c.projectAPI("/wit/workitems/1"), nil, nil)
		if !errors.Is(err, errADOBoardsSignIn) || !strings.Contains(err.Error(), "$"+adoBoardsTokenEnv) {
			t.Errorf("a 203 must be a sign-in error naming $%s, got %v", adoBoardsTokenEnv, err)
		}
	})
}

// TestADOBoardPathNormalization: an area or iteration path relative to the
// project gets the project prefixed once, at construction; one that already is
// the project, or starts with it, is kept as written.
func TestADOBoardPathNormalization(t *testing.T) {
	srv, _ := adoBoardsServer(t, nil)
	for _, tc := range []struct{ area, iteration, wantArea, wantIteration string }{
		{"Web", "Sprint 1", `Fabrikam\Web`, `Fabrikam\Sprint 1`},
		{"Fabrikam", "Fabrikam", "Fabrikam", "Fabrikam"},
		{`Fabrikam\Web`, `Fabrikam\Sprint 1`, `Fabrikam\Web`, `Fabrikam\Sprint 1`},
		{`fabrikam\Web`, `\Fabrikam\Sprint 1`, `fabrikam\Web`, `Fabrikam\Sprint 1`},
		{`FabrikamWeb`, "", `Fabrikam\FabrikamWeb`, ""},
		{"", "", "", ""},
	} {
		c := newTestAzureBoards(t, srv, func(cfg *Config) {
			cfg.AreaPath, cfg.IterationPath = tc.area, tc.iteration
		})
		if c.areaPath != tc.wantArea || c.iterationPath != tc.wantIteration {
			t.Errorf("area %q / iteration %q normalized to %q / %q, want %q / %q",
				tc.area, tc.iteration, c.areaPath, c.iterationPath, tc.wantArea, tc.wantIteration)
		}
	}
}
