# Stage 4 requirements ledger — Pocketful: refunds and batch corrections

Lines 1–494 carry the stage-3 ledger forward unchanged in id and text. They stay `done` (or
`n/a`) except where stage 4 changes the behaviour; those are reopened as `todo (S4: …)`. Lines
495+ are this stage's normative statements (ids `S4-…`). Status: `todo` | `done` | `n/a (reason)`.

Serialization point: unchanged, one global `Server.mu`; one monotonic µs event clock.

| # | id | requirement | status |
|---|---|---|---|
| 1 | SC-01 | Build only from the supplied requirements; no source/API docs/schemas from existing products in the domain | done (process: only the spec text and shipped tests were read) |
| 2 | SC-02 | Only the HTTP API is required (send by handle, request, split, activity feed, settlements) | done |
| 3 | SC-03 | Invariant: sum of wallet balances always equals the total seeded by the last `POST /_test/reset` (incl. concurrency, retries) | done |
| 4 | SC-04 | Invariant: no wallet balance negative, including transiently | done |
| 5 | SC-05 | Invariant: a payment request moves money at most once | done |
| 6 | SC-06 | All amounts are exact integer counts of minor units | done |
| 7 | SC-07 | Money moves only between existing wallets; deposits/top-ups/withdrawals/cards/banks out of scope | done |
| 8 | DL-01 | Deliver an HTTP service, a `Dockerfile` and a `RUN.md` with a command that builds and starts it without manual setup | done |
| 9 | DL-02 | `docker-compose.yml` optional and not used to start the service | done |
| 10 | DL-03 | Image runs on its own with `-e PORT=<port>` and a port mapping | done |
| 11 | DL-04 | No outbound network at run time; all runtime deps, init and seed data inside the single container (vendored at build) | done |
| 12 | DL-05 | Runtime assets (fonts, scripts, stylesheets) included in the image | done |
| 13 | LIM-01 | CPU limit 2 vCPU | done |
| 14 | LIM-02 | Memory limit 2 GiB | done |
| 15 | LIM-03 | Start to first healthy response ≤ 60 s | done |
| 16 | LIM-04 | Up to 50 concurrent requests in flight | done |
| 17 | LIM-05 | Per-request timeout 5 s; 10 s for `POST /_test/reset` and test-control calls (export/import) | done |
| 18 | LIM-06 | Disk ephemeral; state need not survive a container restart | done |
| 19 | RT-01 | Listen on `0.0.0.0` on `PORT` env var, default `8080` | done |
| 20 | RT-02 | `GET /health` → 200 `{"status":"ok"}` once service and store can serve; non-200 allowed before ready | done |
| 21 | RT-03 | `POST /_test/reset` with fixture body → 204 No Content; replaces all state | done |
| 22 | RT-04 | After reset returns 204, subsequent requests see only that fixture; repeated resets supported | done |
| 23 | RT-05 | Reset enabled in delivered image, no authentication | done |
| 24 | CV-01 | Requests and responses are `application/json; charset=utf-8` | done |
| 25 | CV-02 | Response timestamps RFC 3339 with explicit offset (e.g. `+00:00`) | done |
| 26 | CV-03 | Unknown fields in a request body ignored, never an error | done |
| 27 | CV-04 | Unknown query parameters ignored | done |
| 28 | CV-05 | IDs opaque strings ≤ 64 characters | done |
| 29 | MD-01 | One currency, declared in the fixture; every API amount is an integer count of its minor units | done |
| 30 | MD-02 | Amounts with integral numeric value accepted: `1000`, `1000.0`, `1e3` are the same valid amount | done |
| 31 | MD-03 | Booleans and strings are not numbers for amounts | done |
| 32 | MD-04 | Handle unique across service, matches `^[a-z0-9_]{1,20}$`, never changes once set | done |
| 33 | MD-05 | Recipients identified by handle; directory/user-search endpoints out of scope | n/a (out of scope by text) |
| 34 | MD-06 | Seeded users take handle from fixture | done |
| 35 | MD-07 | Signup handle derived from email: local part, lowercase, chars outside `[a-z0-9_]` → `_`, truncate to 20 | done |
| 36 | MD-08 | Derived handle already taken → signup fails (see AU-07) | done |
| 37 | MD-09 | New users start with balance 0; can receive money and be asked for money immediately | done |
| 38 | MD-10 | A payment moves money from one wallet to another immediately and atomically; sent directly or by paying a request | done |
| 39 | MD-11 | Request lifecycle: `pending` then exactly one of `paid`, `declined`, `cancelled` | done |
| 40 | MD-12 | Only the payer may pay or decline; only the requester may cancel | done |
| 41 | MD-13 | A request may exceed payer's balance; legal at creation, stays `pending` | done |
| 42 | MD-14 | Paying while short → 409 `insufficient_funds`, changes nothing; same request payable later once money arrives | done |
| 43 | MD-15 | Visibility belongs to the payment, chosen by the payer when money moves; a request has no visibility | done |
| 44 | MD-16 | A request never appears in anyone else's feed | done |
| 45 | FD-01 | `GET /activity` returns payments only | done |
| 46 | FD-02 | Payment appears for caller iff `visibility` is `public` OR caller is sender OR receiver; no other rule | done |
| 47 | FD-03 | Requests never in activity feed; `GET /requests` returns only requests where caller is requester or payer | done |
| 48 | FD-04 | A split is not a feed item; its requests visible to their two parties; fulfilling payments follow FD-02 | done |
| 49 | FD-05 | Visibility is one value on the payment, identical for both parties and everyone else; private hidden from third parties only | done |
| 50 | AR-01 | `amount` ≤ 1000000000 on any single request | done |
| 51 | AR-02 | No operation produces a balance outside ±2⁵³ | done |
| 52 | AR-03 | Monetary arithmetic exact, no rounding error (integers, no floats) | done |
| 53 | FX-01 | Fixture shape: `currency`, `minor_units`, `users[]` (id, email, password, display_name, handle, balance), `payments[]` (id, from_user_id, to_user_id, amount, note, visibility), `requests[]` (id, requester_id, payer_id, amount, note, status) | done |
| 54 | FX-02 | Seeded users can log in with the given password immediately | done |
| 55 | FX-03 | Seeded `balance` is post-payment; seeded payments are not replayed against balances | done |
| 56 | FX-04 | Fixture `balance` < 0 → 422 `validation_failed` from reset, change nothing | done |
| 57 | FX-05 | `minor_units` is 0, 2 or 3; fixtures use EUR (2), JPY (0), BHD (3) | done |
| 58 | FX-06 | Fixture may include `settlement_operator_ids` (array of user ids), default `[]` | done |
| 59 | FX-07 | Administrative balance endpoint out of scope | n/a (out of scope by text) |
| 60 | ER-01 | Every 4xx/5xx body is `{"error":{"code":"...","message":"..."}}`; message any wording | done |
| 61 | ER-02 | 400 `malformed_request`: unparseable body, or field of wrong JSON type | done |
| 62 | ER-03 | 400 `missing_idempotency_key`: required `Idempotency-Key` absent or empty | done |
| 63 | ER-04 | 401 `unauthenticated`: missing, malformed or unknown bearer token | done |
| 64 | ER-05 | 403 `forbidden`: authenticated but not permitted on this resource | done |
| 65 | ER-06 | 404 `not_found`: no such resource, or not visible to caller | done |
| 66 | ER-07 | 409 `idempotency_key_reuse`: key already used by this caller with a different body | done |
| 67 | ER-08 | 422 `validation_failed`: required field/query param missing, or stated rule violated with no more specific code | done |
| 68 | ER-09 | Correct JSON type but invalid format/out-of-range (invalid dates, negative counts, over max/length) → 422 `validation_failed` unless endpoint says otherwise | done |
| 69 | ER-10 | Invalid `amount` incl. strings and booleans → 422 `validation_failed` (not 400) | done |
| 70 | ER-11 | Non-string `note` incl. `null` → 422 `validation_failed` | done |
| 71 | ER-12 | `visibility` other than `public`/`private` (any type) → 422 `validation_failed` | done |
| 72 | ER-13 | Omission alone selects optional-field defaults; other wrong JSON types → 400 `malformed_request` | done |
| 73 | ER-14 | Integer query params must be plain decimal digits; `1e9`, `4.0`, `+4` → 422 `validation_failed` | done |
| 74 | ER-15 | 400 `malformed_request` reserved for unparseable body or wrong field type | done |
| 75 | ER-16 | `Idempotency-Key` 1–255 chars else 422 `validation_failed` | done |
| 76 | ER-17 | `limit` integer 1–200 else 422 `validation_failed` | done |
| 77 | ER-18 | `offset` integer ≥ 0 else 422 `validation_failed` | done |
| 78 | ER-19 | No 5xx responses, including under concurrent load | done |
| 79 | AU-01 | `POST /auth/signup` {email, password, display_name} → 201 {user_id, display_name, token} | done |
| 80 | AU-02 | `POST /auth/login` {email, password} → 200 {user_id, display_name, token} | done |
| 81 | AU-03 | Signup: email already registered → 409 `email_taken` | done |
| 82 | AU-04 | Signup: password shorter than 8 characters → 422 `validation_failed` | done |
| 83 | AU-05 | Signup: `email` not of form `local@domain` → 422 `validation_failed` | done |
| 84 | AU-06 | Login: wrong password or unknown email → 401 `unauthenticated` | done |
| 85 | AU-07 | Signup: derived handle already taken → 409 `handle_taken`, no account created | done |
| 86 | AU-08 | Every other endpoint requires a bearer token except `/health`, `/_test/reset`, `/_test/export`, `/_test/import`, signup, login | done |
| 87 | AU-09 | Token sent as `Authorization: Bearer <token>` | done |
| 88 | AU-10 | Tokens do not expire; multiple valid tokens and concurrent sessions per account | done |
| 89 | AU-11 | Passwords stored with bcrypt/scrypt/Argon2 or equivalent; never plaintext | done |
| 90 | AU-12 | Email verification, password reset, refresh tokens, role management out of scope | n/a (out of scope by text) |
| 91 | ID-01 | Idempotency key required on `POST /payments`, `POST /requests`, `POST /requests/{id}/pay`, `POST /splits`, `POST /settlements`, each independently | done |
| 92 | ID-02 | Key scoped to the authenticated user; same key string across users has no interaction | done |
| 93 | ID-03 | Replay = same user, same method, same path, same body; same key+body on a different path is a new request and succeeds normally | done |
| 94 | ID-04 | Header absent or empty → 400 `missing_idempotency_key` | done |
| 95 | ID-05 | First use of key → normal response 201 | done |
| 96 | ID-06 | Replay (same key, same body) → 200, body identical to original as a JSON value | done |
| 97 | ID-07 | Same key, different body → 409 `idempotency_key_reuse` | done |
| 98 | ID-08 | Key reused after original failed with 4xx → treated as first use (failed attempts claim no key) | done |
| 99 | ID-09 | "Same body" = same JSON value after parsing; key order and whitespace irrelevant | done |
| 100 | ID-10 | Concurrent identical requests with unused key: exactly one 201, others 200 same body; effect once | done |
| 101 | ID-11 | Successful replay returns original response even after the resource changed/cancelled; no further state change | done |
| 102 | ID-12 | Order: body parses as JSON object + caller authenticated → claimed key resolved before field validation or resource checks (invalid body with used key → 409 `idempotency_key_reuse`) | done |
| 103 | API-01 | `GET /me` → {user_id, display_name, handle, balance, currency, minor_units} | done |
| 104 | PM-01 | `POST /payments` body {to_handle, amount, note?, visibility?}; `note` default `""`, `visibility` default `"public"` | done |
| 105 | PM-02 | 201 payment body: payment_id, from_user_id, from_handle, to_user_id, to_handle, amount, currency, note, visibility, request_id (null), created_at (+ settlement_id null per ST-09) | done |
| 106 | PM-03 | Caller balance below `amount` → 409 `insufficient_funds` | done |
| 107 | PM-04 | `amount` < 1, > 1000000000 or non-integer → 422 `validation_failed` | done |
| 108 | PM-05 | `to_handle` is caller's own → 422 `self_payment` | done |
| 109 | PM-06 | `note` > 200 characters → 422 `validation_failed` | done |
| 110 | PM-07 | `visibility` neither `public` nor `private` → 422 `validation_failed` | done |
| 111 | PM-08 | No user has that handle → 404 `not_found` | done |
| 112 | PM-09 | Debit and credit one atomic step; never visible in one wallet and not the other; failed payment leaves no trace | done |
| 113 | PM-10 | `note` stored and returned verbatim (no trim/escape/normalise); Unicode/emoji round-trip byte for byte | done |
| 114 | RQ-01 | `POST /requests` body {payer_handle, amount, note?}; caller is requester | done |
| 115 | RQ-02 | 201 request body: request_id, requester_id, requester_handle, payer_id, payer_handle, amount, currency, note, status `pending`, payment_id null, created_at | done |
| 116 | RQ-03 | `amount` < 1, > 1000000000 or non-integer → 422 `validation_failed` | done |
| 117 | RQ-04 | `payer_handle` is caller's own → 422 `self_request` | done |
| 118 | RQ-05 | `note` > 200 characters → 422 `validation_failed` | done |
| 119 | RQ-06 | No user has that handle → 404 `not_found` | done |
| 120 | RQ-07 | Payer's balance not checked at creation | done |
| 121 | PY-01 | `POST /requests/{id}/pay` idempotent; only payer may call | done |
| 122 | PY-02 | Body `visibility` only, optional, default `"public"`; `{}` and `{"visibility":"public"}` are different bodies for replay | done |
| 123 | PY-03 | 201 with the created payment as in `POST /payments`, `request_id` set; request becomes `paid` with `payment_id` | done |
| 124 | PY-04 | Request not `pending` → 409 `request_not_pending` | done |
| 125 | PY-05 | Payer balance below amount → 409 `insufficient_funds` | done |
| 126 | PY-06 | Caller not the payer → 403 `forbidden` | done |
| 127 | PY-07 | Unknown request → 404 `not_found` | done |
| 128 | PY-08 | Replay of successful pay → 200 original payment body even when already `paid`; no extra money; never 409 `request_not_pending` | done |
| 129 | DC-01 | `POST /requests/{id}/decline`: only payer, no idempotency key, 200 with request `status: "declined"` | done |
| 130 | DC-02 | Decline already-declined → 200 current state | done |
| 131 | DC-03 | Decline `paid` or `cancelled` → 409 `request_not_pending` | done |
| 132 | DC-04 | Decline by non-payer → 403 `forbidden` | done |
| 133 | CN-01 | `POST /requests/{id}/cancel`: only requester, no idempotency key, 200 with request `status: "cancelled"` | done |
| 134 | CN-02 | Cancel already-cancelled → 200 | done |
| 135 | CN-03 | Cancel `paid` or `declined` → 409 `request_not_pending` | done |
| 136 | CN-04 | Cancel by non-requester → 403 `forbidden` | done |
| 137 | LR-01 | `GET /requests` returns only requests where caller is requester or payer, newest first by `created_at` | done |
| 138 | LR-02 | `direction`: `incoming` (caller payer), `outgoing` (caller requester), absent = both; unknown → 422 | done |
| 139 | LR-03 | `status`: one of four statuses or absent = all; unknown → 422 | done |
| 140 | LR-04 | `limit` default 50 (1–200), `offset` default 0 (≥ 0); out of range → 422 `validation_failed` | done |
| 141 | LR-05 | Response `{"requests":[...], "has_more": bool}`; `has_more` true when items exist beyond last returned | done |
| 142 | SP-01 | `POST /splits` idempotent; body {amount, participant_handles, note?}; caller may be included or omitted | done |
| 143 | SP-02 | Shares by §9 rule in given handle order; one `pending` request per participant except caller, caller as requester | done |
| 144 | SP-03 | 201 body: split_id, amount, currency, note, shares [{handle, amount}], requests [...], created_at | done |
| 145 | SP-04 | `shares` covers every participant incl. caller in given order and sums to `amount`; `requests` excludes caller, same order | done |
| 146 | SP-05 | `amount` < 1, > 1000000000 or non-integer → 422 `validation_failed` | done |
| 147 | SP-06 | `participant_handles` empty or containing duplicate → 422 `validation_failed` | done |
| 148 | SP-07 | `note` > 200 characters → 422 `validation_failed` | done |
| 149 | SP-08 | Any handle unknown → 404 `not_found` | done |
| 150 | SP-09 | Caller as only participant is valid: one share, zero requests, `"requests": []` | done |
| 151 | SP-10 | A split checks nobody's balance | done |
| 152 | AC-01 | `GET /activity` → payments visible per FD-02, newest first by `created_at`; `{"payments":[...], "has_more": bool}` | done |
| 153 | AC-02 | Same-second order unspecified; stable pagination under concurrent writes not required | done |
| 154 | AC-03 | `limit`/`offset` behave exactly as in `GET /requests` | done |
| 155 | MR-01 | Shares whole minor units, sum exactly to `amount`, differ by at most 1 | done |
| 156 | MR-02 | Remainder units go to the first participants in `participant_handles` order | done |
| 157 | MR-03 | Table: 1000/3 → 334, 333, 333 | done |
| 158 | MR-04 | Table: 1/3 → 1, 0, 0 | done |
| 159 | MR-05 | Table: 10/3 → 4, 3, 3 | done |
| 160 | MR-06 | Table: 999/3 → 333, 333, 333 | done |
| 161 | MR-07 | Table: 5/5 → 1, 1, 1, 1, 1 | done |
| 162 | MR-08 | Different handle order gives the extra unit to a different person | done |
| 163 | MR-09 | A share of 0 is legal and still produces a request for that participant | done |
| 164 | MR-10 | Each split's shares independent of previous splits; after any splits paid in full, balances sum to seeded total | done |
| 165 | EX-01 | `GET /_test/export` and `POST /_test/import` exist, unauthenticated | done |
| 166 | EX-02 | Export → 200 JSON object with `track: "pocketful"`, `format_version: 1`, `state` (opaque object) | done |
| 167 | EX-03 | Export is an atomic read-only snapshot; later source writes do not change it | done |
| 168 | EX-04 | Import takes the entire export object, atomically replaces state, → 204; accepts unchanged export of this service | done |
| 169 | EX-05 | No dependency on source process, files, volume, port or network address | done |
| 170 | EX-06 | Import is replacement, not merge; repeating restores state without duplication | done |
| 171 | EX-07 | Import invalid JSON → §5 (400 `malformed_request`) | done |
| 172 | EX-08 | Import missing fields, wrong track/version or invalid state → 422 `validation_failed`, destination unchanged | done |
| 173 | EX-09 | Import/export 10 s timeout | done |
| 174 | EX-10 | Preserve accounts, hashed-password login, existing bearer tokens, currency, balances, payments, requests, permissions (operators) | done |
| 175 | EX-11 | Preserve all completed idempotent request bodies and original responses; replays still return 200 original after import | done |
| 176 | EX-12 | Identities, timestamps, monetary records not regenerated or replayed against net balances | done |
| 177 | EX-13 | Failed request keys remain reusable after import | done |
| 178 | EX-14 | Import removes all previous destination data and credentials | done |
| 179 | EX-15 | Reset clears all state, including imported state | done |
| 180 | EX-16 | Preserve settlement membership (`settlement_id` on payments) and settlement retry responses through export/import | done |
| 181 | ST-01 | Operators: `settlement_operator_ids` from fixture; operator may settle across any wallets | done |
| 182 | ST-02 | Operator permission grants no access to others' requests or private activity | done |
| 183 | ST-03 | `POST /settlements` requires operator + idempotency key; no token → 401; non-operator → 403 `forbidden` | done |
| 184 | ST-04 | Body `{"transfers":[{from_handle, to_handle, amount, note?, visibility?}]}`; 1..32 transfers | done |
| 185 | ST-05 | Each transfer uses payment amount/note/visibility rules (defaults empty note, public) | done |
| 186 | ST-06 | Unknown handle → 404 `not_found`; self-transfer → 422 `self_payment`; malformed batch shape → 422 `validation_failed`; unknown fields ignored | done |
| 187 | ST-07 | Entry errors take precedence in input order, all before insufficient funds | done |
| 188 | ST-08 | Affordable iff every wallet's net balance after all transfers ≥ 0; else 409 `insufficient_funds` | done |
| 189 | ST-09 | All movements commit together or none; failed validation claims no key, creates no payment or revision | done |
| 190 | ST-10 | 201 with `settlement_id`, `committed_at`, `payments` in input order | done |
| 191 | ST-11 | Members are ordinary payments with `settlement_id`; non-member payments expose `settlement_id: null` | done |
| 192 | ST-12 | Members have `request_id: null` and identical server-assigned `created_at` == `committed_at` | done |
| 193 | ST-13 | Members follow ordinary activity-feed visibility | done |
| 194 | ST-14 | Replay → 200 with original complete response | done |
| 195 | ST-15 | Reset/import preserves operator permissions, original payments, requests, membership and retry responses | done |
| 196 | ID-13 | A body that parses but is not a JSON object (array, string, number, null) on any POST taking a body → 400 `malformed_request` (coordinator addition) | done |
| 197 | ER-20 | Same rule as ID-13 for every POST endpoint, including signup, login, reset, import (coordinator addition) | done |
| 198 | FX-08 | Reset fixture with wrong JSON types, missing required fields (currency, minor_units, users; user id/email/password/handle/balance; payment id/from/to/amount; request id/requester/payer/amount), duplicate user ids/handles/emails/payment ids/request ids, unknown user ids in payments/requests/settlement_operator_ids, bad handle, minor_units not 0/2/3 → 422 `validation_failed`, state unchanged; unparseable → 400 (coordinator addition) | done |
| 199 | FX-09 | Seeded requests carry status (default `pending`) and optional payment_id; seeded payments carry optional request_id, settlement_id, visibility (default public), note (default ""); created_at server-assigned at reset when absent (coordinator addition) | done |
| 200 | AU-13 | Unknown route → 404 `not_found`; known path, wrong method → 405 `method_not_allowed`, both in the error envelope; bearer checked before body/idempotency on authed endpoints (coordinator addition) | done |
| 201 | ER-21 | Write-path precedence: 401 → missing/invalid Idempotency-Key (400/422) → body parses as object (400) → claimed key (409 / 200 replay) → field validation → resource checks (coordinator addition) | done |
| 202 | PF-01 | Password hashing never holds the state lock; reset of ~100 users and 50 concurrent logins finish well under 5 s on 2 vCPU (coordinator addition) | done |
| 203 | S2-SC-01 | The stage-1 requirements continue to apply, with the stage-2 additions | done |
| 204 | S2-SC-02 | Users manage payments, requests and bill splits in a browser, and can reserve money for a recipient to collect later in one or more captures | done |
| 205 | S2-RT-01 | Route `/` reachable by URL: balance, pay form, request form and activity feed | done |
| 206 | S2-RT-02 | Route `/requests` reachable by URL: incoming and outgoing requests with pay, decline, cancel | done |
| 207 | S2-RT-03 | Route `/split` reachable by URL: split form | done |
| 208 | S2-RT-04 | Route `/signup` reachable by URL: signup | done |
| 209 | S2-RT-05 | Route `/login` reachable by URL: login | done |
| 210 | S2-RT-06 | Route `/authorizations` reachable by URL: authorisation list (with capture/void) | done |
| 211 | S2-RT-07 | Other screens reachable through the UI (consistent navigation) | done |
| 212 | S2-RT-08 | `GET /requests` with `Accept: text/html` returns the UI; without that header returns JSON (stage-1 API unchanged) | done |
| 213 | S2-RT-09 | `GET /authorizations` with `Accept: text/html` returns the UI; otherwise JSON | done |
| 214 | S2-RT-10 | UI exposes every listed `data-testid`; extra elements allowed | done |
| 215 | S2-UX-01 | Coherent, presentation-ready consumer finance product with a calm, trustworthy character (not a test harness) | done |
| 216 | S2-UX-02 | Available funds are the clearest monetary value once holds exist; total and held visibly secondary | done |
| 217 | S2-UX-03 | Payments, requests, splits and authorisations easy to scan; status, direction, privacy and money movement understandable without raw API data | done |
| 218 | S2-UX-04 | Consistent visual system for typography, spacing, colour, controls and feedback | done |
| 219 | S2-UX-05 | Primary actions easy to identify | done |
| 220 | S2-UX-06 | Available, held, pending, loading, successful, refused and uncertain states are visually distinct | done |
| 221 | S2-UX-07 | People, amounts and timestamps formatted for people; technical ids only where they help | done |
| 222 | S2-UX-08 | Required flows usable at a 375 CSS-px viewport with no horizontal page scrolling | done |
| 223 | S2-UX-09 | Required flows usable at conventional desktop widths with no horizontal page scrolling | done |
| 224 | S2-UX-10 | Every input has a visible label | done |
| 225 | S2-UX-11 | Keyboard focus is visibly apparent | done |
| 226 | S2-UX-12 | Text and controls have sufficient contrast (target WCAG AA 4.5:1) | done |
| 227 | S2-UX-13 | Considered empty states | done |
| 228 | S2-UX-14 | Considered loading states | done |
| 229 | S2-UX-15 | Considered error states | done |
| 230 | S2-UX-16 | Navigation consistent across the required routes | done |
| 231 | S2-UX-17 | All UI assets (CSS, JS, any fonts) served from the image; no CDN or external fetch at run time | done |
| 232 | S2-AU-01 | `signup-email`, `signup-password`, `signup-display-name` inputs | done |
| 233 | S2-AU-02 | `signup-submit` button | done |
| 234 | S2-AU-03 | `login-email`, `login-password` inputs and `login-submit` button | done |
| 235 | S2-AU-04 | `auth-error` error message, present only when there is one | done |
| 236 | S2-AU-05 | `current-user` visible on every screen when signed in; text contains the display name | done |
| 237 | S2-AU-06 | `current-handle` text is exactly the caller's handle (no `@`, no other words) | done |
| 238 | S2-AU-07 | `logout-button` button | done |
| 239 | S2-WL-01 | `wallet-balance` text is exactly the formatted `total`; carries `data-amount="{minor units}"` | done |
| 240 | S2-WL-02 | `pay-handle`, `pay-amount`, `pay-note` inputs; `pay-amount` is a decimal string as typed (e.g. `15.00`) | done |
| 241 | S2-WL-03 | `pay-visibility` selects `public` or `private`; option values are exactly those strings | done |
| 242 | S2-WL-04 | `pay-submit` button | done |
| 243 | S2-WL-05 | `pay-error` shown when the payment is refused, including insufficient funds | done |
| 244 | S2-WL-06 | Request form: `request-handle`, `request-amount`, `request-note`, `request-submit` | done |
| 245 | S2-WL-07 | `request-error` shown when the request is refused | done |
| 246 | S2-WL-08 | Pay form keeps its values after success | done |
| 247 | S2-WL-09 | Resubmitting the unchanged pay form sends no new payment: balance falls once, feed has one payment, `pay-error` absent (same key + body replay) | done |
| 248 | S2-WL-10 | Changing any pay field makes the next submission a new payment (new key) | done |
| 249 | S2-WL-11 | UI retries follow §7 | done |
| 250 | S2-WL-12 | Formatted amount: exactly `minor_units` decimals, one space, currency code (`100.00 EUR`) | done |
| 251 | S2-WL-13 | `minor_units` 0: no decimal point (`1200 JPY`) | done |
| 252 | S2-WL-14 | Formatted amounts carry no sign | done |
| 253 | S2-WL-15 | Decimal input converts to minor units exactly: `15.00` and `15` → 1500, `15.5` → 1550 (minor_units 2); no floats | done |
| 254 | S2-WL-16 | Nonnumeric input, or more than `minor_units` decimals (`15.005`), shows the form's error element and sends no request; never rounds | done |
| 255 | S2-FD-01 | `activity-list` container; children newest first in the DOM | done |
| 256 | S2-FD-02 | `activity-item-{payment_id}` one per visible payment, with `data-visibility="public"|"private"` | done |
| 257 | S2-FD-03 | `activity-parties-{payment_id}` text contains both handles | done |
| 258 | S2-FD-04 | `activity-amount-{payment_id}` text is exactly the formatted amount | done |
| 259 | S2-FD-05 | `activity-note-{payment_id}` text is exactly the note; present even when the note is empty | done |
| 260 | S2-FD-06 | `empty-activity` shown instead of the list when nothing is visible | done |
| 261 | S2-RQ-01 | `incoming-list` and `outgoing-list` containers on `/requests` | done |
| 262 | S2-RQ-02 | `request-item-{request_id}` one per request, with `data-status="{status}"` | done |
| 263 | S2-RQ-03 | `request-amount-{request_id}` text is exactly the formatted amount | done |
| 264 | S2-RQ-04 | `request-pay-{request_id}` button present only on a pending incoming request | done |
| 265 | S2-RQ-05 | `request-decline-{request_id}` button present only on a pending incoming request | done |
| 266 | S2-RQ-06 | `request-cancel-{request_id}` button present only on a pending outgoing request | done |
| 267 | S2-RQ-07 | `request-error` shown when a pay, decline or cancel is refused | done |
| 268 | S2-RQ-08 | `empty-requests` shown when both lists are empty | done |
| 269 | S2-SP-01 | `split-amount` decimal input, same rule as `pay-amount` | done |
| 270 | S2-SP-02 | `split-handles` text input: comma-separated handles, in order | done |
| 271 | S2-SP-03 | `split-note` input and `split-submit` button | done |
| 272 | S2-SP-04 | `split-preview` shows computed shares before submitting, with one `split-share-{handle}` per participant | done |
| 273 | S2-SP-05 | `split-share-{handle}` text is exactly the formatted share | done |
| 274 | S2-SP-06 | `split-error` shown when the split is refused | done |
| 275 | S2-SP-07 | Preview uses the §9 rule; preview and submitted split have identical shares | done |
| 276 | S2-RF-01 | After any successful action, balance, feed and request lists on the same page show the new state without manual reload | done |
| 277 | S2-RF-02 | Refresh after a write waits for the write to succeed | done |
| 278 | S2-RF-03 | No live-update/polling requirement (only own actions and explicit refresh) | n/a (no requirement: stated as not required) |
| 279 | S2-CC-01 | `wallet-refresh` button on `/` refreshes balance and feed without clearing the pay form | done |
| 280 | S2-CC-02 | Latest refresh wins: an earlier read arriving late never overwrites a later refresh (out-of-order responses) | done |
| 281 | S2-CC-03 | Refused payment shows `pay-error`, refreshes balance and feed, and preserves all pay inputs | done |
| 282 | S2-CC-04 | Request cancelled elsewhere: refused pay shows `request-error` and refreshes the list so the stale pay button disappears | done |
| 283 | S2-CC-05 | Lost payment response (incl. after commit) shows `pay-uncertain` (nonempty text), not `pay-error` | done |
| 284 | S2-CC-06 | The unchanged form stays retryable with the same key and body after an uncertain outcome | done |
| 285 | S2-CC-07 | Successful retry removes `pay-error` and `pay-uncertain`, refreshes balance and feed, money moves exactly once | done |
| 286 | S2-CC-08 | Unknown outcomes are never shown as confirmed rejections | done |
| 287 | S2-CC-09 | Refresh rules also apply to available and held amounts | done |
| 288 | S2-CC-10 | No background polling, live sync or reload recovery required | n/a (no requirement: stated as not required) |
| 289 | S2-UP-01 | A stage-2 service accepts an export produced by our stage-1 service (`POST /_test/import`) | done |
| 290 | S2-UP-02 | A browser signed in before the export/import upgrade stays signed in afterwards | done |
| 291 | S2-UP-03 | Pending requests remain payable through the request screen after the upgrade | done |
| 292 | S2-UP-04 | A payment whose response was lost before export is retryable after import with the same key and body; the UI recovers the original payment and refreshes the imported balance | done |
| 293 | S2-UP-05 | No page reload or new screen needed; the form and pending retry identity survive the upgrade (import between browser requests only) | done |
| 294 | S2-AZ-01 | An authorisation can be captured later for the full amount or less | done |
| 295 | S2-AZ-02 | An authorisation places a hold on the payer: reserves money without moving it | done |
| 296 | S2-AZ-03 | Capturing moves money; a final capture releases whatever was not captured | done |
| 297 | S2-AZ-04 | Nonfinal captures keep the remainder held | done |
| 298 | S2-AZ-05 | An open authorisation expires on its own and releases its remainder | done |
| 299 | S2-AZ-06 | Invariant: sum of wallet `total` always equals the last reset's seeded total; holds move no money | done |
| 300 | S2-AZ-07 | Invariant: `available = total − held` is never negative | done |
| 301 | S2-AZ-08 | Held funds cannot fund new payments, authorisations or settlement net debits | done |
| 302 | S2-AZ-09 | Captures may spend the money reserved for them | done |
| 303 | S2-AZ-10 | Invariant: cumulative captures never exceed the authorised amount | done |
| 304 | S2-AZ-11 | Each idempotent capture moves money once | done |
| 305 | S2-AZ-12 | A closed hold cannot be captured again | done |
| 306 | S2-API-01 | `GET /me` keeps `balance`; `balance` always equals `total`; adds `total`, `available`, `held` | done |
| 307 | S2-API-02 | `held` is the sum of open holds; `available = total − held` | done |
| 308 | S2-API-03 | With no open holds: `balance` = `total` = `available`, `held` = 0, earlier behaviour unchanged | done |
| 309 | S2-API-04 | `POST /payments` stays an immediate transfer; no intermediate hold or capture | done |
| 310 | S2-API-05 | `409 insufficient_funds` on `POST /payments` is evaluated against `available` | done |
| 311 | S2-API-06 | `409 insufficient_funds` on `POST /requests/{id}/pay` is evaluated against `available` | done |
| 312 | S2-API-07 | Settlement affordability uses `available` (net debits cannot spend held funds) | done |
| 313 | S2-API-08 | Paying a request stays immediate; authorising a request is out of scope | done |
| 314 | S2-API-09 | `POST /splits` unchanged | done |
| 315 | S2-API-10 | Seven idempotent write paths: stage 1's five, `POST /authorizations`, `POST /authorizations/{id}/capture`; replay rules apply to each independently | done |
| 316 | S2-API-11 | Payments not created by a capture carry `authorization_id: null`; `request_id` semantics unchanged | done |
| 317 | S2-FX-01 | Fixture `authorization_ttl_seconds` applies to every API-created authorisation; default 600 when omitted | done |
| 318 | S2-FX-02 | `authorization_ttl_seconds`, if supplied, must be a positive integer number of seconds, otherwise 422 `validation_failed` | done |
| 319 | S2-FX-03 | Fixture `authorizations[]`: id, from_user_id, to_user_id, amount, note, visibility, status, absolute `expires_at` | done |
| 320 | S2-FX-04 | Seeded `balance` is `total`; `available` is derived, never seeded (seeded open holds subtracted) | done |
| 321 | S2-FX-05 | Sum of a user's seeded unexpired open holds > their `balance` → 422 `validation_failed` from reset, nothing changed | done |
| 322 | S2-FX-06 | Seeded `status` is `open`, `captured`, `voided` or `expired`; only `open` holds funds | done |
| 323 | S2-FX-07 | `authorizations` omitted → empty list (stage-1 fixtures stay valid) | done |
| 324 | S2-FX-08 | Seeded authorisation validation: unknown user ids, duplicate ids, bad status/visibility/amount, unparseable `expires_at` → 422 (fixture-validation rule) | done |
| 325 | S2-EXP-01 | An authorisation whose `expires_at` is at or before now is `expired` and holds no funds | done |
| 326 | S2-EXP-02 | Reads and writes reflect expiry even if no request happened at the deadline | done |
| 327 | S2-EXP-03 | `GET /authorizations` shows `status: "expired"` for clock-expired authorisations | done |
| 328 | S2-EXP-04 | `GET /me` includes the released remainder in `available` | done |
| 329 | S2-EXP-05 | Seeded `expires_at` may be in the past (expired at reset) or the future; API-created ones may be short-lived | done |
| 330 | S2-AC-01 | `POST /authorizations` requires `Idempotency-Key`; the caller is the payer | done |
| 331 | S2-AC-02 | Body `to_handle`, `amount`, optional `note` (default "") and `visibility` (default "public") | done |
| 332 | S2-AC-03 | (stage 3: created_at and expires_at carry µs from the event clock; expires_at − created_at = ttl exactly) 201 body: authorization_id, from_user_id, from_handle, to_user_id, to_handle, amount, captured_amount 0, currency, note, visibility, status `open`, expires_at, payment_id null, created_at, remaining_amount, payment_ids [] | done |
| 333 | S2-AC-04 | `expires_at` = `created_at` + `authorization_ttl_seconds` | done |
| 334 | S2-AC-05 | Caller's `available` below `amount` → 409 `insufficient_funds` | done |
| 335 | S2-AC-06 | `amount` below 1, above 1000000000 or not an integer → 422 `validation_failed` | done |
| 336 | S2-AC-07 | `to_handle` is the caller's own → 422 `self_payment` | done |
| 337 | S2-AC-08 | `note` over 200 characters or bad `visibility` → 422 `validation_failed` | done |
| 338 | S2-AC-09 | No user has that handle → 404 `not_found` | done |
| 339 | S2-AC-10 | An authorisation is never an activity-feed item | done |
| 340 | S2-CP-01 | `POST /authorizations/{id}/capture` requires `Idempotency-Key`; only the receiver may capture | done |
| 341 | S2-CP-02 | Body `amount` optional, defaulting to the remaining amount | done |
| 342 | S2-CP-03 | A replay must send the identical body: `{}` vs `{"amount":2000}` with one key → 409 `idempotency_key_reuse` | done |
| 343 | S2-CP-04 | 201 with a payment in exactly the `POST /payments` shape, `authorization_id` set, `request_id: null` | done |
| 344 | S2-CP-05 | Payment amount = captured amount; note and visibility copied from the authorisation; appears in the feed by the ordinary rule | done |
| 345 | S2-CP-06 | Default (final) capture: status `captured`, `captured_amount`, `payment_id` set, uncaptured remainder released in the same step | done |
| 346 | S2-CP-07 | A capture after a final capture → 409 `authorization_not_open` | done |
| 347 | S2-CP-08 | `final` is boolean, default `true` (wrong type → 400 `malformed_request` by the §5 wrong-type rule) | done |
| 348 | S2-CP-09 | `final: false` with a remainder left: status stays `open`; further captures allowed up to the remainder | done |
| 349 | S2-CP-10 | Capturing the entire remainder closes the authorisation even with `final: false` | done |
| 350 | S2-CP-11 | A final capture closes the authorisation and releases any remainder | done |
| 351 | S2-CP-12 | `capture_exceeds_authorization` compares with the remaining amount; omitted amount = remainder | done |
| 352 | S2-CP-13 | `captured_amount` is cumulative; `payment_id` is the latest capture; `payment_ids` lists every capture in order | done |
| 353 | S2-CP-14 | Every authorisation response includes `remaining_amount` (amount still held, 0 when closed) | done |
| 354 | S2-CP-15 | Void and expiry can close a partially captured authorisation, release only the remainder, and keep all capture records | done |
| 355 | S2-CP-16 | New fields do not change idempotency body equality | done |
| 356 | S2-CP-17 | Not `open` → 409 `authorization_not_open` | done |
| 357 | S2-CP-18 | `expires_at` at or before now → 409 `authorization_expired` | done |
| 358 | S2-CP-19 | `amount` above the uncaptured remainder → 422 `capture_exceeds_authorization` (includes whole numbers above 1e9, which can never fit a remainder) | done |
| 359 | S2-CP-20 | `amount` below 1 or not an integer → 422 `validation_failed` | done |
| 360 | S2-CP-21 | Caller is not the receiver (incl. neither party) → 403 `forbidden` | done |
| 361 | S2-CP-22 | Unknown authorisation → 404 `not_found` | done |
| 362 | S2-VD-01 | `POST /authorizations/{id}/void`: only the payer; no idempotency key | done |
| 363 | S2-VD-02 | 200 with the authorisation, `status: "voided"`, hold released | done |
| 364 | S2-VD-03 | Voiding an already-voided authorisation → 200 with the current state | done |
| 365 | S2-VD-04 | Voiding a `captured` or `expired` authorisation → 409 `authorization_not_open` | done |
| 366 | S2-VD-05 | Caller is not the payer (incl. neither party) → 403 `forbidden`; unknown → 404 `not_found` | done |
| 367 | S2-LA-01 | `GET /authorizations` returns only authorisations where the caller is payer or receiver, newest first by `created_at` | done |
| 368 | S2-LA-02 | `direction`: `outgoing` (payer), `incoming` (receiver), absent = both; unknown value → 422 | done |
| 369 | S2-LA-03 | `status`: one of the four, or absent = all; clock-expired matches `expired`, never `open`; unknown value → 422 | done |
| 370 | S2-LA-04 | `limit`, `offset`, `has_more` behave exactly as on `GET /requests`; body `{"authorizations": [...], "has_more": bool}` | done |
| 371 | S2-UA-01 | `wallet-balance` shows formatted `total` with `data-amount`, as before | done |
| 372 | S2-UA-02 | `wallet-available` shows formatted `available` with `data-amount`, presented as the headline number | done |
| 373 | S2-UA-03 | `wallet-held` shows formatted `held` with `data-amount`; absent when `held` is 0 | done |
| 374 | S2-UA-04 | Authorise form `authorize-handle`, `authorize-amount`, `authorize-note`, `authorize-visibility`, `authorize-submit`, with the pay-form input rules (on `/` as "Reserve money" and on `/authorizations`) | done |
| 375 | S2-UA-05 | `authorize-error` shown when an authorisation is refused, including insufficient available funds | done |
| 376 | S2-UA-06 | `authorization-list` container on `/authorizations`; children newest first in the DOM | done |
| 377 | S2-UA-07 | `authorization-item-{id}` with `data-status="{status}"` | done |
| 378 | S2-UA-08 | `authorization-amount-{id}` text is exactly the formatted authorised amount | done |
| 379 | S2-UA-09 | `authorization-captured-{id}` formatted captured amount, present only when status is `captured` | done |
| 380 | S2-UA-10 | `authorization-expires-{id}` text is the RFC 3339 `expires_at` | done |
| 381 | S2-UA-11 | `authorization-capture-amount-{id}` decimal input pre-filled with the remaining amount; only on an incoming open authorisation | done |
| 382 | S2-UA-12 | `authorization-capture-{id}` button only on an incoming open authorisation | done |
| 383 | S2-UA-13 | `authorization-void-{id}` button only on an outgoing open authorisation | done |
| 384 | S2-UA-14 | `authorization-error` shown when a capture or void is refused | done |
| 385 | S2-UA-15 | `empty-authorizations` shown when the list is empty | done |
| 386 | S2-UA-16 | UI reflects seeded and new holds; available shown as the spending balance, including right after a reset with open holds | done |
| 387 | S2-CON-01 | Concurrent requests give the same results as some serial order (linearizable) | done |
| 388 | S2-CON-02 | Every requirement holds at every read | done |
| 389 | S2-EX-01 | Export/import carry authorisations, ttl, capture records and the seven paths' idempotency receipts; format_version stays 1 | done |
| 390 | S3-SC-01 | The stage-1 and stage-2 requirements continue to apply, with the stage-3 additions | done |
| 391 | S3-SC-02 | Users can request historical balances and paginated statements; senders can correct eligible payments while the original receipt is preserved; historical queries support both effective date and information known at a given time | done |
| 392 | S3-TS-01 | Every payment's `created_at` is an RFC 3339 instant with an offset identifying when it moved money; every endpoint returning a payment includes it | done |
| 393 | S3-TS-02 | `GET /activity` keeps its ordering by `created_at` and shows the original payment (corrections are not feed payments) | done |
| 394 | S3-TS-03 | Seeded payments may supply `created_at`; omitted → reset time, which is before every later API-created payment (the reset instant itself in µs, at or after every supplied seeded `created_at`) | done |
| 395 | S3-TS-04 | A seeded `created_at` in the future → 422 `validation_failed` from reset, state unchanged | done |
| 396 | S3-TS-05 | Fixture `balance` stays the balance after all seeded payments; loading seeded payments never changes it | done |
| 397 | S3-AS-01 | `GET /me?as_of=` optional; must be an RFC 3339 instant with an offset | done |
| 398 | S3-AS-02 | `as_of` as naive local time, bare date or empty value → 422 `validation_failed` | done |
| 399 | S3-AS-03 | Without temporal parameters `/me` keeps its existing fields (no `as_of` key) and reports current corrected values | done |
| 400 | S3-AS-04 | With `as_of`: balance after every payment of the caller effective at or before `as_of` and before every later one (inclusive: a payment at exactly `as_of` counts) | done |
| 401 | S3-AS-05 | `as_of` at or after the latest payment → the current balance | done |
| 402 | S3-AS-06 | `as_of` before the earliest payment → the opening balance | done |
| 403 | S3-AS-07 | The response echoes `as_of` exactly as given (string unchanged) | done |
| 404 | S3-AS-08 | `as_of` may be in the future | done |
| 405 | S3-ST-01 | `GET /statement?from=&to=&limit=&offset=`; `from`/`to` optional; `from` defaults to the opening of the wallet, `to` to now (the read's start) | done |
| 406 | S3-ST-02 | `limit` and `offset` behave exactly as in `GET /requests` (default 50, 1..200, offset ≥ 0, plain digits, else 422) | done |
| 407 | S3-ST-03 | `from`/`to` must be RFC 3339 instants with offset; invalid or empty → 422 `validation_failed` | done |
| 408 | S3-ST-04 | Entries are the caller's sent or received payments in the half-open window `[from, to)`, oldest first | done |
| 409 | S3-ST-05 | Body: `opening_balance`, `entries` [{`payment`, `delta`, `balance_after`, `revision`, `effective_at`, `recorded_at`}], `closing_balance`, `has_more`, `snapshot` | done |
| 410 | S3-ST-06 | Order: selected `effective_at` ascending (= `created_at` without corrections), then payment `id` ascending for ties | done |
| 411 | S3-ST-07 | `opening_balance` = balance immediately before `from`; `closing_balance` = balance immediately before `to` | done |
| 412 | S3-ST-08 | `opening_balance` + every `delta` in the full window = `closing_balance`; sent → negative delta, received → positive | done |
| 413 | S3-ST-09 | `balance_after` is the caller's balance immediately after the entry (ties applied in entry order) | done |
| 414 | S3-ST-10 | Pagination never changes `balance_after`, `opening_balance` or `closing_balance` (they describe the full window) | done |
| 415 | S3-ST-11 | Only the caller's own payments appear, even when other payments are public; feed visibility rules do not apply | done |
| 416 | S3-ST-12 | `has_more` is correct on the final partial page and for offsets beyond the end | done |
| 417 | S3-ST-13 | Requires a bearer token (401 otherwise) | done |
| 418 | S3-RV-01 | The service distinguishes when money took effect (`effective_at`) from when it learned it (`recorded_at`) | done |
| 419 | S3-RV-02 | Every payment has a revision history; revision 1 = original amount with `effective_at = recorded_at = created_at` | done |
| 420 | S3-RV-03 | A seeded payment's supplied `created_at` is its revision-1 recorded and effective time; omitted → reset time | done |
| 421 | S3-RV-04 | Opening balance = seeded ending balance − net effect of the original seeded payments; corrections never change opening balances | done |
| 422 | S3-RV-05 | New accounts open at zero | done |
| 423 | S3-RV-06 | Seeded history is promised consistent and nonnegative (no extra validation required) | n/a (promise about fixtures, no behaviour required) |
| 424 | S3-CR-01 | `POST /payments/{payment_id}/corrections` requires an `Idempotency-Key` (eighth idempotent write path; §7 rules apply) | done |
| 425 | S3-CR-02 | Only the original sender may correct; an authenticated non-sender → 403 `forbidden` | done |
| 426 | S3-CR-03 | Unknown payment → 404 `not_found` | done |
| 427 | S3-CR-04 | Body fields `expected_revision`, `amount`, `effective_at`, `reason` all required; missing → 422 `validation_failed` | done |
| 428 | S3-CR-05 | `expected_revision` positive integer, else 422 `validation_failed` | done |
| 429 | S3-CR-06 | `amount` integer 0..1000000000 (0 reverses the whole payment), else 422 `validation_failed` | done |
| 430 | S3-CR-07 | `reason` string of 1..200 characters, else 422 `validation_failed` | done |
| 431 | S3-CR-08 | `effective_at` RFC 3339 instant with offset, not later than now, else 422 `validation_failed` | done |
| 432 | S3-CR-09 | A correction changes neither the parties nor the visibility | done |
| 433 | S3-CR-10 | Appends an immutable revision; 201 body {`payment_id`, `revision`, `amount`, `effective_at`, `recorded_at` (server-assigned), `reason`} | done |
| 434 | S3-CR-11 | `recorded_at` values of one payment strictly increase | done |
| 435 | S3-CR-12 | `expected_revision` not equal to the latest revision → 409 `stale_revision` | done |
| 436 | S3-CR-13 | Successful replay returns that original revision with 200, even after newer revisions | done |
| 437 | S3-CR-14 | Same key with a different body → 409 `idempotency_key_reuse` | done |
| 438 | S3-CR-15 | The amount difference moves between the same two wallets in one atomic step: increase debits the original sender, decrease debits the original receiver | done |
| 439 | S3-CR-16 | A currently unaffordable debit (against available) → 409 `insufficient_funds`; this takes precedence over historical checks | done |
| 440 | S3-CR-17 | Otherwise, any user's corrected total or available negative at any effective-time/event boundary → 409 `historical_overdraft` | done |
| 441 | S3-CR-18 | Boundary balances include the combined effect of all movements at that instant | done |
| 442 | S3-CR-19 | Either failure preserves balances, revision history, statements and idempotency state (no key claimed) | done |
| 443 | S3-CR-20 | The sum of balances equals the seeded total in every historical view | done |
| 444 | S3-CR-21 | The original payment and every original idempotent response remain unchanged | done |
| 445 | S3-CR-22 | Settlement members → 422 `linked_payment_immutable` | done |
| 446 | S3-CR-23 | Captures → 422 `linked_payment_immutable` | done |
| 447 | S3-CR-24 | Check order: 401 → key header → body object → claimed key → 404 → 403 → field validation → 422 linked → 409 stale → 409 insufficient_funds → 409 historical_overdraft | done |
| 448 | S3-RL-01 | `GET /payments/{payment_id}/revisions` → `{"revisions": [...]}` in revision order, including revision 1 with `reason: ""` | done |
| 449 | S3-RL-02 | Each revision has `payment_id`, `revision`, `amount`, `effective_at`, `recorded_at`, `reason` | done |
| 450 | S3-RL-03 | Only the two parties may read it; a third party → 404 even for a public payment; unknown → 404 | done |
| 451 | S3-RL-04 | No token → 401 | done |
| 452 | S3-KA-01 | `GET /me` and `GET /statement` accept optional `known_at`, an RFC 3339 instant with offset; invalid or empty → 422 | done |
| 453 | S3-KA-02 | Per payment, select its latest revision recorded at or before `known_at`; none recorded yet → the payment contributes nothing | done |
| 454 | S3-KA-03 | Omitted `known_at` = everything known when the read begins | done |
| 455 | S3-KA-04 | Selected revisions apply at their effective times; `as_of` stays inclusive, a statement window stays half-open | done |
| 456 | S3-KA-05 | `known_at` (and `as_of`) may be in the future | done |
| 457 | S3-KA-06 | A supplied `known_at` is echoed exactly | done |
| 458 | S3-KA-07 | Statement entries carry the selected `revision`, `effective_at`, `recorded_at`; `payment.amount` is the selected amount | done |
| 459 | S3-KA-08 | Zero-amount revisions still appear as entries with zero delta | done |
| 460 | S3-KA-09 | A correction is never counted alongside the revision it replaces | done |
| 461 | S3-KA-10 | With no corrections and no `known_at`, earlier behaviour is unchanged | done |
| 462 | S3-SN-01 | Every first `GET /statement` response returns an opaque `snapshot` token | done |
| 463 | S3-SN-02 | The snapshot freezes the caller's selected revisions, window, balances, entries and default `to` | done |
| 464 | S3-SN-03 | `GET /statement?snapshot=&limit=&offset=` pages that exact result even after later payments, corrections or lifecycle actions | done |
| 465 | S3-SN-04 | Only `limit` and `offset` may accompany `snapshot`; `from`, `to` or `known_at` with it → 422 `validation_failed` | done |
| 466 | S3-SN-05 | Unknown token, another user's token, or a token from before a reset → 404 `not_found` | done |
| 467 | S3-SN-06 | Tokens last until reset; surviving a container restart is not required | done |
| 468 | S3-SN-07 | Snapshot paging changes neither balances nor entries; final partial page and offsets beyond the end report `has_more` correctly | done |
| 469 | S3-SN-08 | Unrecognized query parameters stay ignored | done |
| 470 | S3-SN-09 | A correction may move a payment into or out of a statement window | done |
| 471 | S3-SN-10 | Concurrent corrections with the same `expected_revision` cannot both succeed | done |
| 472 | S3-SE-01 | Settlements keep their original receipts and privacy rules | done |
| 473 | S3-SE-02 | Each settlement member's revision 1 uses the shared `committed_at` as both `effective_at` and `recorded_at` | done |
| 474 | S3-SE-03 | A stage-3 service accepts exports from our stage-1 and stage-2 services | done |
| 475 | S3-SE-04 | Imported ledgers account for authorizations and captures | done |
| 476 | S3-HH-01 | `GET /me?as_of=T&known_at=K`: all four money fields describe that same view; `balance = total`, `available = total − held` | done |
| 477 | S3-HH-02 | A hold starts at authorization creation (creation is stamped on the same monotonic µs event clock as payments, so it never precedes an earlier write) | done |
| 478 | S3-HH-03 | A nonfinal capture reduces the hold at capture time | done |
| 479 | S3-HH-04 | A final capture, void or expiry releases the remainder at that event's time | done |
| 480 | S3-HH-05 | Expiry takes effect at `expires_at` | done |
| 481 | S3-HH-06 | Events other than clock expiry are known at their server-assigned event time | done |
| 482 | S3-HH-07 | Once creation is known, the expiry deadline is known too | done |
| 483 | S3-HH-08 | For query instants beyond now, an open hold expires at its deadline | done |
| 484 | S3-HH-09 | Without `as_of`, the view is at the instant the request began | done |
| 485 | S3-HH-10 | Authorizations expose `closed_at`: null while open, the event time once closed | done |
| 486 | S3-HH-11 | Historical `total` follows the effective/recorded-time rules | done |
| 487 | S3-HH-12 | A correction making total or available negative at any past effective/event boundary, under the latest known revisions → 409 `historical_overdraft` | done |
| 488 | S3-HH-13 | Seeded open holds are assumed created at reset unless `created_at` is supplied; seeded closed holds need not reconstruct a lifecycle | done |
| 489 | S3-HH-14 | `GET /statement` contains money movements only: authorization, release and expiry are not entries | done |
| 490 | S3-HH-15 | Captures appear exactly once in statements, with their links (`authorization_id`) | done |
| 491 | S3-HH-16 | Old snapshots stay unchanged after any lifecycle action or correction | done |
| 492 | S3-EX-01 | Export/import carry revision histories, hold lifecycle events and statement snapshots; format_version stays 1 | done |
| 493 | S3-EX-02 | Importing a stage-1/stage-2 export synthesizes revision 1 (effective = recorded = created_at) and hold history; tokens, receipts and retries stay valid | done |
| 494 | S3-PF-01 | Statements, `/me` history and correction checks over ~10^4 payments finish well within the 5 s request budget | done |
| 495 | S4-SC-01 | The stage-1, stage-2 and stage-3 requirements continue to apply | done |
| 496 | S4-SC-02 | Recipients can refund payments; settlement operators can correct several payments in one request, including settlement payments; existing receipts and saved statements stay available in their original form | done |
| 497 | S4-ID-01 | Ten idempotent write paths: stage 1's five, authorizations, captures, corrections, refunds, correction batches; §7 replay rules apply to each independently | done |
| 498 | S4-RF-01 | `POST /payments/{payment_id}/refunds`, body `{"amount": n}`, requires an `Idempotency-Key` | done |
| 499 | S4-RF-02 | Only the original receiver may refund; anyone else authenticated → 403 `forbidden` | done |
| 500 | S4-RF-03 | Unknown payment → 404 `not_found` | done |
| 501 | S4-RF-04 | The target may be a direct payment, a request payment, a capture or a settlement payment, but never a refund | done |
| 502 | S4-RF-05 | Invalid amount (missing, non-integer, below 1, above 1000000000, wrong type) → 422 `validation_failed` | done |
| 503 | S4-RF-06 | Cumulative refunds may not exceed the target's current corrected amount → 422 `refund_exceeds_payment` | done |
| 504 | S4-RF-07 | A refund of a refund → 422 `invalid_refund_target` | done |
| 505 | S4-RF-08 | A refund is a new payment in the opposite direction (from the original receiver to the original sender) | done |
| 506 | S4-RF-09 | The refund payment has `refund_of` = target id, `request_id: null`, `authorization_id: null` (and `settlement_id: null`) | done |
| 507 | S4-RF-10 | The refund payment copies the target's note and visibility | done |
| 508 | S4-RF-11 | 201 with the refund payment; a replay → 200 with the original body | done |
| 509 | S4-RF-12 | The refund moves existing money from the receiver's available funds, atomically; otherwise 409 `insufficient_funds` | done |
| 510 | S4-RF-13 | Refunds never reopen a request or authorization and never restore a released hold | done |
| 511 | S4-RF-14 | Every other payment carries `refund_of: null` | done |
| 512 | S4-RF-15 | A settlement payment may be refunded under the same rules; refunds never change settlement membership | done |
| 513 | S4-RF-16 | A refund payment's revision 1 has effective_at = recorded_at = created_at (stage-3 rule); it appears in statements and historical views like any payment | done |
| 514 | S4-RF-17 | Check order: 401 → key header → body object → claimed key → 404 → 403 → 422 validation_failed → 422 invalid_refund_target → 422 refund_exceeds_payment → 409 insufficient_funds | done |
| 515 | S4-CR-01 | Stage-3 single corrections remain available for ordinary direct and request payments | done |
| 516 | S4-CR-02 | Captures and refund payments cannot be corrected → 422 `linked_payment_immutable` | done |
| 517 | S4-CR-03 | Settlement members on the single correction endpoint → 422 `linked_payment_immutable` (they are corrected only through a complete batch) | done |
| 518 | S4-CR-04 | A correction cannot reduce a payment below its already-refunded amount → 422 `refund_exceeds_payment` | done |
| 519 | S4-CR-05 | Correction debits are checked against available funds (409 `insufficient_funds`) | done |
| 520 | S4-CR-06 | Refund payments stay effective at their own created_at; corrections of their target never shift them; historical checks include them | done |
| 521 | S4-CR-07 | Single-correction check order: … → 403 → 422 validation_failed → 422 linked_payment_immutable → 409 stale_revision → 422 refund_exceeds_payment → 409 insufficient_funds → 409 historical_overdraft | done |
| 522 | S4-CB-01 | `POST /correction-batches` requires a settlement operator and an `Idempotency-Key`; no token → 401; authenticated non-operator → 403 `forbidden` (same rules as settlements) | done |
| 523 | S4-CB-02 | Body `{"corrections": [...]}` with 1..32 objects, else 422 `validation_failed` | done |
| 524 | S4-CB-03 | payment_ids in a batch must be distinct, else 422 `validation_failed` | done |
| 525 | S4-CB-04 | Every item has the ordinary correction fields and validation (expected_revision, amount, effective_at, reason) → 422 `validation_failed` | done |
| 526 | S4-CB-05 | An item's unknown payment → 404 `not_found` | done |
| 527 | S4-CB-06 | An item's stale expected revision → 409 `stale_revision` | done |
| 528 | S4-CB-07 | The operator may correct ordinary, request and settlement payments of any wallets | done |
| 529 | S4-CB-08 | Captures and refunds stay immutable in a batch → 422 `linked_payment_immutable` | done |
| 530 | S4-CB-09 | An item may not reduce a payment below its refunded amount → 422 `refund_exceeds_payment` | done |
| 531 | S4-CB-10 | Correcting any settlement member requires including every member of that settlement → 422 `incomplete_settlement` | done |
| 532 | S4-CB-11 | Members of one settlement must have identical effective instants (offset spellings may differ) → else 422 `validation_failed` | done |
| 533 | S4-CB-12 | Ordinary single-payment corrections remain available for nonmembers | done |
| 534 | S4-CB-13 | Unknown fields are ignored | done |
| 535 | S4-CB-14 | Precedence: item errors in input order → settlement completeness → resulting current available funds → historical total and available at every effective/event boundary | done |
| 536 | S4-CB-15 | Affordability is the combined effect of all proposed revisions (net change per wallet) | done |
| 537 | S4-CB-16 | A rejected batch leaves history, balances and idempotency records unchanged | done |
| 538 | S4-CB-17 | 201 body: `correction_batch_id`, `recorded_at`, `revisions` (in input order) | done |
| 539 | S4-CB-18 | All new revisions share recorded_at, strictly later than the previous recorded_at of every member | done |
| 540 | S4-CB-19 | Each revision exposes `correction_batch_id` (in the batch response and in revision lists) | done |
| 541 | S4-CB-20 | Effective times cannot be later than now | done |
| 542 | S4-CB-21 | Original payments and receipts never change; original payment and settlement retries return their original bodies | done |
| 543 | S4-CB-22 | New statements reflect the new revisions; earlier snapshot tokens keep paging their frozen entries | done |
| 544 | S4-CB-23 | Replays return the original batch response with 200 | done |
| 545 | S4-CB-24 | Concurrent corrections sharing any expected payment revision cannot both succeed (batch vs batch, batch vs single) | done |
| 546 | S4-SM-01 | Settlement receipts and privacy rules are unchanged by refunds and batch corrections | done |
| 547 | S4-IM-01 | A stage-4 service accepts exports from our stage-1, stage-2 and stage-3 services, keeping settlement membership, corrections and snapshots | done |
| 548 | S4-IM-02 | Stage-4 export/import carries refunds (refund_of links) and correction batches (ids on revisions, receipts) | done |
| 549 | S4-HI-01 | Refunds and batch revisions appear in as_of/known_at views and statements under the stage-3 rules; the sum of balances equals the seeded total in every view | done |
| 550 | S4-HI-02 | Saved statement snapshots stay byte-identical after refunds and batch corrections | done |
| 551 | S4-CON-01 | One serialization point; concurrent refunds, corrections and batches stay linearizable (no double spend, no lost revision) | done |
