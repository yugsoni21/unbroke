package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yug/mend/internal/catalog"
	"github.com/yug/mend/internal/workflow"
)

// runCalls lists the endpoints a codebase calls, whether or not they changed.
func runCalls(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("calls", flag.ContinueOnError)
	only := fs.String("provider", "", "comma-separated provider ids (default: detect from the code)")
	limit := fs.Int("n", 20, "most endpoints to list per API")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	root := "."
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: mend calls [flags] [repo-path]")
	}
	if fs.NArg() == 1 {
		root = fs.Arg(0)
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	cat, err := catalog.Load()
	if err != nil {
		return err
	}
	found, err := workflow.Choose(cat, root, *only)
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
	for _, d := range found {
		calls, err := workflow.Calls(context.Background(), deps, d.P, root)
		if err != nil {
			fmt.Fprintf(out, "%s: %v\n", d.P.Name, err)
			continue
		}
		fmt.Fprintf(out, "%s: %d endpoint(s) called\n", d.P.Name, len(calls))
		for i, c := range calls {
			if i == *limit {
				fmt.Fprintf(out, "  ... and %d more\n", len(calls)-*limit)
				break
			}
			m := c.Matches[0]
			fmt.Fprintf(out, "  %-52s %3d  %s:%d  (%s, %s)\n", clipStr(c.Operation, 52), len(c.Matches), m.File, m.Line, m.Kind, m.Confidence)
		}
	}
	return nil
}

func clipStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n-1]) + "…"
}
