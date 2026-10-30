# Running Unbroke in GitHub Actions

Two recipes. Replace `github.com/yug/unbroke` with the module path you publish under.

## 1. Tell me when an API change affects this repository

Runs weekly. The job fails (exit status 3) when a Stripe, OpenAI, Twilio or other
catalogued API changed in a way that touches code in the repository, so the failure
is the notification.

```yaml
name: unbroke
on:
  schedule:
    - cron: "17 6 * * 1"
  workflow_dispatch:
jobs:
  scan:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: stable
      - run: go install github.com/yug/unbroke/cmd/unbroke@latest
      # The token only raises GitHub's rate limit for history lookups.
      - run: unbroke scan -since 3m -fail-on warning .
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

`-fail-on` takes `never` (the default), `warning` or `breaking`. Only changes that
touch code in the repository count; an API change you don't call never fails the job.
Exit status 4 means an API the code uses could not be checked this run (for example
its spec is archived or the download failed). Treat it as a failure, or allow it
explicitly, but don't read it as "nothing changed".

## 2. Open a pull request with the fix

Needs a model key of your own in the repository secrets as `UNBROKE_ANTHROPIC_API_KEY`.
Unbroke does not read `ANTHROPIC_API_KEY`, so it never picks up a key someone else issued.
Your project's tests run, and the change is discarded if they fail.

```yaml
name: unbroke-fix
on:
  workflow_dispatch:
permissions:
  contents: write
  pull-requests: write
jobs:
  fix:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: stable
      - run: go install github.com/yug/unbroke/cmd/unbroke@latest
      - run: |
          git config user.name "unbroke[bot]"
          git config user.email "unbroke@users.noreply.github.com"
      - run: unbroke fix -provider stripe -test "npm test" -open-pr
        env:
          UNBROKE_ANTHROPIC_API_KEY: ${{ secrets.UNBROKE_ANTHROPIC_API_KEY }}
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

Run it manually the first time and read the pull request closely.

## Providers that publish no spec history

For 53 of the 122 catalogued APIs (the ones marked `snapshot` in `unbroke catalog list`, such
as Auth0, PostHog, Mistral, Jira and Notion) the provider hosts its own spec, so Unbroke builds
history by saving copies. Until the first copy is a few days old, such an API reports *not
checked* (exit status 4 with `-fail-on`). Keep the snapshots between runs (a cache step on
`~/.unbroke/snapshots`) and add `unbroke snapshot` to the weekly job:

```yaml
      - uses: actions/cache@v4
        with:
          path: ~/.unbroke/snapshots
          key: unbroke-snapshots-${{ github.run_id }}
          restore-keys: unbroke-snapshots-
      - run: unbroke snapshot
```
