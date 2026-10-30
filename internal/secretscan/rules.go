package secretscan

import (
	"bytes"
	"math"
	"regexp"
	"strings"
)

// Rule is one credential shape the scanner recognises.
//
// Every Regex carries exactly one capture group — the secret value itself —
// so a hit's length fingerprint measures the value, not the key name or the
// header scheme around it. Prefix is the rule's fixed literal, printed as the
// second half of the fingerprint; it is a property of the rule, never bytes
// copied out of the match, which is what keeps a report echo-free (c-5).
type Rule struct {
	Name   string
	Regex  *regexp.Regexp
	Prefix string

	// carveOut, when set, rejects a candidate value the regex accepted — the
	// one place a rule can say "this shape, but not this instance" without
	// widening the regex into something a reader can no longer check.
	carveOut func(value string) bool

	// needles are the prefilter: literals of which every Regex match holds at
	// least one, so a line holding none of them skips the regex. bytes.Contains
	// is assembly, which the -race tax on the regex VM does not reach. A rule
	// with no needles always runs its regex (fail open).
	needles [][]byte

	// fold marks a (?i) rule: its needles are lower-case and are searched in
	// the ASCII-lowered line.
	fold bool
}

// valueClass is the character set a credential value is drawn from. `$`,
// `<`, `>`, `"` and `+` sit outside it on purpose: `$GITHUB_TOKEN`,
// `<your-token>`, `"Bearer "+c.token` and `Bearer <token-from-auth_env>` are
// references to a secret, not the secret, and the class is what tells them
// apart from a pasted value of the same length.
const valueClass = `[A-Za-z0-9+/=_.~@!#%^*-]`

// keyContextMinEntropy is the Shannon floor (bits per byte) a key-context value
// must clear. A value below it — `aaaaaaaaaaaaaaaaaaaab`, `xxxxxxxxxxxxxxxx` —
// is a placeholder, and the entropy_rules decision restricts the judgement to
// the value side of a labelled assignment; no bare line is ever judged.
const keyContextMinEntropy = 3.0

// rules is the fixed table. The order is the report order; no rule is
// consulted from anywhere but the binary (pattern_source decision).
var rules = []Rule{
	{
		Name:    "github-token",
		Regex:   regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{22,})\b`),
		Prefix:  "ghp_",
		needles: lits("ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"),
	},
	{
		Name:    "gitlab-pat",
		Regex:   regexp.MustCompile(`\b(glpat-[A-Za-z0-9_-]{20,})`),
		Prefix:  "glpat-",
		needles: lits("glpat-"),
	},
	{
		// A new-format Azure DevOps PAT: 84 alphanumerics with the fixed AZDO
		// signature at positions 76-79. The signature sits mid-value, so it is the
		// rule's fixed literal rather than a true prefix. An old 52-char PAT is
		// bare base32 — too generic to match alone — and is left to key-context.
		Name:    "azure-devops-pat",
		Regex:   regexp.MustCompile(`\b([A-Za-z0-9]{76}AZDO[A-Za-z0-9]{4})\b`),
		Prefix:  "AZDO",
		needles: lits("AZDO"),
	},
	{
		Name:    "atlassian-token",
		Regex:   regexp.MustCompile(`\b(ATATT[A-Za-z0-9_=-]{20,})`),
		Prefix:  "ATATT",
		needles: lits("ATATT"),
	},
	{
		Name:    "aws-access-key",
		Regex:   regexp.MustCompile(`\b((?:AKIA|ASIA)[A-Z0-9]{16})\b`),
		Prefix:  "AKIA",
		needles: lits("AKIA", "ASIA"),
		// AKIAIOSFODNN7EXAMPLE is the placeholder every AWS document carries;
		// it is documentation, not a key.
		carveOut: func(v string) bool { return strings.HasSuffix(v, "EXAMPLE") },
	},
	{
		Name:    "slack-token",
		Regex:   regexp.MustCompile(`\b(xox[abprs]-[A-Za-z0-9-]{10,})`),
		Prefix:  "xox",
		needles: lits("xox"),
	},
	{
		Name:    "sk-api-key",
		Regex:   regexp.MustCompile(`\b(sk-(?:proj-|ant-)?[A-Za-z0-9_-]{20,})`),
		Prefix:  "sk-",
		needles: lits("sk-"),
	},
	{
		Name:    "pem-private-key",
		Regex:   regexp.MustCompile(`(-----BEGIN (?:[A-Z]+ )?PRIVATE KEY(?: BLOCK)?-----)`),
		Prefix:  "-----BEGIN",
		needles: lits("PRIVATE KEY"),
	},
	{
		Name:    "authorization-header",
		Regex:   regexp.MustCompile(`(?i)authorization["']?\s*[:=]\s*["']?(?:basic|bearer)\s+(` + valueClass + `{8,})`),
		Prefix:  "Basic|Bearer",
		needles: lits("authorization"),
		fold:    true,
	},
	{
		Name: "key-context",
		Regex: regexp.MustCompile(`(?i)[A-Za-z0-9_.-]*(?:password|passwd|pwd|secret|api[_-]key|access[_-]key|token|authorization)["']?\s*[:=]\s*["']?(` +
			valueClass + `{16,})`),
		Prefix:   "",
		carveOut: func(v string) bool { return entropy(v) < keyContextMinEntropy },
		needles: lits("password", "passwd", "pwd", "secret", "api_key", "api-key",
			"access_key", "access-key", "token", "authorization"),
		fold: true,
	},
}

// Rules returns the compiled table, in report order.
func Rules() []Rule { return rules }

func lits(ss ...string) [][]byte {
	out := make([][]byte, len(ss))
	for i, s := range ss {
		out[i] = []byte(s)
	}
	return out
}

// foldEscapes are the only non-ASCII runes (?i) folds onto an ASCII letter:
// U+017F LONG S matches s and U+212A KELVIN SIGN matches k, so `paſſword`
// satisfies the key-context regex while holding no ASCII needle. A fold rule
// runs its regex on any line carrying one — exactly these byte sequences, not
// "any non-ASCII", since em-dashes are common in .dross prose.
var foldEscapes = lits("\u017f", "\u212a")

// candidates is the prefilter: bit i is set when rules[i]'s regex could match
// line. It reads nothing but its arguments, so concurrent scans share it.
func candidates(line []byte) uint64 { return candidatesIn(rules, line) }

func candidatesIn(rs []Rule, line []byte) uint64 {
	var mask uint64
	var folded []byte
	for i, r := range rs {
		hay := line
		if r.fold {
			if containsAny(line, foldEscapes) {
				mask |= 1 << i
				continue
			}
			if folded == nil {
				folded = asciiLower(line)
			}
			hay = folded
		}
		if len(r.needles) == 0 || containsAny(hay, r.needles) {
			mask |= 1 << i
		}
	}
	return mask
}

func containsAny(b []byte, needles [][]byte) bool {
	for _, n := range needles {
		if bytes.Contains(b, n) {
			return true
		}
	}
	return false
}

// asciiLower maps A-Z to a-z and copies every other byte as is. A multi-byte
// UTF-8 sequence holds no ASCII byte, so folding can't conjure a needle out of
// non-ASCII text.
func asciiLower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

// entropy is the Shannon entropy of s in bits per byte.
func entropy(s string) float64 {
	if s == "" {
		return 0
	}
	var counts [256]int
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	n := float64(len(s))
	var h float64
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}
