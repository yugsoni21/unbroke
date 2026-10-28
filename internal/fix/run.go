package fix

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/yug/unbroke/internal/llm"
)

// RunOptions configure a fix run.
type RunOptions struct {
	Root     string
	Provider string
	Model    llm.Completer
	// TestCmd is run through the shell in Root after the edits. Empty means the
	// tests are not run, and the report says so.
	TestCmd string
	// Branch is the branch to create. Empty picks unbroke/<provider>-<date>.
	Branch string
	// AllowDirty permits starting with uncommitted changes in the tree.
	AllowDirty bool
	// DryRun builds the prompts and stops before calling the model or changing
	// anything.
	DryRun   bool
	Progress func(string)
}

// Report is the outcome of a run.
type Report struct {
	BaseBranch string
	Branch     string
	Commit     string
	Files      []FileResult
	TestCmd    string
	TestsRan   bool
	TestsPass  bool
	TestOutput string
	// Discarded explains why finished work was thrown away, if it was.
	Discarded string
	// Prompts holds what would be sent, for a dry run.
	Prompts map[string]string
}

// Changed lists the files that were modified.
func (r *Report) Changed() []string {
	var out []string
	for _, f := range r.Files {
		if f.Changed() {
			out = append(out, f.File)
		}
	}
	return out
}

func (o RunOptions) say(format string, a ...any) {
	if o.Progress != nil {
		o.Progress(fmt.Sprintf(format, a...))
	}
}

// Run applies the tasks on a new branch.
func Run(ctx context.Context, tasks []FileTask, o RunOptions) (*Report, error) {
	rep := &Report{Prompts: map[string]string{}, TestCmd: o.TestCmd}

	if o.DryRun {
		for _, t := range tasks {
			p, err := BuildPrompt(o.Root, t, o.Provider)
			if err != nil {
				return rep, err
			}
			rep.Prompts[t.File] = p
		}
		return rep, nil
	}
	if o.Model == nil {
		return rep, errors.New("no model configured")
	}

	if _, err := git(ctx, o.Root, "rev-parse", "--is-inside-work-tree"); err != nil {
		return rep, errors.New("not a git repository; Unbroke makes its changes on a branch so you can review and undo them")
	}
	base, err := git(ctx, o.Root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return rep, err
	}
	rep.BaseBranch = base
	if !o.AllowDirty {
		if status, _ := git(ctx, o.Root, "status", "--porcelain"); status != "" {
			return rep, errors.New("the working tree has uncommitted changes; commit or stash them first (or pass -allow-dirty)")
		}
	}
	branch := o.Branch
	if branch == "" {
		branch = fmt.Sprintf("unbroke/%s-%s", strings.ToLower(strings.Fields(o.Provider)[0]), time.Now().Format("2006-01-02"))
	}
	if _, err := git(ctx, o.Root, "checkout", "-b", branch); err != nil {
		return rep, fmt.Errorf("create branch %s: %w", branch, err)
	}
	rep.Branch = branch

	// abandon puts the repository back exactly as it was found.
	abandon := func(reason string) {
		_, _ = git(ctx, o.Root, "checkout", "--", ".")
		_, _ = git(ctx, o.Root, "checkout", base)
		_, _ = git(ctx, o.Root, "branch", "-D", branch)
		rep.Discarded = reason
		rep.Branch = ""
	}

	for _, t := range tasks {
		o.say("asking the model about %s (%d call site(s))", t.File, len(t.Hits))
		res := FileResult{File: t.File}
		prompt, err := BuildPrompt(o.Root, t, o.Provider)
		if err == nil {
			var reply string
			reply, err = o.Model.Complete(ctx, SystemPrompt, prompt)
			if err == nil {
				var p Proposal
				if p, err = ParseProposal(reply); err == nil {
					res, err = Apply(o.Root, t.File, p)
				}
			}
		}
		if err != nil {
			res.File, res.Err = t.File, err
		}
		rep.Files = append(rep.Files, res)
	}

	changed := rep.Changed()
	if len(changed) == 0 {
		abandon("the model proposed no edits Unbroke could apply safely")
		return rep, nil
	}

	if o.TestCmd != "" {
		o.say("running tests: %s", o.TestCmd)
		out, err := runShell(ctx, o.Root, o.TestCmd, 10*time.Minute)
		rep.TestsRan, rep.TestsPass, rep.TestOutput = true, err == nil, out
		if err != nil {
			abandon("the project's tests failed after the edits, so they were discarded")
			return rep, nil
		}
	}

	args := append([]string{"add", "--"}, changed...)
	if _, err := git(ctx, o.Root, args...); err != nil {
		return rep, err
	}
	if _, err := git(ctx, o.Root, "commit", "-q", "-m", commitMessage(o.Provider, rep.Files)); err != nil {
		return rep, fmt.Errorf("commit: %w (is git user.name / user.email set?)", err)
	}
	rep.Commit, _ = git(ctx, o.Root, "rev-parse", "--short", "HEAD")
	return rep, nil
}

func commitMessage(provider string, files []FileResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Update %s integration for API changes\n\n", provider)
	for _, f := range files {
		if f.Changed() {
			fmt.Fprintf(&b, "- %s: %s\n", f.File, f.Explanation)
		}
	}
	b.WriteString("\nProposed by Unbroke. Review before merging.\n")
	return b.String()
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s := strings.TrimSpace(out.String())
	if err != nil {
		return s, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, s)
	}
	return s, nil
}

// runShell runs a command line and returns the tail of its combined output.
func runShell(ctx context.Context, dir, command string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s := out.String()
	if len(s) > 4000 {
		s = "..." + s[len(s)-4000:]
	}
	if ctx.Err() == context.DeadlineExceeded {
		return s, fmt.Errorf("timed out after %s", timeout)
	}
	return s, err
}
