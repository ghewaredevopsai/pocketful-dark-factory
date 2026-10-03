# Stage 1 requirements ledger — Pocketful: payments and settlements

One line per normative statement of the stage-1 text (parts 1–3, spec lines 1–600).
Status: `todo` | `done` | `n/a (reason)`.

Serialization point (implemented in `app/server.go`, `Server.mu`): one process holding all state in memory;
every state-changing operation (reset, import, payment, request create/pay/decline/cancel, split,
settlement, signup, idempotency-key claim) runs inside one global critical section, so balance
checks, debits/credits and key claims are a single atomic step. Reads that must be snapshots
(export, feeds) also take that section.

| # | id | requirement | status |
|---|---|---|---|
| 1 | SC-01 | Build only from the supplied requirements; no source/API docs/schemas from existing products in the domain | todo |
| 2 | SC-02 | Only the HTTP API is required (send by handle, request, split, activity feed, settlements) | done |
| 3 | SC-03 | Invariant: sum of wallet balances always equals the total seeded by the last `POST /_test/reset` (incl. concurrency, retries) | done |
| 4 | SC-04 | Invariant: no wallet balance negative, including transiently | done |
| 5 | SC-05 | Invariant: a payment request moves money at most once | todo |
| 6 | SC-06 | All amounts are exact integer counts of minor units | todo |
| 7 | SC-07 | Money moves only between existing wallets; deposits/top-ups/withdrawals/cards/banks out of scope | todo |
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
| 35 | MD-07 | Signup handle derived from email: local part, lowercase, chars outside `[a-z0-9_]` → `_`, truncate to 20 | todo |
| 36 | MD-08 | Derived handle already taken → signup fails (see AU-07) | todo |
| 37 | MD-09 | New users start with balance 0; can receive money and be asked for money immediately | done |
| 38 | MD-10 | A payment moves money from one wallet to another immediately and atomically; sent directly or by paying a request | todo |
| 39 | MD-11 | Request lifecycle: `pending` then exactly one of `paid`, `declined`, `cancelled` | todo |
| 40 | MD-12 | Only the payer may pay or decline; only the requester may cancel | todo |
| 41 | MD-13 | A request may exceed payer's balance; legal at creation, stays `pending` | todo |
| 42 | MD-14 | Paying while short → 409 `insufficient_funds`, changes nothing; same request payable later once money arrives | todo |
| 43 | MD-15 | Visibility belongs to the payment, chosen by the payer when money moves; a request has no visibility | todo |
| 44 | MD-16 | A request never appears in anyone else's feed | todo |
| 45 | FD-01 | `GET /activity` returns payments only | todo |
| 46 | FD-02 | Payment appears for caller iff `visibility` is `public` OR caller is sender OR receiver; no other rule | todo |
| 47 | FD-03 | Requests never in activity feed; `GET /requests` returns only requests where caller is requester or payer | todo |
| 48 | FD-04 | A split is not a feed item; its requests visible to their two parties; fulfilling payments follow FD-02 | todo |
| 49 | FD-05 | Visibility is one value on the payment, identical for both parties and everyone else; private hidden from third parties only | todo |
| 50 | AR-01 | `amount` ≤ 1000000000 on any single request | todo |
| 51 | AR-02 | No operation produces a balance outside ±2⁵³ | todo |
| 52 | AR-03 | Monetary arithmetic exact, no rounding error (integers, no floats) | todo |
| 53 | FX-01 | Fixture shape: `currency`, `minor_units`, `users[]` (id, email, password, display_name, handle, balance), `payments[]` (id, from_user_id, to_user_id, amount, note, visibility), `requests[]` (id, requester_id, payer_id, amount, note, status) | done |
| 54 | FX-02 | Seeded users can log in with the given password immediately | done |
| 55 | FX-03 | Seeded `balance` is post-payment; seeded payments are not replayed against balances | done |
| 56 | FX-04 | Fixture `balance` < 0 → 422 `validation_failed` from reset, change nothing | done |
| 57 | FX-05 | `minor_units` is 0, 2 or 3; fixtures use EUR (2), JPY (0), BHD (3) | done |
| 58 | FX-06 | Fixture may include `settlement_operator_ids` (array of user ids), default `[]` | done |
| 59 | FX-07 | Administrative balance endpoint out of scope | n/a (out of scope by text) |
| 60 | ER-01 | Every 4xx/5xx body is `{"error":{"code":"...","message":"..."}}`; message any wording | done |
| 61 | ER-02 | 400 `malformed_request`: unparseable body, or field of wrong JSON type | done |
| 62 | ER-03 | 400 `missing_idempotency_key`: required `Idempotency-Key` absent or empty | todo |
| 63 | ER-04 | 401 `unauthenticated`: missing, malformed or unknown bearer token | done |
| 64 | ER-05 | 403 `forbidden`: authenticated but not permitted on this resource | todo |
| 65 | ER-06 | 404 `not_found`: no such resource, or not visible to caller | todo |
| 66 | ER-07 | 409 `idempotency_key_reuse`: key already used by this caller with a different body | todo |
| 67 | ER-08 | 422 `validation_failed`: required field/query param missing, or stated rule violated with no more specific code | done |
| 68 | ER-09 | Correct JSON type but invalid format/out-of-range (invalid dates, negative counts, over max/length) → 422 `validation_failed` unless endpoint says otherwise | todo |
| 69 | ER-10 | Invalid `amount` incl. strings and booleans → 422 `validation_failed` (not 400) | todo |
| 70 | ER-11 | Non-string `note` incl. `null` → 422 `validation_failed` | todo |
| 71 | ER-12 | `visibility` other than `public`/`private` (any type) → 422 `validation_failed` | todo |
| 72 | ER-13 | Omission alone selects optional-field defaults; other wrong JSON types → 400 `malformed_request` | done |
| 73 | ER-14 | Integer query params must be plain decimal digits; `1e9`, `4.0`, `+4` → 422 `validation_failed` | todo |
| 74 | ER-15 | 400 `malformed_request` reserved for unparseable body or wrong field type | done |
| 75 | ER-16 | `Idempotency-Key` 1–255 chars else 422 `validation_failed` | todo |
| 76 | ER-17 | `limit` integer 1–200 else 422 `validation_failed` | todo |
| 77 | ER-18 | `offset` integer ≥ 0 else 422 `validation_failed` | todo |
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
| 91 | ID-01 | Idempotency key required on `POST /payments`, `POST /requests`, `POST /requests/{id}/pay`, `POST /splits`, `POST /settlements`, each independently | todo |
| 92 | ID-02 | Key scoped to the authenticated user; same key string across users has no interaction | todo |
| 93 | ID-03 | Replay = same user, same method, same path, same body; same key+body on a different path is a new request and succeeds normally | todo |
| 94 | ID-04 | Header absent or empty → 400 `missing_idempotency_key` | todo |
| 95 | ID-05 | First use of key → normal response 201 | todo |
| 96 | ID-06 | Replay (same key, same body) → 200, body identical to original as a JSON value | todo |
| 97 | ID-07 | Same key, different body → 409 `idempotency_key_reuse` | todo |
| 98 | ID-08 | Key reused after original failed with 4xx → treated as first use (failed attempts claim no key) | todo |
| 99 | ID-09 | "Same body" = same JSON value after parsing; key order and whitespace irrelevant | todo |
| 100 | ID-10 | Concurrent identical requests with unused key: exactly one 201, others 200 same body; effect once | todo |
| 101 | ID-11 | Successful replay returns original response even after the resource changed/cancelled; no further state change | todo |
| 102 | ID-12 | Order: body parses as JSON object + caller authenticated → claimed key resolved before field validation or resource checks (invalid body with used key → 409 `idempotency_key_reuse`) | todo |
| 103 | API-01 | `GET /me` → {user_id, display_name, handle, balance, currency, minor_units} | done |
| 104 | PM-01 | `POST /payments` body {to_handle, amount, note?, visibility?}; `note` default `""`, `visibility` default `"public"` | todo |
| 105 | PM-02 | 201 payment body: payment_id, from_user_id, from_handle, to_user_id, to_handle, amount, currency, note, visibility, request_id (null), created_at (+ settlement_id null per ST-09) | todo |
| 106 | PM-03 | Caller balance below `amount` → 409 `insufficient_funds` | todo |
| 107 | PM-04 | `amount` < 1, > 1000000000 or non-integer → 422 `validation_failed` | todo |
| 108 | PM-05 | `to_handle` is caller's own → 422 `self_payment` | todo |
| 109 | PM-06 | `note` > 200 characters → 422 `validation_failed` | todo |
| 110 | PM-07 | `visibility` neither `public` nor `private` → 422 `validation_failed` | todo |
| 111 | PM-08 | No user has that handle → 404 `not_found` | todo |
| 112 | PM-09 | Debit and credit one atomic step; never visible in one wallet and not the other; failed payment leaves no trace | todo |
| 113 | PM-10 | `note` stored and returned verbatim (no trim/escape/normalise); Unicode/emoji round-trip byte for byte | todo |
| 114 | RQ-01 | `POST /requests` body {payer_handle, amount, note?}; caller is requester | todo |
| 115 | RQ-02 | 201 request body: request_id, requester_id, requester_handle, payer_id, payer_handle, amount, currency, note, status `pending`, payment_id null, created_at | todo |
| 116 | RQ-03 | `amount` < 1, > 1000000000 or non-integer → 422 `validation_failed` | todo |
| 117 | RQ-04 | `payer_handle` is caller's own → 422 `self_request` | todo |
| 118 | RQ-05 | `note` > 200 characters → 422 `validation_failed` | todo |
| 119 | RQ-06 | No user has that handle → 404 `not_found` | todo |
| 120 | RQ-07 | Payer's balance not checked at creation | todo |
| 121 | PY-01 | `POST /requests/{id}/pay` idempotent; only payer may call | todo |
| 122 | PY-02 | Body `visibility` only, optional, default `"public"`; `{}` and `{"visibility":"public"}` are different bodies for replay | todo |
| 123 | PY-03 | 201 with the created payment as in `POST /payments`, `request_id` set; request becomes `paid` with `payment_id` | todo |
| 124 | PY-04 | Request not `pending` → 409 `request_not_pending` | todo |
| 125 | PY-05 | Payer balance below amount → 409 `insufficient_funds` | todo |
| 126 | PY-06 | Caller not the payer → 403 `forbidden` | todo |
| 127 | PY-07 | Unknown request → 404 `not_found` | todo |
| 128 | PY-08 | Replay of successful pay → 200 original payment body even when already `paid`; no extra money; never 409 `request_not_pending` | todo |
| 129 | DC-01 | `POST /requests/{id}/decline`: only payer, no idempotency key, 200 with request `status: "declined"` | todo |
| 130 | DC-02 | Decline already-declined → 200 current state | todo |
| 131 | DC-03 | Decline `paid` or `cancelled` → 409 `request_not_pending` | todo |
| 132 | DC-04 | Decline by non-payer → 403 `forbidden` | todo |
| 133 | CN-01 | `POST /requests/{id}/cancel`: only requester, no idempotency key, 200 with request `status: "cancelled"` | todo |
| 134 | CN-02 | Cancel already-cancelled → 200 | todo |
| 135 | CN-03 | Cancel `paid` or `declined` → 409 `request_not_pending` | todo |
| 136 | CN-04 | Cancel by non-requester → 403 `forbidden` | todo |
| 137 | LR-01 | `GET /requests` returns only requests where caller is requester or payer, newest first by `created_at` | todo |
| 138 | LR-02 | `direction`: `incoming` (caller payer), `outgoing` (caller requester), absent = both; unknown → 422 | todo |
| 139 | LR-03 | `status`: one of four statuses or absent = all; unknown → 422 | todo |
| 140 | LR-04 | `limit` default 50 (1–200), `offset` default 0 (≥ 0); out of range → 422 `validation_failed` | todo |
| 141 | LR-05 | Response `{"requests":[...], "has_more": bool}`; `has_more` true when items exist beyond last returned | todo |
| 142 | SP-01 | `POST /splits` idempotent; body {amount, participant_handles, note?}; caller may be included or omitted | todo |
| 143 | SP-02 | Shares by §9 rule in given handle order; one `pending` request per participant except caller, caller as requester | todo |
| 144 | SP-03 | 201 body: split_id, amount, currency, note, shares [{handle, amount}], requests [...], created_at | todo |
| 145 | SP-04 | `shares` covers every participant incl. caller in given order and sums to `amount`; `requests` excludes caller, same order | todo |
| 146 | SP-05 | `amount` < 1, > 1000000000 or non-integer → 422 `validation_failed` | todo |
| 147 | SP-06 | `participant_handles` empty or containing duplicate → 422 `validation_failed` | todo |
| 148 | SP-07 | `note` > 200 characters → 422 `validation_failed` | todo |
| 149 | SP-08 | Any handle unknown → 404 `not_found` | todo |
| 150 | SP-09 | Caller as only participant is valid: one share, zero requests, `"requests": []` | todo |
| 151 | SP-10 | A split checks nobody's balance | todo |
| 152 | AC-01 | `GET /activity` → payments visible per FD-02, newest first by `created_at`; `{"payments":[...], "has_more": bool}` | todo |
| 153 | AC-02 | Same-second order unspecified; stable pagination under concurrent writes not required | todo |
| 154 | AC-03 | `limit`/`offset` behave exactly as in `GET /requests` | todo |
| 155 | MR-01 | Shares whole minor units, sum exactly to `amount`, differ by at most 1 | todo |
| 156 | MR-02 | Remainder units go to the first participants in `participant_handles` order | todo |
| 157 | MR-03 | Table: 1000/3 → 334, 333, 333 | todo |
| 158 | MR-04 | Table: 1/3 → 1, 0, 0 | todo |
| 159 | MR-05 | Table: 10/3 → 4, 3, 3 | todo |
| 160 | MR-06 | Table: 999/3 → 333, 333, 333 | todo |
| 161 | MR-07 | Table: 5/5 → 1, 1, 1, 1, 1 | todo |
| 162 | MR-08 | Different handle order gives the extra unit to a different person | todo |
| 163 | MR-09 | A share of 0 is legal and still produces a request for that participant | todo |
| 164 | MR-10 | Each split's shares independent of previous splits; after any splits paid in full, balances sum to seeded total | todo |
| 165 | EX-01 | `GET /_test/export` and `POST /_test/import` exist, unauthenticated | todo |
| 166 | EX-02 | Export → 200 JSON object with `track: "pocketful"`, `format_version: 1`, `state` (opaque object) | todo |
| 167 | EX-03 | Export is an atomic read-only snapshot; later source writes do not change it | todo |
| 168 | EX-04 | Import takes the entire export object, atomically replaces state, → 204; accepts unchanged export of this service | todo |
| 169 | EX-05 | No dependency on source process, files, volume, port or network address | todo |
| 170 | EX-06 | Import is replacement, not merge; repeating restores state without duplication | todo |
| 171 | EX-07 | Import invalid JSON → §5 (400 `malformed_request`) | todo |
| 172 | EX-08 | Import missing fields, wrong track/version or invalid state → 422 `validation_failed`, destination unchanged | todo |
| 173 | EX-09 | Import/export 10 s timeout | todo |
| 174 | EX-10 | Preserve accounts, hashed-password login, existing bearer tokens, currency, balances, payments, requests, permissions (operators) | todo |
| 175 | EX-11 | Preserve all completed idempotent request bodies and original responses; replays still return 200 original after import | todo |
| 176 | EX-12 | Identities, timestamps, monetary records not regenerated or replayed against net balances | todo |
| 177 | EX-13 | Failed request keys remain reusable after import | todo |
| 178 | EX-14 | Import removes all previous destination data and credentials | todo |
| 179 | EX-15 | Reset clears all state, including imported state | todo |
| 180 | EX-16 | Preserve settlement membership (`settlement_id` on payments) and settlement retry responses through export/import | todo |
| 181 | ST-01 | Operators: `settlement_operator_ids` from fixture; operator may settle across any wallets | todo |
| 182 | ST-02 | Operator permission grants no access to others' requests or private activity | todo |
| 183 | ST-03 | `POST /settlements` requires operator + idempotency key; no token → 401; non-operator → 403 `forbidden` | todo |
| 184 | ST-04 | Body `{"transfers":[{from_handle, to_handle, amount, note?, visibility?}]}`; 1..32 transfers | todo |
| 185 | ST-05 | Each transfer uses payment amount/note/visibility rules (defaults empty note, public) | todo |
| 186 | ST-06 | Unknown handle → 404 `not_found`; self-transfer → 422 `self_payment`; malformed batch shape → 422 `validation_failed`; unknown fields ignored | todo |
| 187 | ST-07 | Entry errors take precedence in input order, all before insufficient funds | todo |
| 188 | ST-08 | Affordable iff every wallet's net balance after all transfers ≥ 0; else 409 `insufficient_funds` | todo |
| 189 | ST-09 | All movements commit together or none; failed validation claims no key, creates no payment or revision | todo |
| 190 | ST-10 | 201 with `settlement_id`, `committed_at`, `payments` in input order | todo |
| 191 | ST-11 | Members are ordinary payments with `settlement_id`; non-member payments expose `settlement_id: null` | todo |
| 192 | ST-12 | Members have `request_id: null` and identical server-assigned `created_at` == `committed_at` | todo |
| 193 | ST-13 | Members follow ordinary activity-feed visibility | todo |
| 194 | ST-14 | Replay → 200 with original complete response | todo |
| 195 | ST-15 | Reset/import preserves operator permissions, original payments, requests, membership and retry responses | todo |
| 196 | ID-13 | A body that parses but is not a JSON object (array, string, number, null) on any POST taking a body → 400 `malformed_request` (coordinator addition) | done |
| 197 | ER-20 | Same rule as ID-13 for every POST endpoint, including signup, login, reset, import (coordinator addition) | done |
| 198 | FX-08 | Reset fixture with wrong JSON types, missing required fields (currency, minor_units, users; user id/email/password/handle/balance; payment id/from/to/amount; request id/requester/payer/amount), duplicate user ids/handles/emails/payment ids/request ids, unknown user ids in payments/requests/settlement_operator_ids, bad handle, minor_units not 0/2/3 → 422 `validation_failed`, state unchanged; unparseable → 400 (coordinator addition) | done |
| 199 | FX-09 | Seeded requests carry status (default `pending`) and optional payment_id; seeded payments carry optional request_id, settlement_id, visibility (default public), note (default ""); created_at server-assigned at reset when absent (coordinator addition) | done |
| 200 | AU-13 | Unknown route → 404 `not_found`; known path, wrong method → 405 `method_not_allowed`, both in the error envelope; bearer checked before body/idempotency on authed endpoints (coordinator addition) | done |
| 201 | ER-21 | Write-path precedence: 401 → missing/invalid Idempotency-Key (400/422) → body parses as object (400) → claimed key (409 / 200 replay) → field validation → resource checks (coordinator addition) | todo |
| 202 | PF-01 | Password hashing never holds the state lock; reset of ~100 users and 50 concurrent logins finish well under 5 s on 2 vCPU (coordinator addition) | done |
