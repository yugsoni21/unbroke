package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/yug/unbroke/internal/catalog"
	"github.com/yug/unbroke/internal/workflow"
)

// usageError is a mistake in how the command was invoked. It exits with status
// 2, the convention for bad usage, so a CI job can tell "you called it wrong"
// from "it ran and failed" (1) and from the scan gate's own statuses (3, 4).
type usageError struct {
	msg string
	// shown is true when the flag package has already printed the problem and
	// the flag list, so main prints nothing more.
	shown bool
}

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{msg: fmt.Sprintf(format, a...)} }

// newFlags returns a flag set whose -h output starts with the command's
// synopsis instead of Go's generic "Usage of scan:".
func newFlags(name, synopsis string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		w := fs.Output()
		fmt.Fprintf(w, "usage: %s\n", synopsis)
		hasFlags := false
		fs.VisitAll(func(*flag.Flag) { hasFlags = true })
		if hasFlags {
			fmt.Fprintln(w, "\nflags:")
			fs.PrintDefaults()
		}
	}
	return fs
}

// parseFlags parses args (flags may follow positional arguments). -h is passed
// through as flag.ErrHelp; any other problem has already been printed by the
// flag package and becomes a usage error.
func parseFlags(fs *flag.FlagSet, args []string) error {
	err := fs.Parse(reorder(fs, args))
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return err
	}
	return usageError{msg: err.Error(), shown: true}
}

// exitCode prints err the way main should and returns the process status.
func exitCode(err error, stderr io.Writer) int {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return 0
	}
	var ee exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	var ue usageError
	if errors.As(err, &ue) {
		if !ue.shown {
			fmt.Fprintf(stderr, "unbroke: %v\n", err)
		}
		return 2
	}
	fmt.Fprintf(stderr, "unbroke: %v\n", err)
	return 1
}

// requireDir checks that a repository path names a directory, and says which
// way it is wrong when it is not.
func requireDir(path string) error {
	st, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%s does not exist", path)
	case err != nil:
		return err
	case !st.IsDir():
		return fmt.Errorf("%s is a file, not a directory; give the folder that holds the code", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", path, err)
	}
	f.Close()
	return nil
}

// clipStr shortens s to at most n characters, never splitting one.
func clipStr(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// chooseProviders is workflow.Choose with an unknown provider id reported as a
// usage mistake.
func chooseProviders(cat *catalog.Catalog, root, only string) ([]workflow.Detected, error) {
	found, err := workflow.Choose(cat, root, only)
	if errors.Is(err, workflow.ErrUnknownProvider) {
		return nil, usageError{msg: err.Error()}
	}
	return found, err
}
