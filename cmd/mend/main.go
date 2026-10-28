// Command mend keeps integrations with third-party APIs working. This is the
// command-line entry point; for now it exposes the change detector.
package main

import (
	"encoding/json"
	"errors"
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
	case "catalog":
		err = runCatalog(os.Args[2:], os.Stdout)
	case "endpoints":
		err = runEndpoints(os.Args[2:], os.Stdout)
	case "changes":
		err = runChanges(os.Args[2:], os.Stdout)
	case "scan":
		err = runScan(os.Args[2:], os.Stdout)
	case "fix":
		err = runFix(os.Args[2:], os.Stdout)
	case "snapshot":
		err = runSnapshot(os.Args[2:], os.Stdout)
	case "ui":
		err = runUI(os.Args[2:], os.Stdout)
	case "calls":
		err = runCalls(os.Args[2:], os.Stdout)
	case "help", "-h", "--help":
		usage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "mend: unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		fmt.Fprintf(os.Stderr, "mend: %v\n", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `mend: integrations that fix themselves

Usage:
  mend catalog list [-category name]     the public APIs Mend watches
  mend catalog verify [flags]            download and parse every catalogued spec
  mend endpoints <provider> [flags]      list one provider's endpoints
  mend ui [repo-path]                    open the web interface
  mend calls [flags] [repo-path]         list the API endpoints this codebase calls
  mend scan [flags] [repo-path]          find which API changes affect this codebase
  mend fix [flags] [repo-path]           propose the code changes that keep this codebase working
  mend snapshot [provider...]            save today's copy of specs that publish no history
  mend changes <provider> [flags]        what changed in a provider's API since 6 months ago (or -since)
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

	changes, note, err := specdiff.CompareResilient(oldSpec, newSpec, specdiff.Options{API: *api, MinSeverity: sev})
	if err != nil {
		return err
	}
	if note != "" {
		fmt.Fprintln(os.Stderr, "mend: "+note)
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

		// Changes that hit exactly one operation are grouped under it, so a
		// restructured endpoint reads as one block instead of a scattered list.
		// Changes shared by several operations name how many they reach.
		var shared []change.Change
		lastOp := ""
		for _, c := range cs {
			if c.Severity != sev {
				continue
			}
			if len(c.Operations) > 1 {
				shared = append(shared, c)
				continue
			}
			op := c.Operation()
			if op == "" {
				op = "(spec-wide)"
			}
			if op != lastOp {
				fmt.Fprintf(w, "  %s\n", op)
				lastOp = op
			}
			fmt.Fprintf(w, "    %s\n", describe(c))
		}
		for _, c := range shared {
			fmt.Fprintf(w, "  %s\n", describe(c))
			fmt.Fprintf(w, "    affects %d operations: %s\n", len(c.Operations), preview(c.Operations, 4))
		}
	}
}

func describe(c change.Change) string {
	s := c.Summary + "  [" + c.RuleID + "]"
	if c.Sunset != nil {
		s += "  sunset " + c.Sunset.Format("2006-01-02")
	}
	return s
}

// preview lists the first n items and says how many more there are.
func preview(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(items[:n], ", "), len(items)-n)
}
