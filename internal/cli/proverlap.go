package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cloudcons/deconflict/internal/claim"
)

// PR overlap detection, which is deliberately independent of the claim
// registry.
//
// Claims require an agent to announce itself. This does not: it compares the
// changed-file set of one pull request against every other open pull request
// and says who else is in the same files. Nobody has to adopt a habit, so it
// works on day one and keeps working on the day somebody forgets. The registry
// warns before the edit; this catches what the registry missed, at the point
// the overlap becomes a fact.

const overlapMarker = "<!-- agentclaims:pr-overlap -->"

type ghPR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Draft  bool   `json:"draft"`
	State  string `json:"state"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
	HTMLURL string `json:"html_url"`
}

type ghFile struct {
	Filename string `json:"filename"`
}

type ghClient struct {
	api   string
	token string
	http  *http.Client
}

func newGH(api, token string) *ghClient {
	if api == "" {
		api = "https://api.github.com"
	}
	return &ghClient{api: strings.TrimRight(api, "/"), token: token, http: &http.Client{Timeout: 20 * time.Second}}
}

func (g *ghClient) get(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, g.api+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("GET %s: %s: %s", path, resp.Status, bytes.TrimSpace(b))
	}
	return json.Unmarshal(b, out)
}

func (g *ghClient) post(path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, g.api+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("POST %s: %s: %s", path, resp.Status, bytes.TrimSpace(rb))
	}
	return nil
}

// files lists a PR's changed files, capped. GitHub paginates at 100 and stops
// at 3000; a PR past the cap is reported as truncated rather than silently
// half-compared — a silent cap reads as "covered everything" when it did not.
func (g *ghClient) files(repo string, num, cap int) (map[string]bool, bool, error) {
	out := map[string]bool{}
	for page := 1; page <= 30; page++ {
		var batch []ghFile
		if err := g.get(fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", repo, num, page), &batch); err != nil {
			return out, false, err
		}
		for _, f := range batch {
			out[f.Filename] = true
		}
		if len(batch) < 100 {
			return out, false, nil
		}
		if len(out) >= cap {
			return out, true, nil
		}
	}
	return out, true, nil
}

func cmdPROverlap(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("pr-overlap", flag.ContinueOnError)
	repo := fs.String("repo", os.Getenv("GITHUB_REPOSITORY"), "owner/name")
	num := fs.Int("pr", 0, "pull request number")
	comment := fs.Bool("comment", false, "post the report as a PR comment")
	includeDrafts := fs.Bool("drafts", true, "include draft PRs")
	maxFiles := fs.Int("max-files", 3000, "per-PR file cap")
	api := fs.String("api", os.Getenv("GITHUB_API_URL"), "GitHub API base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *num == 0 {
		return fmt.Errorf("--repo and --pr are required")
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	g := newGH(*api, token)

	// The ignore list is operator-configured and shared with the claim
	// registry, so CI and the agents agree on what counts as noise.
	var ignore []string
	if st, err := openStore(""); err == nil {
		ignore = st.Settings().IgnorePaths
	}

	mine, truncated, err := g.files(*repo, *num, *maxFiles)
	if err != nil {
		return err
	}
	if len(mine) == 0 {
		fmt.Fprintln(out, "PR touches no files.")
		return nil
	}

	var open []ghPR
	for page := 1; page <= 10; page++ {
		var batch []ghPR
		if err := g.get(fmt.Sprintf("/repos/%s/pulls?state=open&per_page=100&page=%d", *repo, page), &batch); err != nil {
			return err
		}
		open = append(open, batch...)
		if len(batch) < 100 {
			break
		}
	}

	type hit struct {
		pr        ghPR
		shared    []string
		truncated bool
	}
	var hits []hit
	for _, p := range open {
		if p.Number == *num || (!*includeDrafts && p.Draft) {
			continue
		}
		theirs, tr, err := g.files(*repo, p.Number, *maxFiles)
		if err != nil {
			fmt.Fprintf(os.Stderr, "claims: skipping PR #%d: %v\n", p.Number, err)
			continue
		}
		var shared []string
		for f := range theirs {
			// Lockfiles and generated files collide on essentially every pair
			// of PRs. Reporting them is how a bot like this gets muted.
			if mine[f] && !claim.IsIgnored(f, ignore) {
				shared = append(shared, f)
			}
		}
		if len(shared) == 0 {
			continue
		}
		sort.Strings(shared)
		hits = append(hits, hit{pr: p, shared: shared, truncated: tr})
	}
	sort.Slice(hits, func(i, j int) bool { return len(hits[i].shared) > len(hits[j].shared) })

	if len(hits) == 0 {
		fmt.Fprintf(out, "PR #%d overlaps no other open PR (%d files compared against %d open PRs).\n",
			*num, len(mine), len(open)-1)
		return nil
	}

	var b strings.Builder
	b.WriteString(overlapMarker + "\n")
	fmt.Fprintf(&b, "**This PR touches files that %d other open PR(s) also touch.**\n\n", len(hits))
	for _, h := range hits {
		fmt.Fprintf(&b, "- **#%d** %s — @%s (`%s`) — %d shared file(s)\n",
			h.pr.Number, h.pr.Title, h.pr.User.Login, h.pr.Head.Ref, len(h.shared))
		show := h.shared
		if len(show) > 10 {
			show = show[:10]
		}
		for _, f := range show {
			fmt.Fprintf(&b, "  - `%s`\n", f)
		}
		if len(h.shared) > len(show) {
			fmt.Fprintf(&b, "  - …and %d more\n", len(h.shared)-len(show))
		}
	}
	if truncated {
		fmt.Fprintf(&b, "\n_This PR exceeds the %d-file comparison cap; the list above is incomplete._\n", *maxFiles)
	}
	b.WriteString("\nWhichever lands second will need a rebase. Agree who goes first before both are finished.\n")

	fmt.Fprint(out, b.String())
	if *comment {
		if err := g.post(fmt.Sprintf("/repos/%s/issues/%d/comments", *repo, *num),
			map[string]string{"body": b.String()}); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nposted to %s#%d\n", *repo, *num)
	}
	return exitCode{3}
}
