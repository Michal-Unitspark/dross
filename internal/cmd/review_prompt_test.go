package cmd

import (
	"regexp"
	"strings"
	"testing"
)

// reviewADOBullet is review.md §1's Azure DevOps bullet: its first line and
// every indented line after it, up to the next unindented line.
func reviewADOBullet(t *testing.T, section string) string {
	t.Helper()
	lines := strings.Split(section, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "- Azure DevOps") {
			continue
		}
		end := i + 1
		for end < len(lines) && (lines[end] == "" || strings.HasPrefix(lines[end], " ")) {
			end++
		}
		return strings.Join(lines[i:end], "\n")
	}
	t.Fatal("review.md §1 has no `- Azure DevOps` bullet")
	return ""
}

// twoDotDiff is a base..head range with exactly two dots — the diff of both
// sides, not what the phase added.
var twoDotDiff = regexp.MustCompile(`[^.]\.\.origin/phase/`)

// TestReviewPromptAzureDevOpsDiff: on Azure DevOps the review diff is built
// from git, three-dot against the PR's base, after fetching both refs — never
// through a hand-built authenticated call.
func TestReviewPromptAzureDevOpsDiff(t *testing.T) {
	p := readPrompt(t, "review.md")
	fetchSec := promptSection(t, p, "## 1. Fetch the PR diff")
	bullet := reviewADOBullet(t, fetchSec)

	const fetch = "git fetch origin <base> phase/<phase-id>"
	const diff = "git diff origin/<base>...origin/phase/<phase-id> > .dross/phases/<phase-id>/review-diff.patch"
	fi, di := strings.Index(bullet, fetch), strings.Index(bullet, diff)
	if di < 0 {
		t.Errorf("the azure-devops bullet lacks the three-dot diff into review-diff.patch: %q", diff)
	}
	if fi < 0 || (di >= 0 && fi > di) {
		t.Errorf("the azure-devops bullet must fetch both refs (%q) before the diff", fetch)
	}
	if twoDotDiff.MatchString(bullet) {
		t.Error("the azure-devops bullet uses a two-dot range; the review diff is three-dot")
	}
	if !strings.Contains(bullet, "`dross changes show <phase-id>`") || !strings.Contains(bullet, "`base`") {
		t.Error("the azure-devops bullet must take the base from `base` in `dross changes show <phase-id>`")
	}

	for _, banned := range []string{"curl", "az ", "Authorization", "$<auth_env>"} {
		if strings.Contains(bullet, banned) {
			t.Errorf("the azure-devops bullet hand-builds auth: it contains %q", banned)
		}
	}

	if !strings.Contains(fetchSec, "exists and is non-empty") {
		t.Error("§1 lost the 'exists and is non-empty' guard on the diff")
	}
	if post := promptSection(t, p, "## 5. Post"); !strings.Contains(post, "dross ship comment --pr <n> --body-file") {
		t.Error("§5 lost `dross ship comment --pr <n> --body-file`, the path findings post through")
	}
}
