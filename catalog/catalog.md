# API catalog

35 providers, 185 specification files, 39,110 endpoints. Every row below was downloaded and parsed.

History: **git** means past versions of the spec are public, so any two versions can be compared. **snapshot** means the provider hosts the spec itself and Mend builds the history by saving copies.

## AI

| Provider | Company | Spec files | Endpoints | Deprecated | Size | History |
|---|---|---:|---:|---:|---:|---|
| OpenAI | OpenAI | 1 | 371 | 16 | 4.2 MB | git |

## CRM

| Provider | Company | Spec files | Endpoints | Deprecated | Size | History |
|---|---|---:|---:|---:|---:|---|
| HubSpot CRM | HubSpot | 4 | 49 | 0 | 0.3 MB | git |

## Cloud infrastructure

| Provider | Company | Spec files | Endpoints | Deprecated | Size | History |
|---|---|---:|---:|---:|---:|---|
| Cloudflare | Cloudflare | 1 | 3,793 | 335 | 27.7 MB | git |
| Kubernetes | CNCF (Kubernetes) | 1 | 1,158 | 0 | 4.3 MB | git |
| DigitalOcean | DigitalOcean | 1 | 858 | 0 | 0.1 MB | git |
| Vercel | Vercel | 1 | 477 | 6 | 12.0 MB | snapshot |
| Netlify | Netlify | 1 | 186 | 0 | 0.2 MB | git |
| Docker Engine | Docker | 1 | 108 | 0 | 0.5 MB | git |

## Collaboration

| Provider | Company | Spec files | Endpoints | Deprecated | Size | History |
|---|---|---:|---:|---:|---:|---|
| Jira Cloud | Atlassian | 1 | 630 | 29 | 2.5 MB | snapshot |
| Box | Box | 1 | 297 | 0 | 1.8 MB | git |
| Asana | Asana | 1 | 271 | 1 | 3.5 MB | git |
| Slack Web API | Salesforce (Slack) | 1 | 174 | 0 | 1.2 MB | git |
| Notion | Notion | 1 | 65 | 0 | 1.3 MB | snapshot |

## Communications

| Provider | Company | Spec files | Endpoints | Deprecated | Size | History |
|---|---|---:|---:|---:|---:|---|
| Twilio | Twilio | 60 | 1,570 | 0 | 12.6 MB | git |
| Telnyx | Telnyx | 1 | 1,505 | 3 | 8.5 MB | git |
| SendGrid | Twilio (SendGrid) | 46 | 391 | 0 | 2.1 MB | git |
| Discord | Discord | 1 | 250 | 0 | 1.2 MB | git |
| X API v2 | X Corp. | 1 | 249 | 0 | 1.3 MB | snapshot |
| Resend | Resend | 1 | 142 | 4 | 0.4 MB | git |

## Customer support

| Provider | Company | Spec files | Endpoints | Deprecated | Size | History |
|---|---|---:|---:|---:|---:|---|
| Zendesk | Zendesk | 1 | 672 | 4 | 2.4 MB | snapshot |
| Intercom | Intercom | 1 | 108 | 0 | 0.7 MB | git |

## Developer platform

| Provider | Company | Spec files | Endpoints | Deprecated | Size | History |
|---|---|---:|---:|---:|---:|---|
| GitHub | Microsoft (GitHub) | 1 | 1,247 | 38 | 13.8 MB | git |
| LaunchDarkly | LaunchDarkly | 1 | 405 | 23 | 3.1 MB | snapshot |

## Identity

| Provider | Company | Spec files | Endpoints | Deprecated | Size | History |
|---|---|---:|---:|---:|---:|---|
| Okta Management | Okta | 1 | 731 | 4 | 3.5 MB | git |

## Observability

| Provider | Company | Spec files | Endpoints | Deprecated | Size | History |
|---|---|---:|---:|---:|---:|---|
| Datadog | Datadog | 2 | 1,931 | 98 | 10.9 MB | git |
| PagerDuty | PagerDuty | 1 | 473 | 14 | 2.8 MB | git |
| Sentry | Sentry | 1 | 266 | 2 | 4.5 MB | git |

## Payments

| Provider | Company | Spec files | Endpoints | Deprecated | Size | History |
|---|---|---:|---:|---:|---:|---|
| Stripe | Stripe | 1 | 612 | 6 | 8.3 MB | git |
| Adyen | Adyen | 31 | 486 | 16 | 5.6 MB | git |
| Plaid | Plaid | 1 | 350 | 48 | 3.3 MB | git |
| Square | Block (Square) | 1 | 332 | 24 | 3.3 MB | git |
| PayPal | PayPal | 13 | 115 | 0 | 3.4 MB | git |

## Productivity

| Provider | Company | Spec files | Endpoints | Deprecated | Size | History |
|---|---|---:|---:|---:|---:|---|
| Microsoft Graph | Microsoft | 1 | 17,911 | 85 | 44.5 MB | git |

## Search

| Provider | Company | Spec files | Endpoints | Deprecated | Size | History |
|---|---|---:|---:|---:|---:|---|
| Elasticsearch | Elastic | 1 | 849 | 24 | 6.6 MB | git |
| Algolia | Algolia | 1 | 78 | 9 | 0.3 MB | git |

## Not located

Well-known companies whose public OpenAPI spec was not found. That is not proof none exists.

- **Shopify**: Admin API is GraphQL-first; no REST OpenAPI located
- **Salesforce**: Core platform has no stable official OpenAPI; sObjects spec is an opt-in beta
- **Zoom**: no public OpenAPI located
- **Anthropic**: no public OpenAPI located
- **Amazon Web Services**: no first-party OpenAPI; per-service Smithy models instead
- **Google Cloud**: publishes Discovery documents and protobufs, not OpenAPI
- **Microsoft Azure**: per-service specs in a very large multi-service repository; not yet added
- **Auth0**: not located at the paths tried
- **Mailgun**: not located at the paths tried
- **Gusto**: not located at the paths tried
- **WorkOS**: not located at the paths tried
- **Brex**: not located at the paths tried

