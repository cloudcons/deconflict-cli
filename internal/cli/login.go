package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cloudcons/deconflict-cli/pkg/client"
)

// `deconflict login` — the device flow, from the terminal's side.
//
// The obvious design is "paste your token here", and it is wrong for the same
// reason it is wrong everywhere: a credential typed at a shell prompt is a
// credential in shell history, in the scrollback of whoever is pairing with
// you, and in the terminal recording somebody left running. The device flow
// costs one extra step and means the secret is minted after a human consented
// in a browser and travels straight into a 0600 file.
//
// It also gives the token an owner. A token approved by a person is attributed
// to that person, which is what makes the registry's answer to "who is working
// here" true rather than merely plausible.

func cmdLogin(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	serverURL := fs.String("server", "", "registry URL (default: $DECONFLICT_STORE)")
	timeout := fs.Duration("timeout", 5*time.Minute, "how long to wait for approval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base, err := resolveServer(*serverURL)
	if err != nil {
		return err
	}

	host, _ := os.Hostname()
	var start struct {
		DeviceCode      string    `json:"device_code"`
		UserCode        string    `json:"user_code"`
		VerificationURL string    `json:"verification_url"`
		ExpiresAt       time.Time `json:"expires_at"`
		Interval        int       `json:"interval"`
	}
	if err := apiJSON(http.MethodPost, base+"/v1/cli-auth", "",
		map[string]string{"host": host}, &start); err != nil {
		return fmt.Errorf("%s: %w (is this a registry with accounts enabled?)", base, err)
	}

	fmt.Fprintf(out, "\n  Open:  %s\n  Code:  %s\n\n", start.VerificationURL, start.UserCode)
	fmt.Fprintf(out, "waiting for approval… (ctrl-c to stop)\n")

	interval := time.Duration(max(start.Interval, 1)) * time.Second
	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		var poll struct {
			Status string `json:"status"`
			Token  string `json:"token"`
		}
		code, err := apiJSONStatus(http.MethodGet,
			base+"/v1/cli-auth?device_code="+urlEscape(start.DeviceCode), "", nil, &poll)
		if err != nil {
			return err
		}
		if code == http.StatusAccepted {
			continue
		}
		if poll.Token == "" {
			return fmt.Errorf("the registry approved the sign-in but returned no token")
		}
		if err := client.SaveToken(base, poll.Token); err != nil {
			return fmt.Errorf("could not save the token: %w", err)
		}
		who := whoamiLine(base, poll.Token)
		fmt.Fprintf(out, "\nsigned in%s\n", who)
		fmt.Fprintf(out, "token saved to %s\n", client.CredentialsPath())
		return nil
	}
	return fmt.Errorf("timed out waiting for approval")
}

func cmdLogout(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	serverURL := fs.String("server", "", "registry URL (default: $DECONFLICT_STORE)")
	all := fs.Bool("all", false, "forget every registry")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base := ""
	if !*all {
		var err error
		if base, err = resolveServer(*serverURL); err != nil {
			return err
		}
	}
	if err := client.ForgetToken(base); err != nil {
		return err
	}
	// The token stays valid on the server: forgetting it locally is not
	// revoking it. Saying so is the difference between a user who revokes a
	// leaked credential and one who thinks they already did.
	if *all {
		fmt.Fprintln(out, "forgot every stored token.")
	} else {
		fmt.Fprintf(out, "forgot the token for %s.\n", base)
	}
	fmt.Fprintln(out, "the token itself is still valid — revoke it in the control panel or with `deconflict token revoke <id>`.")
	return nil
}

func cmdWhoami(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	serverURL := fs.String("server", "", "registry URL (default: $DECONFLICT_STORE)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base, err := resolveServer(*serverURL)
	if err != nil {
		return err
	}
	var me meResp
	if err := apiJSON(http.MethodGet, base+"/v1/me", client.TokenFor(base), nil, &me); err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(out, me)
	}
	fmt.Fprintf(out, "registry: %s (%s mode)\n", base, me.Mode)
	fmt.Fprintf(out, "you:      %s", me.User.Login)
	if me.User.Name != "" {
		fmt.Fprintf(out, " (%s)", me.User.Name)
	}
	fmt.Fprintf(out, " — %s, %s\n", me.User.Role, me.User.Status)
	fmt.Fprintf(out, "via:      %s\n", me.Via)
	var on []string
	for k, v := range me.Features {
		if v {
			on = append(on, k)
		}
	}
	if len(on) > 0 {
		fmt.Fprintf(out, "enabled:  %s\n", strings.Join(sortedStrings(on), ", "))
	}
	return nil
}

type meResp struct {
	User struct {
		ID     string `json:"id"`
		Login  string `json:"login"`
		Name   string `json:"name"`
		Role   string `json:"role"`
		Status string `json:"status"`
	} `json:"user"`
	Via      string          `json:"via"`
	IsAdmin  bool            `json:"is_admin"`
	Mode     string          `json:"mode"`
	Features map[string]bool `json:"features"`
}

func whoamiLine(base, token string) string {
	var me meResp
	if err := apiJSON(http.MethodGet, base+"/v1/me", token, nil, &me); err != nil {
		return ""
	}
	return " as " + me.User.Login
}

// ---------- personal API tokens ----------

func cmdToken(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("claims token <list|create|revoke>")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	serverURL := fs.String("server", "", "registry URL (default: $DECONFLICT_STORE)")
	name := fs.String("name", "", "a label for a new token, e.g. \"ci\" or \"laptop agent\"")
	days := fs.Int("days", 0, "expire after this many days (0 = never)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	base, err := resolveServer(*serverURL)
	if err != nil {
		return err
	}
	token := client.TokenFor(base)

	switch sub {
	case "list":
		var toks []struct {
			ID         string     `json:"id"`
			Name       string     `json:"name"`
			CreatedAt  time.Time  `json:"created_at"`
			ExpiresAt  *time.Time `json:"expires_at"`
			LastUsedAt *time.Time `json:"last_used_at"`
			RevokedAt  *time.Time `json:"revoked_at"`
		}
		if err := apiJSON(http.MethodGet, base+"/api/tokens", token, nil, &toks); err != nil {
			return err
		}
		if len(toks) == 0 {
			fmt.Fprintln(out, "no tokens.")
			return nil
		}
		for _, t := range toks {
			state := "active"
			switch {
			case t.RevokedAt != nil:
				state = "revoked"
			case t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt):
				state = "expired"
			}
			used := "never used"
			if t.LastUsedAt != nil {
				used = "last used " + t.LastUsedAt.Local().Format("2006-01-02 15:04")
			}
			fmt.Fprintf(out, "%s  %-8s %-24s %s\n", t.ID, state, t.Name, used)
		}
		return nil

	case "create":
		var tok struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Secret string `json:"secret"`
		}
		if err := apiJSON(http.MethodPost, base+"/api/tokens", token,
			map[string]any{"name": *name, "days": *days}, &tok); err != nil {
			return err
		}
		// Printed once, to stdout, with the reason it will not be shown again —
		// people do not scroll back for a secret they were not told to keep.
		fmt.Fprintf(out, "%s\n\n", tok.Secret)
		fmt.Fprintf(out, "id %s (%s). This is the only time it will be shown.\n", tok.ID, tok.Name)
		fmt.Fprintf(out, "Give it to an agent as DECONFLICT_TOKEN, or run `deconflict login` on that machine instead.\n")
		return nil

	case "revoke":
		id := fs.Arg(0)
		if id == "" {
			return fmt.Errorf("which token? `deconflict token list` shows the ids")
		}
		if err := apiJSON(http.MethodDelete, base+"/api/tokens/"+urlEscape(id), token, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "revoked %s\n", id)
		return nil
	}
	return fmt.Errorf("unknown token subcommand %q", sub)
}

// ---------- shared HTTP for the account commands ----------

// resolveServer works out which registry these commands are talking to. They
// are the one part of the CLI that only makes sense against a server, so a file
// store is a clear error rather than a confusing one.
func resolveServer(explicit string) (string, error) {
	v := strings.TrimSpace(explicit)
	if v == "" {
		v = strings.TrimSpace(os.Getenv("DECONFLICT_STORE"))
	}
	if v == "" {
		return "", fmt.Errorf("no registry URL: pass --server, or set DECONFLICT_STORE=http://host:7777")
	}
	if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
		return "", fmt.Errorf("%q is a local file store — accounts only exist on a `deconflict serve` registry", v)
	}
	return strings.TrimRight(v, "/"), nil
}

func apiJSON(method, url, token string, body, out any) error {
	_, err := apiJSONStatus(method, url, token, body, out)
	return err
}

func apiJSONStatus(method, url, token string, body, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		if resp.StatusCode == http.StatusUnauthorized {
			msg += " — run `deconflict login`"
		}
		return resp.StatusCode, fmt.Errorf("%s", msg)
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("unparseable response from %s: %w", url, err)
		}
	}
	return resp.StatusCode, nil
}
