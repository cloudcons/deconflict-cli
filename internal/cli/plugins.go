package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/cloudcons/deconflict/internal/gitinfo"
	"github.com/cloudcons/deconflict/internal/plugin"
	"github.com/cloudcons/deconflict/internal/store"
)

// Integrations from a terminal. Configuring one is a form-shaped job and stays
// in the control panel; what belongs here is everything an operator wants
// during an incident — what is configured, does it still authenticate, what
// failed to deliver, and try again.

func cmdPlugins(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("claims plugins <list|providers|test|deliveries|retry>")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("plugins", flag.ContinueOnError)
	serverURL := fs.String("server", "", "registry URL (default: $DECONFLICT_STORE)")
	asJSON := fs.Bool("json", false, "emit JSON")
	limit := fs.Int("limit", 25, "how many deliveries to show")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	base, err := resolveServer(*serverURL)
	if err != nil {
		return err
	}
	token := store.TokenFor(base)

	var state struct {
		Integrations []plugin.Integration `json:"integrations"`
		Providers    []plugin.Descriptor  `json:"providers"`
		Deliveries   []plugin.Delivery    `json:"deliveries"`
		KeyPresent   bool                 `json:"key_present"`
	}

	switch sub {
	case "list":
		if err := apiJSON(http.MethodGet, base+"/api/plugins", token, nil, &state); err != nil {
			return err
		}
		if *asJSON {
			return encodeJSON(out, state.Integrations)
		}
		if len(state.Integrations) == 0 {
			fmt.Fprintln(out, "no integrations configured.")
			fmt.Fprintf(out, "add one at %s/#integrations\n", base)
			return nil
		}
		for _, in := range state.Integrations {
			enabled := "enabled"
			if !in.Enabled {
				enabled = "disabled"
			}
			scope := "all repositories"
			if len(in.Repos) > 0 {
				scope = strings.Join(in.Repos, ", ")
			}
			fmt.Fprintf(out, "%s  %-9s %-10s %s\n", in.ID, enabled, in.Provider, in.Name)
			fmt.Fprintf(out, "    scope:  %s\n", scope)
			fmt.Fprintf(out, "    secret: %s\n", map[bool]string{true: "set", false: "none"}[in.HasSecret])
		}
		if !state.KeyPresent {
			fmt.Fprintln(out, "\nno DECONFLICT_SECRET_KEY on the server — integrations needing an API key cannot be saved.")
		}
		return nil

	case "providers":
		if err := apiJSON(http.MethodGet, base+"/api/plugins", token, nil, &state); err != nil {
			return err
		}
		if *asJSON {
			return encodeJSON(out, state.Providers)
		}
		for _, d := range state.Providers {
			caps := make([]string, 0, len(d.Capabilities))
			for _, c := range d.Capabilities {
				caps = append(caps, string(c))
			}
			fmt.Fprintf(out, "%-10s %s  (%s)\n", d.Provider, d.Title, strings.Join(caps, ", "))
			fmt.Fprintf(out, "    %s\n", wrap(d.Blurb, 76, "    "))
			if d.RefHint != "" {
				fmt.Fprintf(out, "    --task accepts: %s\n", d.RefHint)
			}
			for _, f := range d.Fields {
				req := ""
				if f.Required {
					req = " (required)"
				}
				if f.Secret {
					req += " (secret)"
				}
				fmt.Fprintf(out, "      %-18s %s%s\n", f.Key, f.Label, req)
			}
			fmt.Fprintln(out)
		}
		return nil

	case "test":
		id := fs.Arg(0)
		if id == "" {
			return fmt.Errorf("which integration? `deconflict plugins list` shows the ids")
		}
		var res struct {
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		}
		if err := apiJSON(http.MethodPost, base+"/api/plugins/"+url.PathEscape(id)+"/test", token, nil, &res); err != nil {
			return err
		}
		if !res.OK {
			fmt.Fprintf(out, "FAILED: %s\n", res.Message)
			return exitCode{1}
		}
		fmt.Fprintf(out, "ok: %s\n", res.Message)
		return nil

	case "deliveries":
		q := fmt.Sprintf("?limit=%d", *limit)
		if id := fs.Arg(0); id != "" {
			q += "&integration=" + url.QueryEscape(id)
		}
		var ds []plugin.Delivery
		if err := apiJSON(http.MethodGet, base+"/api/plugins/deliveries"+q, token, nil, &ds); err != nil {
			return err
		}
		if *asJSON {
			return encodeJSON(out, ds)
		}
		if len(ds) == 0 {
			fmt.Fprintln(out, "no deliveries.")
			return nil
		}
		for _, d := range ds {
			fmt.Fprintf(out, "%-8d %-8s %-8s %-14s %s\n",
				d.ID, d.Status, d.Event, d.Ref, d.UpdatedAt.Local().Format("01-02 15:04"))
			if d.LastError != "" {
				fmt.Fprintf(out, "         attempt %d: %s\n", d.Attempts, d.LastError)
			}
		}
		return nil

	case "retry":
		q := ""
		if id := fs.Arg(0); id != "" {
			q = "?integration=" + url.QueryEscape(id)
		}
		var res struct {
			Requeued int `json:"requeued"`
		}
		if err := apiJSON(http.MethodPost, base+"/api/plugins/retry"+q, token, nil, &res); err != nil {
			return err
		}
		fmt.Fprintf(out, "requeued %d failed deliver%s\n", res.Requeued,
			map[bool]string{true: "y", false: "ies"}[res.Requeued == 1])
		return nil
	}
	return fmt.Errorf("unknown plugins subcommand %q", sub)
}

// cmdTask is the other half: resolving and finding work items from a terminal,
// so that `--task` can be filled in with something that is known to exist
// rather than something that was typed from memory.
func cmdTask(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("claims task <resolve|search>")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("task", flag.ContinueOnError)
	serverURL := fs.String("server", "", "registry URL (default: $DECONFLICT_STORE)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	base, err := resolveServer(*serverURL)
	if err != nil {
		return err
	}
	token := store.TokenFor(base)
	cwd, _ := os.Getwd()
	repo := gitinfo.Repo(cwd)

	switch sub {
	case "resolve":
		ref := fs.Arg(0)
		if ref == "" {
			return fmt.Errorf("which reference?")
		}
		var item plugin.WorkItem
		u := fmt.Sprintf("%s/v1/resolve?ref=%s&repo=%s&force=1", base, url.QueryEscape(ref), url.QueryEscape(repo))
		if err := apiJSON(http.MethodGet, u, token, nil, &item); err != nil {
			return err
		}
		if *asJSON {
			return encodeJSON(out, item)
		}
		fmt.Fprintf(out, "%s  %s\n", item.Ref, item.Title)
		if item.State != "" {
			fmt.Fprintf(out, "  state: %s\n", item.State)
		}
		if item.URL != "" {
			fmt.Fprintf(out, "  %s\n", item.URL)
		}
		return nil

	case "search":
		q := strings.Join(fs.Args(), " ")
		if q == "" {
			return fmt.Errorf("search for what?")
		}
		var hits []plugin.SearchHit
		u := fmt.Sprintf("%s/v1/search?q=%s&repo=%s", base, url.QueryEscape(q), url.QueryEscape(repo))
		if err := apiJSON(http.MethodGet, u, token, nil, &hits); err != nil {
			return err
		}
		if *asJSON {
			return encodeJSON(out, hits)
		}
		if len(hits) == 0 {
			fmt.Fprintln(out, "nothing found.")
			return nil
		}
		for _, h := range hits {
			fmt.Fprintf(out, "%-14s %s\n", h.Ref, h.Title)
			if h.Integration != "" {
				fmt.Fprintf(out, "               %s · %s\n", h.Integration, h.URL)
			}
		}
		return nil
	}
	return fmt.Errorf("unknown task subcommand %q", sub)
}

// cmdGenkey prints a deployment encryption key. It writes nothing: where the
// key belongs is a deployment decision — a systemd EnvironmentFile, a secrets
// manager, Infisical — and a tool that guesses would be wrong most of the time.
func cmdGenkey(args []string, out io.Writer) error {
	fmt.Fprintf(out, "DECONFLICT_SECRET_KEY=%s\n", plugin.GenerateKey())
	fmt.Fprintf(out, "\nOrganization-owned content and integration credentials are encrypted with this.\n")
	fmt.Fprintf(out, "Losing it makes that data unrecoverable. Store it with this deployment's other\n")
	fmt.Fprintf(out, "secrets, not in the repository; use `deconflict rotate-key` to replace it.\n")
	return nil
}

// ---------- small helpers ----------

func encodeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func urlEscape(s string) string { return url.PathEscape(s) }

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// wrap breaks a paragraph at a column, indenting continuation lines. Terminal
// output that runs past the edge is output nobody reads.
func wrap(s string, width int, indent string) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	line := 0
	for i, w := range words {
		if i > 0 {
			if line+1+len(w) > width {
				b.WriteString("\n" + indent)
				line = 0
			} else {
				b.WriteString(" ")
				line++
			}
		}
		b.WriteString(w)
		line += len(w)
	}
	return b.String()
}
