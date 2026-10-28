#!/usr/bin/env python3
"""Run `unbroke changes` across every catalogued provider that has public history
and write docs/benchmark.md from the results.

    go build -o bin/unbroke ./cmd/unbroke
    python3 scripts/benchmark.py            # run everything, write the report
    python3 scripts/benchmark.py --report   # rewrite the report from the last run

Every figure in the report is computed here from the raw JSON in bench/, so it
can be regenerated and checked. Microsoft Graph is skipped: its spec is 45 MB
and needs about 900 MB of memory to compare.
"""
import argparse, datetime, glob, json, os, re, subprocess, sys, time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
UNBROKE = os.environ.get("UNBROKE", os.path.join(ROOT, "bin", "unbroke"))
OUT = os.path.join(ROOT, "bench")
DOC = os.path.join(ROOT, "docs", "benchmark.md")
SKIP = {"msgraph"}


def catalog_rows():
    """Rows of `unbroke catalog list`: id, name, company, category, specs, history."""
    out = subprocess.run([UNBROKE, "catalog", "list"], capture_output=True, text=True, check=True).stdout
    rows = []
    for line in out.splitlines()[1:]:
        cols = re.split(r"\s{2,}", line.strip())
        if len(cols) == 6:
            rows.append(cols)
    return rows


def providers():
    return [r[0] for r in catalog_rows() if r[5] == "git" and r[0] not in SKIP]


def run(ids, since):
    os.makedirs(OUT, exist_ok=True)
    for pid in ids:
        t = time.time()
        try:
            r = subprocess.run([UNBROKE, "changes", pid, "-since", since, "-min", "info", "-json"],
                               capture_output=True, text=True, timeout=600)
            open(os.path.join(OUT, pid + ".json"), "w").write(r.stdout)
            status = "ok" if r.returncode == 0 else "exit %d: %s" % (r.returncode, r.stderr.strip()[:120])
        except subprocess.TimeoutExpired:
            status = "TIMEOUT"
        print("%-14s %6.1fs  %s" % (pid, time.time() - t, status), flush=True)


def load():
    rows = []
    for path in sorted(glob.glob(os.path.join(OUT, "*.json"))):
        pid = os.path.basename(path)[:-5]
        try:
            d = json.load(open(path))
        except ValueError:
            continue
        rows.append((pid, d))
    return rows


def analyze(rows):
    res = []
    for pid, d in rows:
        b = w = i = 0
        ops = set()
        reduced, moved, errors = [], [], []
        for s in d["specs"]:
            label = s["spec"] if len(d["specs"]) > 1 else ""
            if s.get("note"):
                reduced.append(label or "REST API")
            if s.get("new_spec"):
                moved.append(label or "REST API")
            if s.get("error"):
                errors.append(s["error"])
            for c in s["changes"]:
                if c["severity"] == "breaking":
                    b += 1
                elif c["severity"] == "warning":
                    w += 1
                else:
                    i += 1
                if c["severity"] != "info":
                    ops.update(c.get("operations") or [])
        res.append(dict(id=pid, since=d.get("from_date", ""), specs=len(d["specs"]), breaking=b, warning=w,
                        info=i, ops=len(ops), reduced=reduced, moved=moved, errors=errors))
    return res


def report(res):
    today = datetime.date.today().isoformat()
    names = {r[0]: r[1] for r in catalog_rows()}
    total_b = sum(r["breaking"] for r in res)
    total_w = sum(r["warning"] for r in res)
    specs = sum(r["specs"] for r in res)
    ops = sum(r["ops"] for r in res)
    with_break = sum(1 for r in res if r["breaking"])
    reduced = [(r["id"], s) for r in res for s in r["reduced"]]
    stale = [r for r in res if r["since"] and r["since"] < "2026-01-01"]
    moved = [(r["id"], s) for r in res for s in r["moved"]]
    failed = [(r["id"], e) for r in res for e in r["errors"]]

    L = []
    L.append("# Benchmark: six months of real API changes\n")
    L.append("Generated %s by `scripts/benchmark.py`. Every figure below is computed from the raw output in `bench/`.\n" % today)
    L.append("## What was run\n")
    L.append("For each catalogued API whose spec lives in a public GitHub repository, Unbroke compared the spec as it stood about six months ago with the spec today (`unbroke changes <provider> -since 6m`). No monitoring was running during that time; the history comes from the providers' own repositories.\n")
    L.append("## Headline\n")
    L.append("- **%d providers, %d spec files** compared, **%d failures**." % (len(res), specs, len(failed)))
    L.append("- **%d providers** had at least one breaking change; **%s breaking** and **%s warning** records in total, touching **%s endpoints**." % (with_break, format(total_b, ","), format(total_w, ","), format(ops, ",")))
    L.append("- Informational changes (new optional fields, new response values) are recorded but hidden by default.\n")
    L.append("## By provider\n")
    L.append("| Provider | Compared since | Spec files | Breaking | Warning | Endpoints affected | Notes |")
    L.append("|---|---|---:|---:|---:|---:|---|")
    for r in sorted(res, key=lambda r: (-r["breaking"], -r["warning"], r["id"])):
        notes = []
        if r["reduced"]:
            notes.append("reduced comparison for %d spec file(s)" % len(r["reduced"]))
        if r["moved"]:
            notes.append("%d spec file(s) absent then" % len(r["moved"]))
        if r["since"] and r["since"] < "2026-01-01":
            notes.append("repo idle since %s" % r["since"])
        L.append("| %s | %s | %d | %d | %d | %d | %s |" % (names.get(r["id"], r["id"]), r["since"], r["specs"], r["breaking"], r["warning"], r["ops"], "; ".join(notes)))
    L.append("")
    L.append("## Read this before quoting the numbers\n")
    L.append("- **Breaking means a caller might break, not that it did.** Unbroke rates changes from the point of view of someone calling the API. A spec comparison cannot know whether your code uses the changed endpoint; that is what the repo scanner is for. A zero is also not proof of stability: a spec diff sees only what the provider documented.")
    if reduced:
        L.append("- **%d of %d spec files got a reduced comparison.** The provider's published spec was malformed (broken references, wrong field types, duplicate endpoints), so Unbroke compared endpoint lists only: removed and newly deprecated endpoints, but nothing inside requests and responses. Affected: %s." % (len(reduced), specs, ", ".join(p if s == "REST API" else "%s/%s" % (p, s) for p, s in reduced)))
    if stale:
        L.append("- **Some repositories had not been updated for a long time**, so \"six months ago\" resolved to an older commit: %s." % ", ".join("%s (%s)" % (r["id"], r["since"]) for r in stale))
    if moved:
        L.append("- **%d spec files did not exist at the earlier date.** They are new, or they moved: %s." % (len(moved), ", ".join(p if s == "REST API" else "%s/%s" % (p, s) for p, s in moved)))
    if failed:
        L.append("- **Failures:** " + "; ".join("%s: %s" % f for f in failed))
    L.append("- The ratings are heuristics, applied the same way to every provider. For example, a response field becoming optional is a warning, and a new response enum value is informational. Different clients would choose differently.\n")
    L.append("## Reproduce\n")
    L.append("```bash\ngo build -o bin/unbroke ./cmd/unbroke\npython3 scripts/benchmark.py\n```\n")
    L.append("History lookups use the unauthenticated GitHub API (60 per hour); set `GITHUB_TOKEN` for a faster first run. Later runs use the cache.\n")
    os.makedirs(os.path.dirname(DOC), exist_ok=True)
    open(DOC, "w").write("\n".join(L))
    print("wrote", DOC)
    print("providers=%d specs=%d breaking=%d warning=%d endpoints=%d failures=%d" % (len(res), specs, total_b, total_w, ops, len(failed)))


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--since", default="6m")
    ap.add_argument("--report", action="store_true", help="only rewrite the report from the last run")
    a = ap.parse_args()
    if not os.path.exists(UNBROKE):
        sys.exit("build unbroke first: go build -o bin/unbroke ./cmd/unbroke")
    if not a.report:
        run(providers(), a.since)
    report(analyze(load()))
