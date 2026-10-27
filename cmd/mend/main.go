// Command mend keeps integrations with third-party APIs working. This is the
// command-line entry point; for now it exposes the change detector.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yug/mend/internal/change"
	"github.com/yug/mend/internal/specdiff"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "diff":
		err = runDiff(os.Args[2:], os.Stdout)
	case "help", "-h", "--help":
		usage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "mend: unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "mend: %v\n", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `mend: integrations that fix themselves

Usage:
  mend diff [flags] <old-spec> <new-spec>

Compares two versions of an OpenAPI description and lists what changed for
anyone who calls the API.

Flags for diff:
  -api string    provider name shown in the report (default: the new spec's title)
  -min string    lowest severity to show: info, warning, breaking (default "warning")
  -json          print machine-readable JSON instead of a report
`)
}

func runDiff(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	api := fs.String("api", "", "provider name")
	min := fs.String("min", "warning", "lowest severity to show")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: mend diff [flags] <old-spec> <new-spec>")
	}

	sev, err := parseSeverity(*min)
	if err != nil {
		return err
	}
	oldSpec, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	newSpec, err := os.ReadFile(fs.Arg(1))
	if err != nil {
		return err
	}

	changes, err := specdiff.Compare(oldSpec, newSpec, specdiff.Options{API: *api, MinSeverity: sev})
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(changes)
	}
	printReport(out, *api, changes)
	return nil
}

func parseSeverity(s string) (change.Severity, error) {
	switch strings.ToLower(s) {
	case "info":
		return change.Info, nil
	case "warning", "warn":
		return change.Warning, nil
	case "breaking", "error":
		return change.Breaking, nil
	}
	return "", fmt.Errorf("unknown severity %q (want info, warning, or breaking)", s)
}

func printReport(w io.Writer, api string, cs []change.Change) {
	if len(cs) == 0 {
		fmt.Fprintln(w, "no changes at or above the chosen severity")
		return
	}
	title := api
	if title == "" {
		title = "API"
	}
	fmt.Fprintf(w, "%s: %s -> %s\n", title, cs[0].FromVersion, cs[0].ToVersion)

	counts := map[change.Severity]int{}
	for _, c := range cs {
		counts[c.Severity]++
	}
	fmt.Fprintf(w, "%d changes: %d breaking, %d warning, %d info\n",
		len(cs), counts[change.Breaking], counts[change.Warning], counts[change.Info])

	for _, sev := range []change.Severity{change.Breaking, change.Warning, change.Info} {
		if counts[sev] == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s (%d)\n", strings.ToUpper(string(sev)), counts[sev])
		for _, c := range cs {
			if c.Severity != sev {
				continue
			}
			where := c.Operation()
			if where == "" {
				where = "(spec-wide)"
			}
			fmt.Fprintf(w, "  %s\n    %s  [%s]", where, c.Summary, c.RuleID)
			if c.Sunset != nil {
				fmt.Fprintf(w, "  sunset %s", c.Sunset.Format("2006-01-02"))
			}
			fmt.Fprintln(w)
		}
	}
}
