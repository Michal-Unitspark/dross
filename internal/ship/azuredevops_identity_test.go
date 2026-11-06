package ship

import (
	"net/http"
	"testing"

	"github.com/Rivil/dross/internal/hostallow"
)

// adoConnectionData is a connectionData answer for an identity id with an
// optional properties.Account.
func adoConnectionData(id, account string) string {
	props := `{}`
	if account != "" {
		props = `{"Account":{"$type":"System.String","$value":"` + account + `"}}`
	}
	return `{"authenticatedUser":{"id":"` + id + `","providerDisplayName":"Me Myself","isActive":true,"properties":` + props + `},` +
		`"authorizedUser":{"id":"` + id + `"},"instanceId":"11111111-2222-3333-4444-555555555555"}`
}

// TestADOAuthenticatedUserEndpoint: identity is read at the org/collection
// base, never under a project, at the preview of the configured api-version.
func TestADOAuthenticatedUserEndpoint(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	body := adoConnectionData(adoGUID1, "me@corp.com")
	for _, tc := range []struct {
		name, base, url, version, path, sent string
	}{
		{"services", "/o", adoTestURL, "7.1", "/o/_apis/connectionData", "7.1-preview.1"},
		{"server collection", "/tfs/Coll", "https://tfs.corp/tfs/Coll/P/_git/R", "6.0", "/tfs/Coll/_apis/connectionData", "6.0-preview.1"},
		{"already a preview", "/o", adoTestURL, "7.1-preview.1", "/o/_apis/connectionData", "7.1-preview.1"},
		{"default version", "/o", adoTestURL, "", "/o/_apis/connectionData", "7.1-preview.1"},
	} {
		rec := newADORecorder(t, 200, body)
		opts := OpenOpts{
			URL:        tc.url,
			APIBase:    rec.URL + tc.base,
			AuthEnv:    adoTestEnv,
			APIVersion: tc.version,
			Hosts:      hostallow.Derive(rec.URL, nil),
		}
		if _, err := azureDevOpsAuthenticatedUser(opts); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if rec.path != tc.path {
			t.Errorf("%s: path = %q, want %q", tc.name, rec.path, tc.path)
		}
		if got := rec.query["api-version"]; len(got) != 1 || got[0] != tc.sent {
			t.Errorf("%s: api-version = %v, want [%s]", tc.name, got, tc.sent)
		}
	}
}

// TestADOAuthenticatedUserRefusesNobody: an anonymous or id-less answer is an
// error, never an empty Account that a thread author could match.
func TestADOAuthenticatedUserRefusesNobody(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"anonymous id":       {200, adoConnectionData(adoAnonymousID, "anon")},
		"anonymous id upper": {200, adoConnectionData("AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA", "anon")},
		"no id":              {200, adoConnectionData("", "me@corp.com")},
		"no user":            {200, `{"instanceId":"x"}`},
		"no account name":    {200, adoConnectionData(adoGUID1, "")},
		"non-JSON body":      {200, `<html>nope</html>`},
		"500":                {500, `{"message":"boom"}`},
		"203 sign-in page":   {http.StatusNonAuthoritativeInfo, `<html>Sign In</html>`},
	} {
		rec := newADORecorder(t, tc.status, tc.body)
		got, err := azureDevOpsAuthenticatedUser(adoTestOpts(rec.Server))
		if err == nil || got != (Account{}) {
			t.Errorf("%s: got %+v, %v; want (Account{}, err)", name, got, err)
		}
	}
}

// TestADOIdentityMatchesThreadAuthor: connectionData and a thread author name
// the same identity in different case; both come out lowercased, so dross's own
// thread reads as its own.
func TestADOIdentityMatchesThreadAuthor(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	rec := newADORecorder(t, 200, adoConnectionData("AB12CD34-0000-4000-8000-0000000000FF", "me@corp.com"))
	me, err := azureDevOpsAuthenticatedUser(adoTestOpts(rec.Server))
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := listADOThreads(t, adoThreads(
		adoThread(1, "", adoTextBy(1, `{"id":"ab12cd34-0000-4000-8000-0000000000ff","uniqueName":"me@corp.com"}`, "mine")),
	))
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if me.ID != got[0].Author.ID {
		t.Errorf("Account.ID %q != CommentAuthor.ID %q", me.ID, got[0].Author.ID)
	}
}
