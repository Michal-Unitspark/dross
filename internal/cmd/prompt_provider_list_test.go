package cmd

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/configenum"
)

// The init and onboard prompts are the *writer* side of [remote].provider: they
// instruct the agent which value to record. Before this phase both listed
// bitbucket (which ship could not dispatch) and omitted gitlab (which it could)
// — the exact inverse of the truth. These tests pin the bullets to
// configenum.ShipProviders so the writer and the dispatcher cannot drift again.
//
// "none" is the sentinel for "this repo has no remote". It is deliberately not
// a ShipProviders member, so it is added here rather than to the Set.
const noRemoteSentinel = "none"

// promptProviderBullets are the prompt files and the bullet label that carries
// their provider list.
var promptProviderBullets = map[string]string{
	"init.md":    "- **Provider** —",
	"onboard.md": "- **provider** —",
}

// providerBulletList returns the slash-separated provider tokens from a prompt's
// provider bullet. ok is false when the bullet is not found, so a heading or
// label rename fails loudly instead of yielding an empty set that trivially
// mismatches — or, worse, trivially matches.
func providerBulletList(t *testing.T, file, label string) (tokens []string, ok bool) {
	t.Helper()
	path := filepath.Join(repoRootFromTest(t), "assets", "prompts", file)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), label) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), label))
		// The list runs to the end of the first sentence; prose follows.
		if dot := strings.Index(rest, "."); dot >= 0 {
			rest = rest[:dot]
		}
		for _, tok := range strings.Split(rest, "/") {
			if tok = strings.TrimSpace(tok); tok != "" {
				tokens = append(tokens, tok)
			}
		}
		return tokens, true
	}
	return nil, false
}

func TestPromptProviderBulletFound(t *testing.T) {
	for file, label := range promptProviderBullets {
		tokens, ok := providerBulletList(t, file, label)
		if !ok {
			t.Errorf("%s: no bullet starting %q — was the label renamed?", file, label)
			continue
		}
		if len(tokens) == 0 {
			t.Errorf("%s: provider bullet parsed to zero tokens", file)
		}
	}
}

func TestPromptProviderListsMatchShipProviders(t *testing.T) {
	want := append(configenum.ShipProviders.Values(), noRemoteSentinel)
	sort.Strings(want)

	for file, label := range promptProviderBullets {
		tokens, ok := providerBulletList(t, file, label)
		if !ok {
			t.Errorf("%s: provider bullet not found", file)
			continue
		}
		got := append([]string(nil), tokens...)
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s provider bullet = %v, want %v — the prompts write what ship must dispatch",
				file, tokens, want)
		}
	}
}

// TestPromptsTeachAzureDevOps: the writer prompts carry what an Azure DevOps
// remote needs that autodetect cannot settle, each on the bullet that sets it —
// the url shapes, api_base as the org URL, the private default, the locked
// auth_env default, the Server api_version values — and tell a Server user to
// keep [remote].url in https form, since the host allowlist derives from it and
// an ssh://host:22 url would refuse the https api_base.
func TestPromptsTeachAzureDevOps(t *testing.T) {
	bullets := map[string][]string{
		"url":         {"https://dev.azure.com/<org>/<project>/_git/<repo>", "https://<org>.visualstudio.com/<project>/_git/<repo>"},
		"public":      {"Default no for self-hosted forges and for azure-devops"},
		"api_base":    {"https://dev.azure.com/<org>", "https://<org>.visualstudio.com", "https://<host>/tfs/<Collection>"},
		"auth_env":    {"AZURE_DEVOPS_EXT_PAT"},
		"api_version": {"remote.api_version", "2019 → `5.0`", "2020 → `6.0`", "2022 → `7.0`"},
	}
	for _, file := range []string{"init.md", "onboard.md"} {
		raw, err := os.ReadFile(filepath.Join(repoRootFromTest(t), "assets", "prompts", file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		lines := strings.Split(string(raw), "\n")

		for field, wants := range bullets {
			label := "- **" + field + "** —"
			bullet := ""
			for _, line := range lines {
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), label) {
					bullet = line
					break
				}
			}
			if bullet == "" {
				t.Errorf("%s has no %s bullet", file, field)
				continue
			}
			for _, want := range wants {
				if !strings.Contains(bullet, want) {
					t.Errorf("%s: the %s bullet does not carry %s: %q", file, field, want, bullet)
				}
			}
		}

		server := false
		for _, line := range lines {
			if strings.Contains(line, "Azure DevOps Server") && strings.Contains(line, "[remote].url") &&
				strings.Contains(line, "https://<host>/tfs/") && strings.Contains(line, "ssh://<host>:22") {
				server = true
			}
		}
		if !server {
			t.Errorf("%s does not tell an Azure DevOps Server user to write [remote].url in https form rather than ssh://<host>:22", file)
		}
	}
}
