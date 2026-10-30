package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/yug/unbroke/internal/catalog"
	"github.com/yug/unbroke/internal/workflow"
)

// runCalls lists the endpoints a codebase calls, whether or not they changed.
func runCalls(args []string, out io.Writer) error {
	fs := newFlags("calls", "unbroke calls [flags] [repo-path]")
	only := fs.String("provider", "", "comma-separated provider ids (default: detect from the code)")
	limit := fs.Int("n", 20, "most endpoints to list per API (0 lists all)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	root := "."
	if fs.NArg() > 1 {
		return usagef("usage: unbroke calls [flags] [repo-path] (one path; got %d)", fs.NArg())
	}
	if fs.NArg() == 1 {
		root = fs.Arg(0)
	}
	if err := requireDir(root); err != nil {
		return err
	}
	if *limit < 0 {
		return usagef("-n must be 0 (all) or more")
	}
	cat, err := catalog.Load()
	if err != nil {
		return err
	}
	found, err := chooseProviders(cat, root, *only)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		fmt.Fprintln(out, "No catalogued API usage found in this repository.")
		return nil
	}
	deps, err := workflow.NewDeps()
	if err != nil {
		return err
	}
	failed := 0
	for _, d := range found {
		calls, err := workflow.Calls(context.Background(), deps, d.P, root)
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "unbroke: %s: %v\n", d.P.Name, err)
			continue
		}
		fmt.Fprintf(out, "%s: %d endpoint(s) called\n", d.P.Name, len(calls))
		for i, c := range calls {
			if *limit > 0 && i == *limit {
				fmt.Fprintf(out, "  ... and %d more\n", len(calls)-*limit)
				break
			}
			m := c.Matches[0]
			fmt.Fprintf(out, "  %-52s %3d  %s:%d  (%s, %s)\n", clipStr(c.Operation, 52), len(c.Matches), m.File, m.Line, m.Kind, m.Confidence)
		}
	}
	if failed == len(found) {
		return fmt.Errorf("could not read the endpoint list of any API this code uses")
	}
	return nil
}
