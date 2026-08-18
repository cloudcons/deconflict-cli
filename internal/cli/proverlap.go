package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/google/go-github/v75/github"

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

const overlapMarker = "<!-- deconflict:pr-overlap -->"

// The GitHub calls go through go-github rather than net/http.
//
// It is already a dependency for sign-in, so there is no marginal cost, and it
// removes the last hand-rolled pagination in the repository. That matters here
// specifically: this command's whole output is "which other PRs touch these
// files", and a page loop that stops early does not fail — it reports fewer
// overlaps than exist, which is the answer the reader will believe.
type ghClient struct {
	api *github.Client
}

func newGH(apiURL, token string) (*ghClient, error) {
	c := github.NewClient(nil)
	if token != "" {
		c = c.WithAuthToken(token)
	}
	// GITHUB_API_URL is set by Actions and points at Enterprise when that is
	// where the workflow runs.
	if apiURL != "" && !strings.Contains(apiURL, "api.github.com") {
		root := strings.TrimRight(apiURL, "/") + "/"
		var err error
		if c, err = c.WithEnterpriseURLs(root, root); err != nil {
			return nil, err
		}
	}
	return &ghClient{api: c}, nil
}

// splitRepo turns "owner/name" into its halves, which is what every go-github
// call wants and what CI hands us as one string.
func splitRepo(full string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(full), "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("--repo should be owner/name, got %q", full)
	}
	return owner, name, nil
}

// files lists a PR's changed files, capped. GitHub stops at 3000 files; a PR
// past the cap is reported as truncated rather than silently half-compared — a
// silent cap reads as "covered everything" when it did not.
func (g *ghClient) files(ctx context.Context, owner, name string, num, cap int) (map[string]bool, bool, error) {
	out := map[string]bool{}
	opts := &github.ListOptions{PerPage: 100}
	for {
		batch, resp, err := g.api.PullRequests.ListFiles(ctx, owner, name, num, opts)
		if err != nil {
			return out, false, err
		}
		for _, f := range batch {
			out[f.GetFilename()] = true
		}
		if len(out) >= cap {
			return out, true, nil
		}
		if resp == nil || resp.NextPage == 0 {
			return out, false, nil
		}
		opts.Page = resp.NextPage
	}
}

// openPRs lists every open pull request, following pagination to the end. The
// previous version stopped after ten pages, which on a busy repository quietly
// compared against a subset.
func (g *ghClient) openPRs(ctx context.Context, owner, name string) ([]*github.PullRequest, error) {
	var all []*github.PullRequest
	opts := &github.PullRequestListOptions{
		State:       "open",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		batch, resp, err := g.api.PullRequests.List(ctx, owner, name, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if resp == nil || resp.NextPage == 0 {
			return all, nil
		}
		opts.Page = resp.NextPage
	}
}

func (g *ghClient) comment(ctx context.Context, owner, name string, num int, body string) error {
	_, _, err := g.api.Issues.CreateComment(ctx, owner, name, num,
		&github.IssueComment{Body: github.Ptr(body)})
	return err
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
	owner, name, err := splitRepo(*repo)
	if err != nil {
		return err
	}
	g, err := newGH(*api, token)
	if err != nil {
		return err
	}
	ctx := context.Background()

	// The ignore list is operator-configured and shared with the claim
	// registry, so CI and the agents agree on what counts as noise.
	var ignore []string
	if st, err := openStore(""); err == nil {
		ignore = st.Settings().IgnorePaths
	}

	mine, truncated, err := g.files(ctx, owner, name, *num, *maxFiles)
	if err != nil {
		return err
	}
	if len(mine) == 0 {
		fmt.Fprintln(out, "PR touches no files.")
		return nil
	}

	open, err := g.openPRs(ctx, owner, name)
	if err != nil {
		return err
	}

	type hit struct {
		pr        *github.PullRequest
		shared    []string
		truncated bool
	}
	var hits []hit
	for _, p := range open {
		if p.GetNumber() == *num || (!*includeDrafts && p.GetDraft()) {
			continue
		}
		theirs, tr, err := g.files(ctx, owner, name, p.GetNumber(), *maxFiles)
		if err != nil {
			fmt.Fprintf(os.Stderr, "deconflict: skipping PR #%d: %v\n", p.GetNumber(), err)
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
			h.pr.GetNumber(), h.pr.GetTitle(), h.pr.GetUser().GetLogin(),
			h.pr.GetHead().GetRef(), len(h.shared))
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
		if err := g.comment(ctx, owner, name, *num, b.String()); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nposted to %s#%d\n", *repo, *num)
	}
	return exitCode{3}
}
