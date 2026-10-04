# Pocketful stage 3 — run instructions

Build and start (from this folder):

```sh
docker build -t pocketful-stage3 . && docker run --rm -e PORT=8080 -p 8080:8080 pocketful-stage3
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

`POST /payments/{id}/corrections` (the eighth idempotent write path) appends a revision. Check
order: 401 → Idempotency-Key header → body is a JSON object → already-claimed key (200 replay of
the original revision / 409 `idempotency_key_reuse`) → 404 unknown payment → 403 not the original
sender → 422 `validation_failed` (any invalid field) → 422 `linked_payment_immutable` (settlement
members, captures) → 409 `stale_revision` → 409 `insufficient_funds` (the current debit against
available) → 409 `historical_overdraft` (total or available negative at any past boundary of
either party, all movements at one instant combined). A refusal changes nothing and claims no key.

Time precision: payments, revisions (`recorded_at`) and hold events (captures, voids) use
microseconds (`2026-10-04T04:43:43.123456+00:00`) from ONE monotonic clock, so `recorded_at`
strictly increases per payment and every money event (payment, settlement, authorization creation,
capture, void, correction) is ordered after every earlier one. Authorization `expires_at` =
`created_at` + ttl exactly. Requests and splits (not money events) keep whole seconds. Seeded
payments and seeded open holds without `created_at` take the reset instant itself (µs, on the same
clock): at or after every supplied seeded `created_at` and before every later API event.

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
docker build -t pocketful-stage3 . && docker build -t pocketful-stage1 ../stage-1
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
.venv/bin/python -m harness run --track pocketful --repo <result-repo> --stage 3 --out <new-dir>
```
