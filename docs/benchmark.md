# Benchmark: six months of real API changes

Generated 2026-10-28 by `scripts/benchmark.py` with `-since 6m`. Every figure below is computed from the raw output in `bench/`.

## What was run

For each catalogued API whose spec lives in a public GitHub repository, Unbroke compared the spec as it stood about six months ago with the spec today (`unbroke changes <provider> -since 6m`). No monitoring was running during that time; the history comes from the providers' own repositories.

## Headline

- **27 providers, 177 spec files** compared, **0 failures**.
- **17 providers** had at least one breaking change; **1,422 breaking** and **1,917 warning** records in total, touching **2,433 endpoints**.
- Informational changes (new optional fields, new response values) are recorded but hidden by default.

## By provider

| Provider | Compared since | Spec files | Breaking | Warning | Endpoints affected | Notes |
|---|---|---:|---:|---:|---:|---|
| Cloudflare | 2026-05-01 | 1 | 611 | 1162 | 1355 |  |
| Sentry | 2026-05-01 | 1 | 166 | 100 | 98 |  |
| Datadog | 2026-04-30 | 2 | 158 | 82 | 176 |  |
| Kubernetes | 2026-05-02 | 1 | 149 | 188 | 400 |  |
| Asana | 2026-04-28 | 1 | 128 | 21 | 23 |  |
| Plaid | 2026-04-27 | 1 | 50 | 105 | 88 |  |
| GitHub | 2026-04-28 | 1 | 43 | 61 | 69 |  |
| Discord | 2026-04-24 | 1 | 25 | 18 | 44 |  |
| Adyen | 2026-05-01 | 31 | 18 | 57 | 25 | 3 spec file(s) absent then |
| Okta Management | 2026-03-23 | 1 | 18 | 21 | 36 | spec repo unchanged since 2026-03-23 |
| Resend | 2026-04-20 | 1 | 17 | 13 | 35 |  |
| Stripe | 2026-04-28 | 1 | 13 | 14 | 17 |  |
| Docker Engine | 2026-05-01 | 1 | 11 | 1 | 5 |  |
| Twilio | 2026-04-14 | 60 | 5 | 2 | 7 | 8 spec file(s) absent then |
| Box | 2026-04-28 | 1 | 4 | 66 | 40 |  |
| Square | 2025-10-16 | 1 | 4 | 0 | 4 | reduced comparison for 1 spec file(s); spec repo unchanged since 2025-10-16 |
| Telnyx | 2026-04-30 | 1 | 2 | 2 | 4 | reduced comparison for 1 spec file(s) |
| Algolia | 2026-04-30 | 1 | 0 | 2 | 2 |  |
| SendGrid | 2026-03-24 | 46 | 0 | 2 | 5 | reduced comparison for 1 spec file(s); spec repo unchanged since 2026-03-24 |
| DigitalOcean | 2026-05-01 | 1 | 0 | 0 | 0 | reduced comparison for 1 spec file(s) |
| Elasticsearch | 2026-04-30 | 1 | 0 | 0 | 0 | reduced comparison for 1 spec file(s) |
| HubSpot CRM | 2026-05-01 | 4 | 0 | 0 | 0 |  |
| Intercom | 2026-04-30 | 1 | 0 | 0 | 0 | 1 spec file(s) absent then |
| Netlify | 2026-04-23 | 1 | 0 | 0 | 0 |  |
| OpenAI | 2025-06-17 | 1 | 0 | 0 | 0 | 1 spec file(s) absent then |
| PagerDuty | 2026-04-30 | 1 | 0 | 0 | 0 | reduced comparison for 1 spec file(s) |
| PayPal | 2026-04-07 | 13 | 0 | 0 | 0 | reduced comparison for 1 spec file(s) |

## Read this before quoting the numbers

- **Breaking means a caller might break, not that it did.** Unbroke rates changes from the point of view of someone calling the API. A spec comparison cannot know whether your code uses the changed endpoint; that is what the repo scanner is for. A zero is also not proof of stability: a spec diff sees only what the provider documented.
- **7 of 177 spec files got a reduced comparison.** The provider's published spec was malformed (broken references, wrong field types, duplicate endpoints), so Unbroke compared endpoint lists only: removed and newly deprecated endpoints, but nothing inside requests and responses. Affected: digitalocean, elasticsearch, pagerduty, paypal/invoicing_v2, sendgrid/logs_v3, square, telnyx.
- **Some repositories had not been updated for a long time**, so "six months ago" resolved to an older commit: okta (2026-03-23), sendgrid (2026-03-24), square (2025-10-16).
- **13 spec files did not exist at the earlier date.** They are new, or they moved: adyen/A2APaymentsService v1, adyen/DocumentCollectorService v1, adyen/RaiseDisputesService v4, intercom, openai, twilio/conversations_v2, twilio/insights_v3, twilio/intelligence_v3, twilio/knowledge_v2, twilio/memory_v1, twilio/routes_v3, twilio/voice_v2, twilio/voice_v3.
- **Not compared because the provider stopped updating its published spec:** Slack Web API. A comparison of an archived spec can only ever show no changes, so it is left out rather than counted as stable.
- The ratings are heuristics, applied the same way to every provider. For example, a response field becoming optional is a warning, and a new response enum value is informational. Different clients would choose differently.

## Reproduce

```bash
go build -o bin/unbroke ./cmd/unbroke
python3 scripts/benchmark.py
```

History lookups use the unauthenticated GitHub API (60 per hour); set `GITHUB_TOKEN` for a faster first run. Later runs use the cache.
