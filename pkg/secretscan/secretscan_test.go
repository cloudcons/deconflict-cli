package secretscan

import (
	"strings"
	"testing"
)

func TestScanFindsCredentialsAndLeavesTalkAboutThemAlone(t *testing.T) {
	secrets := map[string]string{
		"private key":                     "here:\n-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXkt",
		"AWS access key":                  "use AKIAIOSFODNN7EXAMPLE for staging",
		"GitHub token":                    "token ghp_" + strings.Repeat("a1B2", 9),
		"Slack token":                     "xoxb-1234567890-abcdefghij",
		"Stripe key":                      "sk_live_" + strings.Repeat("x9", 12),
		"Anthropic API key":               "ANTHROPIC_API_KEY sk-ant-api03-" + strings.Repeat("Q", 30),
		"JSON web token":                  "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"password in a connection string": "DATABASE_URL=postgres://app:s3cr3tPass@db.lan:5432/app",
		"credential assignment":           "set password = hunter2hunter2 in the env",
	}
	for kind, text := range secrets {
		got := Scan(text)
		if len(got) == 0 || !strings.Contains(Kinds(got), kind) {
			t.Errorf("%s: not found in %q (got %v)", kind, text, got)
		}
	}
	for _, text := range []string{
		"rotate the staging DB password before the release",
		"password=${DB_PASSWORD} comes from Infisical /k8s",
		"api_key: <redacted>",
		"postgres://app:$PGPASSWORD@db.lan/app",
		"postgres://app:***@db.lan/app",
		"the secret is in vault at kv/deconflict/ci, ask ops",
		"sk_test_ keys are fine in fixtures",
		"client_secret = your_client_secret_here",
	} {
		if got := Scan(text); len(got) != 0 {
			t.Errorf("flagged harmless text %q as %v", text, got)
		}
	}
}

func TestRefusalNeverEchoesTheSecret(t *testing.T) {
	secret := "ghp_" + strings.Repeat("Z9", 18)
	msg := Refusal(Scan("here you go: " + secret))
	if strings.Contains(msg, secret) || !strings.Contains(msg, "GitHub token") {
		t.Errorf("refusal: %q", msg)
	}
}

func TestStringsReachesNestedFields(t *testing.T) {
	payload := map[string]any{"body": "ok", "extra": map[string]any{"deep": []any{"AKIAIOSFODNN7EXAMPLE"}}}
	if got := Scan(Strings(payload)...); len(got) != 1 {
		t.Errorf("nested secret missed: %v", got)
	}
}
