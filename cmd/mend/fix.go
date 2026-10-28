package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yug/mend/internal/affect"
	"github.com/yug/mend/internal/catalog"
	"github.com/yug/mend/internal/fix"
	"github.com/yug/mend/internal/llm"
)

func runFix(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("fix", flag.ContinueOnError)
	since := fs.String("since", "6m", "how far back to compare: 90d, 12w, 6m, 1y")
	only := fs.String("provider", "", "provider id to fix for (required if the code uses several)")
	min := fs.String("min", "warning", "lowest severity of API change to consider")
	testCmd := fs.String("test", "", "command that runs the project's tests, e.g. \"npm test\"")
	noTests := fs.Bool("no-tests", false, "make the change without running tests")
	dryRun := fs.Bool("dry-run", false, "show what would be sent to the model, and change nothing")
	openPR := fs.Bool("open-pr", false, "push the branch and open a pull request on GitHub (needs GITHUB_TOKEN)")
	branch := fs.String("branch", "", "branch name (default mend/<provider>-<date>)")
	base := fs.String("base", "", "branch the pull request targets (default: the repository's default branch)")
	maxFiles := fs.Int("max-files", 5, "most files to change in one run")
	allowDirty := fs.Bool("allow-dirty", false, "start even with uncommitted changes")
	prBody := fs.String("pr-body", "", "also write the pull request description to this file")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	root := "."
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: mend fix [flags] [repo-path]")
	}
	if fs.NArg() == 1 {
		root = fs.Arg(0)
	}
	if !*dryRun && *testCmd == "" && !*noTests {
		return errors.New("say how to check the change: -test \"<command>\" runs your tests and discards the change if they fail; -no-tests skips that")
	}
	dur, err := parseSince(*since)
	if err != nil {
		return err
	}
	sev, err := parseSeverity(*min)
	if err != nil {
		return err
	}

	cat, err := catalog.Load()
	if err != nil {
		return err
	}
	found, err := chooseProviders(cat, root, *only)
	if err != nil {
		return err
	}
	switch {
	case len(found) == 0:
		fmt.Fprintln(out, "No catalogued API usage found in this repository; nothing to fix.")
		return nil
	case len(found) > 1:
		var names []string
		for _, d := range found {
			names = append(names, d.p.ID)
		}
		return fmt.Errorf("this code uses several APIs (%s); choose one with -provider, so each pull request stays about one API", strings.Join(names, ", "))
	}
	p := found[0].p

	deps, err := newTrackDeps()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "scanning %s for %s...\n", root, p.Name)
	sr, err := scanProvider(context.Background(), deps, p, root, dur, sev)
	if err != nil {
		return err
	}
	tasks := fix.Plan(sr.Findings, sr.Extra, *maxFiles)
	if len(tasks) == 0 {
		fmt.Fprintf(out, "Nothing to fix: none of %s's %d change(s) affect code in this repository.\n", p.Name, sr.Total)
		return nil
	}
	fmt.Fprintf(out, "%d file(s) have code affected by %s changes:\n", len(tasks), p.Name)
	for _, t := range tasks {
		fmt.Fprintf(out, "  %s  (%d call site(s))\n", t.File, len(t.Hits))
	}

	if *dryRun {
		rep, err := fix.Run(context.Background(), tasks, fix.RunOptions{Root: root, Provider: p.Name, DryRun: true})
		if err != nil {
			return err
		}
		files := make([]string, 0, len(rep.Prompts))
		for f := range rep.Prompts {
			files = append(files, f)
		}
		sort.Strings(files)
		fmt.Fprintf(out, "\nDry run. Nothing was sent anywhere and nothing was changed. This is what the model would be given:\n")
		for _, f := range files {
			fmt.Fprintf(out, "\n----- %s -----\n%s", f, rep.Prompts[f])
		}
		return nil
	}

	model, err := llm.FromEnv()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nSending these files to %s (model %s). Nothing else leaves your machine.\n", model.BaseURL, model.Model)
	rep, err := fix.Run(context.Background(), tasks, fix.RunOptions{
		Root: root, Provider: p.Name, Model: model, TestCmd: *testCmd, Branch: *branch, AllowDirty: *allowDirty,
		Progress: func(s string) { fmt.Fprintln(os.Stderr, "  "+s) },
	})
	if err != nil {
		return err
	}

	printFixReport(out, rep)
	if rep.Branch == "" {
		return nil
	}
	body := fix.PRBody(p.Name, sr.Since, rep, tasks)
	if *prBody != "" {
		if err := os.WriteFile(*prBody, []byte(body), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nPull request description written to %s\n", *prBody)
	}
	if !*openPR {
		fmt.Fprintf(out, "\nReview with: git diff %s...%s\nNothing has been pushed. Add -open-pr to push the branch and open a pull request.\n", rep.BaseBranch, rep.Branch)
		if *prBody == "" {
			fmt.Fprintf(out, "\n--- pull request description ---\n%s", body)
		}
		return nil
	}
	return openPullRequest(out, root, rep, *base, fix.Title(p.Name), body)
}

func printFixReport(w io.Writer, rep *fix.Report) {
	fmt.Fprintln(w)
	for _, f := range rep.Files {
		switch {
		case f.Err != nil:
			fmt.Fprintf(w, "  skipped  %s: %v\n", f.File, f.Err)
		case f.Changed():
			fmt.Fprintf(w, "  changed  %s (%s confidence): %s\n", f.File, f.Confidence, f.Explanation)
		default:
			reason := f.Explanation
			if len(f.Rejected) > 0 {
				reason = strings.Join(f.Rejected, "; ")
			}
			fmt.Fprintf(w, "  left     %s: %s\n", f.File, reason)
		}
	}
	switch {
	case rep.Discarded != "":
		fmt.Fprintf(w, "\nNo change was kept: %s.\n", rep.Discarded)
		if rep.TestsRan && !rep.TestsPass {
			fmt.Fprintf(w, "Test output (tail):\n%s\n", indent(rep.TestOutput))
		}
	case rep.TestsRan:
		fmt.Fprintf(w, "\nTests passed (%s). Committed %s on branch %s.\n", rep.TestCmd, rep.Commit, rep.Branch)
	default:
		fmt.Fprintf(w, "\nCommitted %s on branch %s. Tests were not run.\n", rep.Commit, rep.Branch)
	}
}

func indent(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

func openPullRequest(out io.Writer, root string, rep *fix.Report, base, title, body string) error {
	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if token == "" {
		return errors.New("-open-pr needs GITHUB_TOKEN (a token you created, with permission to push and open pull requests)")
	}
	remote, err := gitOutput(root, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("no 'origin' remote: %w", err)
	}
	owner, repo, err := fix.ParseRemote(remote)
	if err != nil {
		return err
	}
	gh := fix.NewGitHub(token)
	ctx := context.Background()
	if base == "" {
		if base, err = gh.DefaultBranch(ctx, owner, repo); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "\nPushing %s to origin (%s/%s)...\n", rep.Branch, owner, repo)
	if o, err := gitOutput(root, "push", "-u", "origin", rep.Branch); err != nil {
		return fmt.Errorf("push failed: %w\n%s", err, o)
	}
	url, err := gh.OpenPR(ctx, owner, repo, rep.Branch, base, title, body)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Opened %s\n", url)
	return nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = filepath.Clean(dir)
	b, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(b)), err
}

var _ = affect.ChangedField
