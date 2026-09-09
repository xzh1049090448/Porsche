# Public Content and Pricing Design

**Status:** APPROVED_FOR_PLANNING  
**Date:** 2026-09-09  
**Scope:** PRD-260903 P01 and P03-P08  
**Product source:** `/Users/xuzhihao/code/PRD-260903-管理员用户管理与公共页面体系.md`

## Goal and release boundary

Build the public homepage, pricing catalog, stable model detail pages, legal/about pages, Root-only model pricing administration, immutable price snapshots, upstream price monitoring, safe content publishing, and snapshot-aware static rendering. The first delivery uses a safe draft: unreviewed prices, terms, privacy text, or prototype claims cannot be published. Production content approval remains a release condition rather than an implementation shortcut.

The existing plan, call-limit, API key, chat, and billing behavior remains unchanged. Public prices are USD reference prices per million input or output tokens and do not create a ledger or automatic charge.

## Decisions

- Public page visuals follow PRD section 7 and the existing landing/pricing prototypes. Prototype claims such as `40+`, `100%`, and `MIT` are not production data.
- All pricing writes, model lifecycle actions, snapshot publication, and restoration are Root-only. Ordinary content permissions may later be delegated, but they never grant model-pricing access.
- Model configuration is derived from the configured white-label upstream catalog. Root selects an observed upstream model and adds public metadata and reference prices.
- Pricing supports only input and output token prices, fixed to USD per million tokens. There is no per-request price.
- Root changes remain drafts until explicit snapshot publication. Confirmed upstream removal can automatically inactivate a model and publish a safety snapshot that removes it.
- Every deletion is a non-recoverable soft deletion. `inactive` is a separate reversible status. Deleted records are absent from management list/search/detail APIs and have no deleted-items UI.
- Notifications are persistent in-app Root notifications. Email delivery is an explicit follow-up; this delivery only preserves a channel abstraction.
- The upstream monitor runs every five minutes. A model is automatically inactivated after three consecutive absences from successful, complete, fresh catalogs. Failures, stale catalogs, and incomplete responses never advance absence counters.

## Persistence

`public_model_configs` stores permanent `model_key` and `upstream_model_id` identities, display metadata, capabilities, context window, precise input/output prices, `draft|active|inactive` status, inactive reason, upstream observation timestamps, consecutive absence count, optimistic `revision`, `ever_published`, audit fields, and `is_deleted`. `model_key` and `upstream_model_id` remain reserved after deletion.

`public_price_snapshots` stores immutable release metadata, reason (`root_publish`, `upstream_safety`, or `restore`), actor, source draft revision, content hash, timestamps, and restoration provenance. `public_price_snapshot_items` freezes every published model's public identity, metadata, USD input/output prices, and upstream check time. A singleton state row points atomically to the current snapshot.

Additive migration `0013` introduces `public_price_draft_state`, which stores the independent singleton optimistic revision for the aggregate publishable model draft. Every model create, update, lifecycle change, or deletion locks and advances it in the same transaction; publication compares this revision without reusing the committed publication pointer revision.

`public_content_drafts` stores the versioned site/home/about/terms/privacy document with optimistic revision. `public_content_releases` stores immutable validated releases. Site publication state binds exact content and price snapshot versions so homepage references cannot drift from the pricing catalog.

`upstream_model_observations` stores sanitized model identifiers, prices, observation time, completeness/freshness, and response-summary hash for the latest 30 days. It never stores API keys, authorization headers, channel addresses, or an unrestricted raw response.

`root_alerts` stores a deduplicated active or resolved alert keyed by alert type and model identity. `root_alert_receipts` stores each Root user's independent read and acknowledgement state. New Roots can see unresolved global alerts.

`public_render_jobs` records the release generation to materialize, lease state, attempts, and last sanitized failure. The renderer consumes only committed publication state.

## Model lifecycle and deletion

- `draft`: configured but not eligible for publication.
- `active`: eligible for the next manual snapshot.
- `inactive`: visible to Root and reversible after review; excluded from new snapshots.
- `is_deleted=1`: permanently absent from management and public operational APIs; cannot be restored.

A previously published inactive or deleted `modelKey` returns `410 Gone`. A never-published or unknown `modelKey` returns `404 Not Found`. Historical snapshots remain immutable. Reappearing upstream models clear their absence counter but are not automatically reactivated. Root must review and explicitly activate them.

## Upstream monitoring

One scheduler tick runs every five minutes under a distributed lease so only one application instance evaluates a catalog generation. It requests the existing white-label catalog and accepts it for absence decisions only when the response is successful, complete, and fresh.

For each observed model it records sanitized prices and last-seen time. It compares the currently published input and output prices independently with the corresponding upstream values. Either published value below upstream creates or refreshes a deduplicated alert; it never changes a Root price. Invalid or unavailable values create a not-comparable alert.

Each valid full-catalog absence increments a counter. At three, the service inactivates the model with reason `upstream_removed`, audits the evidence, creates a Root alert, and atomically publishes a safety snapshot excluding all currently inactive/deleted models. An upstream error does not change model state. A reappearing model remains inactive and creates a review alert.

## Administration APIs

Root-only model routes:

```text
GET    /admin/v2/public-models
POST   /admin/v2/public-models
GET    /admin/v2/public-models/:guid
PATCH  /admin/v2/public-models/:guid
POST   /admin/v2/public-models/:guid/activate
POST   /admin/v2/public-models/:guid/deactivate
DELETE /admin/v2/public-models/:guid
GET    /admin/v2/public-models/missing
POST   /admin/v2/public-models/sync
```

List/search supports public name, `modelKey`, upstream ID, provider, lifecycle status, configuration completeness, and upstream state. Missing detection distinguishes upstream models with no configuration from configured models missing upstream. Creation must reference the latest accepted upstream observation; arbitrary upstream IDs are rejected. Updates use `expected_revision`; stale writes return `409`.

Root-only pricing routes:

```text
GET  /admin/v2/public-pricing/draft
PUT  /admin/v2/public-pricing/draft
POST /admin/v2/public-pricing/validate
POST /admin/v2/public-pricing/publish
GET  /admin/v2/public-pricing/releases
GET  /admin/v2/public-pricing/releases/:guid
POST /admin/v2/public-pricing/releases/:guid/restore
```

Publish and restore use current-password action verification, a single-use action ticket, optimistic revision, and `Idempotency-Key`. Restoration copies a historical release into a new draft, validates it under current rules, and publishes a new version.

Notification routes:

```text
GET  /admin/v2/notifications
GET  /admin/v2/notifications/unread-count
POST /admin/v2/notifications/:guid/read
POST /admin/v2/notifications/:guid/acknowledge
```

Alert types cover published price below upstream, upstream missing, automatic inactivation, reappearance, catalog sync failure, price not comparable, and renderer failure. Responses and logs contain no upstream secrets.

Content routes manage drafts, validation, authenticated no-store/noindex preview, publication, history, and restoration under `/admin/v2/public-content`. Root has access by role. Future delegated content permissions do not affect pricing APIs.

## Public APIs

```text
GET /api/v1/public/site
GET /api/v1/public/home
GET /api/v1/public/models
GET /api/v1/public/models/:modelKey
GET /api/v1/public/pages/about
GET /api/v1/public/pages/terms
GET /api/v1/public/pages/privacy
```

All responses read committed snapshot pointers, return release version and ETag, exclude internal IDs and upstream routing data, and never replace missing prices with zero. Model responses label prices as references that do not promise automatic charging. Pricing visibility is stored in publication state; authenticated-only mode removes anonymous prices from API, generated HTML, and sitemap before the mode switch commits.

## Validation and content safety

Publication validates required references, permanent `modelKey` uniqueness, active model membership, exact USD/million-token dimensions, decimal bounds, content versions, reviewed terms/privacy state, links, and optimistic revisions. Restricted Markdown/HTML is sanitized with an allowlist. Scripts, event handlers, executable URLs, arbitrary embeds, and remote image fetching are rejected. Only controlled local image references or separately approved assets are accepted.

The validation gate rejects unreviewed legal text and pricing and unsubstantiated prototype claims. An empty safe draft may be edited and previewed but cannot be presented as approved production content.

## Static rendering

A dedicated `public-renderer` process polls committed render jobs at least once per minute. It reads public APIs and generates meaningful HTML for `/`, `/pricing`, each active `/pricing/:modelKey`, `/about`, `/terms`, `/privacy`, and `sitemap.xml`. Files are built and validated in a private staging directory, then an atomic directory pointer is switched. Failure preserves the previous static tree and creates a Root alert.

Generated HTML contains title, description, canonical URL, visible published text, release metadata, and a Vue handoff. It contains no drafts, credentials, internal upstream data, or protected prices. `restart-all.sh` installs/restarts the renderer, while content publication itself does not redeploy either application.

## Frontend

`PublicLayout` owns `/`, `/pricing`, `/pricing/:modelKey`, `/about`, `/terms`, `/privacy`, and the public 404. The authenticated console moves to `/chat`; login defaults to `/chat`, and redirect validation accepts only safe internal paths. Existing `/profile`, `/billing`, and `/api-keys` behavior remains.

The public price page follows the PRD desktop 260px filter plus table and mobile drawer/card design. It supports model-name/alias search, provider/capability/endpoint filters, URL-backed sorting/pagination, distinct empty/loading/failure states, and exact missing-price labels. Detail routing uses only stable `modelKey` even when the upstream ID contains `/`.

Root administration provides model list/search, detail/create/update, activate/inactivate, soft delete, missing-model detection, draft differences, publication validation, snapshot history/restoration, and notification inbox/unread badge. Deleted models have no listing or detail surface.

## Failure behavior

Upstream failure preserves counters, model state, and current snapshots. Price alerts never rewrite prices. Stale revisions return `409`. Duplicate publication returns the original idempotent result. Database or audit failure cannot advance publication state. Automatic snapshot failure leaves the model inactive and public APIs exclude it immediately; the renderer preserves the prior files and raises a high-priority alert until a coherent static generation succeeds.

## Verification

Backend tests use isolated MySQL 8 and Redis 7 and cover Root-only authorization, exact decimal validation, lifecycle/deletion visibility, permanent keys, optimistic writes, idempotent atomic snapshots, restoration, alert deduplication and per-Root receipts, scheduler lease/races, complete-catalog three-strike behavior, stale/error non-strikes, automatic safety snapshots, sanitization, and secret redaction.

Frontend tests cover all public routes and states, safe login redirect, URL-backed catalog controls, missing prices, slash-containing upstream IDs through stable keys, Root CRUD/lifecycle/missing detection, publication/history, notifications, responsive layouts at 375/768/1440, keyboard focus, and reduced motion.

Renderer tests verify meaningful HTML and canonical metadata, public-only sitemap membership, authenticated-price redaction, staging validation, atomic switching, last-good preservation, and API/HTML/sitemap generation agreement within 60 seconds.

Joint acceptance records P01 and P03-P08 separately. Implementation can pass while the first real publication remains blocked on reviewed prices, terms, privacy, and brand content. No code-only or prototype-only evidence may promote production content acceptance.

## Deferred work

- Email notification delivery, SMTP configuration, recipient policy, retry, and bounce handling.
- Real billing ledger, automated token charging, currency conversion, refunds, reconciliation, or payment processing.
- Arbitrary image upload/proxy and remote asset fetching.
- Delegated pricing access for non-Root administrators.
