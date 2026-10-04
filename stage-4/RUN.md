# Pocketful stage 4 — run instructions

Build and start (from this folder):

```sh
docker build -t pocketful-stage4 . && docker run --rm -e PORT=8080 -p 8080:8080 pocketful-stage4
```

Then open http://localhost:8080/ in a browser (sign up, or log in as a user from a reset fixture).

The service listens on `0.0.0.0:$PORT` (default `8080`). `GET /health` returns
`200 {"status":"ok"}` as soon as the process is up. It needs no network, volume or other
service at run time, writes no files (it runs with `--read-only --network none`), and runs as an
unprivileged user from a `scratch` image. All state is in memory: `POST /_test/reset` replaces
it with a fixture, `GET /_test/export` snapshots it and `POST /_test/import` restores a snapshot
(including exports made by the stage-1 and stage-2 services: revision 1 of every payment and the
hold history are synthesized on import).

Stage 3 adds `GET /me?as_of=&known_at=`, `GET /statement` (with frozen `snapshot` paging) and
`GET /payments/{id}/revisions`. Every money view is computed from an append-only log of payment
revisions (effective and recorded times) and hold lifecycle events; new payment and hold event
times carry microseconds and come from a monotonic clock.

`POST /payments/{id}/refunds` (the ninth idempotent write path, stage 4) creates a refund: a new
payment from the original receiver back to the sender with `refund_of`, the target's note and
visibility, `request_id`/`authorization_id`/`settlement_id` null. Check order: 401 → key header →
body object → claimed key → 404 → 403 (not the original receiver) → 422 `validation_failed`
(amount) → 422 `invalid_refund_target` (a refund) → 422 `refund_exceeds_payment` (all refunds above
the target's current corrected amount) → 409 `insufficient_funds` (the receiver's available funds).
Refunds are immutable; they never reopen requests or authorizations or change settlements. Every
payment body carries `refund_of` (null otherwise); stored older receipts and statement snapshots
taken by older stages replay in their original form.

`POST /payments/{id}/corrections` (the eighth idempotent write path) appends a revision (revision objects carry
`correction_batch_id`, null for single corrections). Check
order: 401 → Idempotency-Key header → body is a JSON object → already-claimed key (200 replay of
the original revision / 409 `idempotency_key_reuse`) → 404 unknown payment → 403 not the original
sender → 422 `validation_failed` (any invalid field) → 422 `linked_payment_immutable` (settlement
members, captures, refunds) → 409 `stale_revision` → 422 `refund_exceeds_payment` (below the
refunded amount) → 409 `insufficient_funds` (the current debit against
available) → 409 `historical_overdraft` (total or available negative at any past boundary of
either party, all movements at one instant combined). A refusal changes nothing and claims no key.

Time precision: payments, revisions (`recorded_at`) and hold events (captures, voids) use
microseconds (`2026-10-04T04:43:43.123456+00:00`) from ONE monotonic clock, so `recorded_at`
strictly increases per payment and every money event (payment, settlement, authorization creation,
capture, void, correction) is ordered after every earlier one. Authorization `expires_at` =
`created_at` + ttl exactly. Requests and splits (not money events) keep whole seconds. Seeded
payments and seeded open holds without `created_at` take the reset instant itself (µs, on the same
clock): at or after every supplied seeded `created_at` and before every later API event.

Time-handling rules (audit): one wall clock (`app/clock.go`, pinnable in tests) feeds
`stamp()` = max(wall, last stamp + 1µs) for every money event; checks and reads use
`nowFloor()` = max(wall, last stamp), so "now" is never before an event already recorded.
- Reset: one reset stamp; omitted seeded `created_at` (payments, open holds) = that instant;
  supplied values and seeded `closed_at` must not be after it (422); a supplied seeded hold's
  `closed_at` may not precede its supplied `created_at` (422).
- Import: every imported revision, hold creation and close raises the clock floor, so new events
  are stamped after all imported history even if this container's clock is behind.
- Expiry: the sweep uses `nowFloor()`; a capture or void also checks its own stamp, so an event
  stamped at or after `expires_at` finds the hold expired (409), never captures it.
- Corrections: `effective_at` must not be after `nowFloor()` nor after the correction's own
  `recorded_at`.
- Snapshots: K = min(known_at, read stamp); every later write is stamped after the read.
- Requests and splits use whole-second `created_at`; they are not money events and appear in no
  balance or history computation.

The browser UI is one HTML shell, one stylesheet and one script embedded in the binary
(`app/ui/`), rendered client-side from the JSON API. It uses the system font stack and makes no
requests outside the service. `/requests` and `/authorizations` serve the UI to
`Accept: text/html` and JSON otherwise.

Own tests (Go 1.26, standard library only), from this folder:

```sh
go test ./tests/ -count=1 -v
CGO_ENABLED=1 go test -race ./tests/ -count=1
```

Real-browser checks (Playwright Chromium from the kickoff venv; containers run on an internal
Docker network with no outbound access; screenshots go to `review-ui/screens/`):

```sh
docker build -t pocketful-stage4 . && docker build -t pocketful-stage1 ../stage-1
<kickoff>/.venv/bin/python review-ui/ui_check.py    # screens, flows, layout, contrast
<kickoff>/.venv/bin/python review-ui/ui_check2.py   # out-of-order refresh, competing clients,
                                                     # lost/timed-out/5xx writes, stage-1 -> stage-2
                                                     # upgrade without reload, long values, paging
```

UI behaviour worth knowing: every idempotent form keeps its Idempotency-Key while its body is
unchanged, so resubmitting (or retrying after an unknown outcome) is a replay; a definite 4xx
starts a new key. A request with no answer within 8 s, a network error or a 5xx shows the
"not confirmed" (uncertain) state, never a refusal. Lists show 50 items with "Show more".
The authorise form appears on `/` ("Reserve money") and on `/authorizations`. Error messages
are plain sentences; the API error code is kept only in the message's `data-code` attribute.

Shipped checks (from the kickoff folder):

```sh
.venv/bin/python -m harness run --track pocketful --repo <result-repo> --stage 4 --out <new-dir>
```
