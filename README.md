# Mend

Integrations that fix themselves.

Every software product depends on other companies' APIs. Those APIs change: fields get renamed, endpoints get retired, enums get narrowed. The first sign is usually a customer reporting that something broke.

Mend watches the APIs your code depends on, works out which changes actually affect you, and opens a tested pull request before anything breaks.

**Status: early.** The change engine and the API catalog work on real data. The repo scanner and the fix agent are next. See [What works today](#what-works-today) and [What is not built yet](#what-is-not-built-yet).

## Try it

```bash
go build -o bin/mend ./cmd/mend

bin/mend catalog list                       # the public APIs Mend watches
bin/mend endpoints stripe -grep refund      # one provider's endpoints
bin/mend changes stripe -since 6m           # what changed in Stripe's API in six months
bin/mend changes github -since 90d -min breaking
bin/mend diff old-spec.yaml new-spec.yaml   # compare any two OpenAPI files
```

`mend changes` works for any provider whose spec lives in a public GitHub repository. It finds the repository as it stood on the chosen date, so no prior monitoring is needed.

## What works today

**A catalog of real APIs.** [`catalog/catalog.md`](catalog/catalog.md) lists every provider Mend can watch, with endpoint counts. Every entry was downloaded and parsed by `mend catalog verify`; [`catalog/endpoints/`](catalog/endpoints) lists each endpoint. Companies whose public spec could not be found are listed separately, so the gaps are visible.

**A change engine tuned on real specs.** Built on [oasdiff](https://github.com/oasdiff/oasdiff)'s rules, then adjusted for what matters to someone who *calls* an API:

- A new value in a response is informational, not breaking. It only matters to strict clients.
- A change to a schema shared by hundreds of endpoints is one record that lists every endpoint it reaches. Run naively on two Stripe specs, the same comparison produced 80,673 "breaking" changes; it now produces about 30.
- Announced removals (deprecations with a sunset date) are treated as warnings, since they are the earliest signal there is.

**Handles messy specs.** Providers publish specs with broken references and wrong field types. When a full comparison is impossible, Mend compares the endpoint lists instead and says so, so a malformed spec never means silence. A failed or empty download is never mistaken for "every endpoint was removed".

**Swagger 2.0 and OpenAPI 3**, YAML and JSON.

## Benchmark

Run across every catalogued API with public history, comparing each spec as it stood six months ago with today ([full report](docs/benchmark.md), reproduced by `python3 scripts/benchmark.py`):

- 28 providers, 178 spec files, 0 failures.
- 17 providers had at least one breaking change: 1,413 breaking and 1,909 warning records, touching 2,402 endpoints.
- The slowest provider takes about 12 seconds.

Caveats, stated up front: 9 of the 178 spec files were malformed in the providers' own repositories, so Mend compared endpoint lists only for those. A breaking change means a caller *might* break; whether yours does depends on which endpoints your code calls, which is what the repo scanner is for.

## How it fits together

```
catalog (who, where)  ->  fetch + cache  ->  specload (normalize)
                                                   |
history (spec as of a date)  ----------->  specdiff (compare, rate, group)
                                                   |
                                              track (per provider)
                                                   |
                                               mend changes
```

| Package | Job |
|---|---|
| `internal/catalog` | The list of APIs, and the verifier that proves each entry still downloads and parses |
| `internal/fetch` | Downloads with a disk cache and an honest User-Agent |
| `internal/specload` | YAML/JSON to JSON; Swagger 2 to OpenAPI 3 |
| `internal/inventory` | Lists endpoints without resolving references (a 45 MB spec in about 4 s) |
| `internal/specdiff` | Compares two specs and returns rated, grouped change records; falls back to endpoint-level comparison |
| `internal/history` | Finds a public spec repository as of a past date |
| `internal/track` | Compares every spec of a provider, now versus then |
| `internal/change` | The change record everything else shares |

## What is not built yet

- **Repo scanner.** The part that decides which changes matter to *your* code, by finding which endpoints a codebase calls. This is the product; the engine above is its foundation.
- **Fix agent and pull requests.**
- **Continuous watching and snapshots** for providers that host their own spec and keep no public history.
- **Semantic drift detection** from live responses, for changes a spec comparison cannot see.

## Limits worth knowing

- A spec diff sees what the provider documented. An undocumented behavior change is invisible to it.
- `mend changes` compares the file at the same path then and now. If a provider moved its spec file, the older version is reported as "did not exist then".
- GitHub allows 60 unauthenticated history lookups an hour. Set `GITHUB_TOKEN` for more; results are cached.

## Development

```bash
go test ./...
bin/mend catalog verify -out catalog      # re-download and re-check every catalogued spec
```
