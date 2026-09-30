// Package secretscan finds credential-shaped text in what one agent is about
// to send another.
//
// It is the one defence against an agent being talked into handing over
// secrets that does not depend on judging anybody's intent: whoever asks, and
// however the request is worded, a message carrying a private key or a live
// token does not leave. It is deliberately a list of shapes rather than a
// classifier — deterministic, local, and it never needs to show the content to
// anybody. It reports what kind of secret it saw, never the secret.
package secretscan

import (
	"regexp"
	"strings"
)

// Finding is one kind of credential seen. The matched text is not kept.
type Finding struct {
	Kind string
}

type rule struct {
	kind string
	re   *regexp.Regexp
	// ok, when set, lets a match through: a placeholder, a variable, a
	// redaction — text that talks about a secret without carrying one.
	ok func(match string) bool
}

// placeholder reports whether a value is a stand-in rather than a secret.
func placeholder(v string) bool {
	v = strings.Trim(strings.TrimSpace(v), `"'`)
	if v == "" {
		return true
	}
	switch v[0] {
	case '$', '<', '{', '%', '*', '[', '(':
		return true
	}
	lower := strings.ToLower(v)
	for _, p := range []string{"redacted", "xxxx", "changeme", "example", "placeholder", "your_", "your-", "dummy", "secret_here", "…", "..."} {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

var rules = []rule{
	{kind: "private key", re: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)},
	{kind: "AWS access key", re: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{kind: "GitHub token", re: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,})\b`)},
	{kind: "Slack token", re: regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{kind: "Stripe key", re: regexp.MustCompile(`\b(?:sk|rk)_live_[A-Za-z0-9]{16,}`)},
	{kind: "Google API key", re: regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)},
	{kind: "Anthropic API key", re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}`)},
	{kind: "OpenAI API key", re: regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_\-]{32,}`)},
	{kind: "JSON web token", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	{
		kind: "password in a connection string",
		re:   regexp.MustCompile(`\b[a-z][a-z0-9+.\-]*://[^\s/:@]+:([^\s/@]{3,})@`),
		ok: func(m string) bool {
			i, j := strings.LastIndex(m, ":"), strings.LastIndex(m, "@")
			return i >= 0 && j > i && placeholder(m[i+1:j])
		},
	},
	{
		kind: "credential assignment",
		re:   regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|api[_-]?key|access[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|private[_-]?key)\b["']?\s*[:=]\s*["']?([^\s"',;]{8,})`),
		ok: func(m string) bool {
			k := strings.IndexAny(m, ":=")
			return k >= 0 && placeholder(m[k+1:])
		},
	},
}

// Scan reports each kind of credential found in any of the texts, once.
func Scan(texts ...string) []Finding {
	seen := map[string]bool{}
	var out []Finding
	for _, t := range texts {
		for _, r := range rules {
			if seen[r.kind] {
				continue
			}
			for _, m := range r.re.FindAllString(t, -1) {
				if r.ok != nil && r.ok(m) {
					continue
				}
				seen[r.kind] = true
				out = append(out, Finding{Kind: r.kind})
				break
			}
		}
	}
	return out
}

// Kinds names what was found, for a refusal.
func Kinds(fs []Finding) string {
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = f.Kind
	}
	return strings.Join(names, ", ")
}

// Refusal is the sentence a sender is told. It names the kind, never the value,
// and says what to send instead.
func Refusal(fs []Finding) string {
	return "this looks like it carries a credential (" + Kinds(fs) + "), and credentials are not sent to other agents. " +
		"Send where to find it or who can grant it — a secret's name or path, not its value."
}

// Strings collects every string value in a JSON-shaped payload, however
// nested, so a secret cannot be tucked into a field the scan did not name.
func Strings(v any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case []string:
			out = append(out, x...)
		}
	}
	walk(v)
	return out
}
