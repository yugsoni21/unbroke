package fix

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yug/unbroke/internal/llm"
)

// ErrDirty means the working tree has uncommitted changes and the run was not
// allowed to start anyway.
var ErrDirty = errors.New("the working tree has uncommitted changes; commit or stash them first")

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
	// AllowDirty permits starting with uncommitted changes in the tree. Files
	// with uncommitted changes are never edited.
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
	// TestTouched lists files the test command modified, which were put back.
	TestTouched []string
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

// Run applies the tasks on a new branch and returns the repository to the
// branch it started on, with the working tree exactly as it was found.
//
// The rules that keep it safe:
//   - It only ever writes files it was asked to fix, and refuses any that already
//     have uncommitted changes, so a person's work in progress is never edited,
//     committed or thrown away.
//   - Undoing means writing back the exact bytes it read, never a blanket
//     "git checkout -- .".
//   - Clean-up runs even if the caller's context is cancelled or a panic occurs.
func Run(ctx context.Context, tasks []FileTask, o RunOptions) (rep *Report, err error) {
	rep = &Report{Prompts: map[string]string{}, TestCmd: o.TestCmd}

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

	base, err := checkRepo(ctx, o.Root)
	if err != nil {
		return rep, err
	}
	rep.BaseBranch = base

	// Git reports paths from the top of the repository, while tasks name files
	// from Root, which may be a subdirectory of it. Both are compared in Root's
	// terms, or a dirty file below a subdirectory would look clean and be edited.
	prefix, err := gitRaw(ctx, o.Root, "rev-parse", "--show-prefix")
	if err != nil {
		return rep, err
	}
	prefix = strings.TrimSpace(prefix)
	top, err := git(ctx, o.Root, "rev-parse", "--show-toplevel")
	if err != nil {
		return rep, err
	}
	dirtyTop, err := dirtyFiles(ctx, o.Root)
	if err != nil {
		return rep, err
	}
	dirty := map[string]bool{}
	for f := range dirtyTop {
		if rel, ok := strings.CutPrefix(f, prefix); ok {
			dirty[rel] = true
		}
	}
	if len(dirtyTop) > 0 && !o.AllowDirty {
		return rep, ErrDirty
	}

	branch, err := freeBranch(ctx, o.Root, o.Branch, o.Provider)
	if err != nil {
		return rep, err
	}
	if _, err := git(ctx, o.Root, "checkout", "-q", "-b", branch); err != nil {
		return rep, fmt.Errorf("create branch %s: %w", branch, err)
	}
	rep.Branch = branch

	// Clean-up must survive a cancelled request or a closed browser tab.
	cleanCtx, cancelClean := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancelClean()

	originals := map[string][]byte{} // file -> bytes before Unbroke touched it
	finished := false

	// back returns to the starting branch. If keep is false it first restores
	// every file Unbroke wrote and deletes the new branch.
	back := func(keep bool, reason string) error {
		var problems []string
		if !keep {
			for file, data := range originals {
				if err := restoreFile(o.Root, file, data); err != nil {
					problems = append(problems, err.Error())
				}
			}
			if len(originals) > 0 {
				args := append([]string{"reset", "-q", "--"}, keys(originals)...)
				_, _ = git(cleanCtx, o.Root, args...)
			}
		}
		if _, err := git(cleanCtx, o.Root, "checkout", "-q", base); err != nil {
			problems = append(problems, err.Error())
		}
		if !keep {
			if _, err := git(cleanCtx, o.Root, "branch", "-q", "-D", branch); err != nil {
				problems = append(problems, err.Error())
			}
			rep.Branch = ""
			rep.Discarded = reason
		}
		finished = true
		if len(problems) > 0 {
			return fmt.Errorf("could not fully restore the repository (%s); check `git status` and `git branch`", strings.Join(problems, "; "))
		}
		return nil
	}
	defer func() {
		if finished {
			return
		}
		if r := recover(); r != nil {
			_ = back(false, "an internal error stopped the run")
			panic(r)
		}
		if berr := back(false, "the run stopped before it finished"); err == nil {
			err = berr
		}
	}()
	discard := func(reason string, cause error) (*Report, error) {
		if berr := back(false, reason); berr != nil {
			return rep, berr
		}
		return rep, cause
	}

	for _, t := range tasks {
		res := FileResult{File: t.File}
		if dirty[t.File] {
			res.Err = errors.New("this file has uncommitted changes, so Unbroke left it alone")
			rep.Files = append(rep.Files, res)
			continue
		}
		if ctx.Err() != nil {
			res.Err = ctx.Err()
			rep.Files = append(rep.Files, res)
			continue
		}
		o.say("asking the model about %s (%d call site(s))", t.File, len(t.Hits))
		var data []byte
		var path string
		if path, err = safeJoin(o.Root, t.File); err == nil {
			data, err = os.ReadFile(path)
		}
		if err == nil {
			var prompt, reply string
			prompt, err = BuildPrompt(o.Root, t, o.Provider)
			if err == nil {
				reply, err = o.Model.Complete(ctx, SystemPrompt, prompt)
			}
			if err == nil {
				var p Proposal
				if p, err = ParseProposal(reply); err == nil {
					originals[t.File] = data
					res, err = Apply(o.Root, t.File, p)
					if err == nil && !res.Changed() {
						delete(originals, t.File)
					}
				}
			}
		}
		if err != nil {
			res.File, res.Err = t.File, err
		}
		rep.Files = append(rep.Files, res)
	}
	err = nil

	if ctx.Err() != nil {
		return discard("the run was cancelled, so its edits were discarded", nil)
	}
	changed := rep.Changed()
	if len(changed) == 0 {
		reason := "the model proposed no edits Unbroke could apply safely"
		if allErrored(rep.Files) {
			// Every file failed (a bad API key, the model unreachable): saying
			// the model proposed nothing would hide the real problem.
			reason = "no file could be processed: " + rep.Files[0].Err.Error()
		}
		return discard(reason, nil)
	}

	if o.TestCmd != "" {
		o.say("running tests: %s", o.TestCmd)
		out, terr := runShell(ctx, o.Root, o.TestCmd, 10*time.Minute)
		rep.TestsRan, rep.TestsPass, rep.TestOutput = true, terr == nil, out
		if ctx.Err() != nil {
			rep.TestsPass = false
			return discard("the run was cancelled while the tests were running, so its edits were discarded", nil)
		}
		if terr != nil {
			return discard("the project's tests failed after the edits, so they were discarded", nil)
		}
		// Files the test command itself modified (formatters, snapshots) were
		// clean before the run, so they go back to how they were.
		mine := map[string]bool{}
		for f := range originals {
			mine[prefix+f] = true
		}
		if touched, err := touchedByTests(cleanCtx, top, dirtyTop, mine); err == nil && len(touched) > 0 {
			args := append([]string{"checkout", "-q", "--"}, touched...)
			if _, err := git(cleanCtx, top, args...); err == nil {
				rep.TestTouched = touched
			}
		}
	}

	args := append([]string{"add", "--"}, changed...)
	if out, err := git(cleanCtx, o.Root, args...); err != nil {
		return discard("git could not stage the edits, so they were discarded", fmt.Errorf("git add failed: %s", out))
	}
	// Naming the files limits the commit to Unbroke's edits. Without it, changes
	// the user had staged (with -allow-dirty) would be swept into the commit and
	// vanish from their working tree when Unbroke switches back.
	commitArgs := append([]string{"commit", "-q", "-m", commitMessage(o.Provider, rep.Files), "--"}, changed...)
	if out, err := git(cleanCtx, o.Root, commitArgs...); err != nil {
		msg := "git commit failed: " + out
		if strings.Contains(out, "tell me who you are") || strings.Contains(out, "user.email") {
			msg += " (set git user.name and user.email)"
		}
		return discard("git refused the commit, so the edits were discarded", errors.New(msg))
	}
	rep.Commit, _ = git(cleanCtx, o.Root, "rev-parse", "--short", "HEAD")
	if err := back(true, ""); err != nil {
		return rep, err
	}
	return rep, nil
}

func allErrored(files []FileResult) bool {
	for _, f := range files {
		if f.Err == nil {
			return false
		}
	}
	return len(files) > 0
}

// Preflight reports, before any slow work, whether Run could start in root:
// it must be a git repository on a branch, with a clean working tree unless
// allowDirty is set. Run checks all of this again itself.
func Preflight(ctx context.Context, root string, allowDirty bool) error {
	if _, err := checkRepo(ctx, root); err != nil {
		return err
	}
	if allowDirty {
		return nil
	}
	dirty, err := dirtyFiles(ctx, root)
	if err != nil {
		return err
	}
	if len(dirty) > 0 {
		return ErrDirty
	}
	return nil
}

// checkRepo returns the branch root is on, or why Run cannot work there.
func checkRepo(ctx context.Context, root string) (string, error) {
	if _, err := git(ctx, root, "rev-parse", "--is-inside-work-tree"); err != nil {
		return "", errors.New("not a git repository; Unbroke makes its changes on a branch so you can review and undo them")
	}
	base, err := git(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		if strings.Contains(err.Error(), "unknown revision") || strings.Contains(err.Error(), "ambiguous argument 'HEAD'") {
			return "", errors.New("the repository has no commits yet; commit your code first, so Unbroke has something to branch from")
		}
		return "", err
	}
	if base == "HEAD" {
		return "", errors.New("the repository is on a detached HEAD; check out a branch first, so Unbroke has somewhere to come back to")
	}
	// Found now, not after the model has been paid for and the tests have run.
	if _, err := git(ctx, root, "var", "GIT_COMMITTER_IDENT"); err != nil {
		return "", errors.New("git does not know who you are, so it cannot commit; set git user.name and user.email")
	}
	return base, nil
}

// dirtyFiles lists files with uncommitted changes (staged, unstaged or
// untracked), relative to the top of the repository.
func dirtyFiles(ctx context.Context, root string) (map[string]bool, error) {
	out, err := gitRaw(ctx, root, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	files := map[string]bool{}
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		files[e[3:]] = true
		// A rename or copy is followed by its original name.
		if e[0] == 'R' || e[0] == 'C' {
			i++
		}
	}
	return files, nil
}

// touchedByTests lists tracked files that became modified during the tests and
// are neither Unbroke's edits nor already dirty before the run. top is the
// repository's top directory, and all paths are relative to it.
func touchedByTests(ctx context.Context, top string, before map[string]bool, mine map[string]bool) ([]string, error) {
	now, err := gitRaw(ctx, top, "diff", "--name-only", "-z")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range strings.Split(now, "\x00") {
		if f == "" || before[f] {
			continue
		}
		if mine[f] {
			continue
		}
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
}

// freeBranch returns the requested branch name, or unbroke/<provider>-<date>
// with a numeric suffix if that already exists.
func freeBranch(ctx context.Context, root, want, provider string) (string, error) {
	exists := func(b string) bool {
		_, err := git(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+b)
		return err == nil
	}
	if want != "" {
		if exists(want) {
			return "", fmt.Errorf("branch %s already exists", want)
		}
		return want, nil
	}
	name := fmt.Sprintf("unbroke/%s-%s", slug(provider), time.Now().Format("2006-01-02"))
	if !exists(name) {
		return name, nil
	}
	for i := 2; i < 100; i++ {
		if c := fmt.Sprintf("%s-%d", name, i); !exists(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("too many %s branches; delete old ones", name)
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "api"
	}
	return out
}

func restoreFile(root, file string, data []byte) error {
	path, err := safeJoin(root, file)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	mode := os.FileMode(0o644)
	if err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, data, mode)
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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

// gitRaw runs git and returns its standard output untrimmed, for -z output.
func gitRaw(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
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
func runShell(parent context.Context, dir, command string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	ownProcessGroup(cmd)
	// If a process outlives the kill and keeps the output pipe open, stop
	// waiting for it rather than hanging the whole run.
	cmd.WaitDelay = 5 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s := out.String()
	if len(s) > 4000 {
		s = s[len(s)-4000:]
		// Start on a whole character, not in the middle of one.
		for len(s) > 0 && !utf8.RuneStart(s[0]) {
			s = s[1:]
		}
		s = "..." + s
	}
	if parent.Err() != nil {
		return s, parent.Err()
	}
	if ctx.Err() == context.DeadlineExceeded {
		return s, fmt.Errorf("timed out after %s", timeout)
	}
	return s, err
}
