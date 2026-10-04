# Stage 2 requirements ledger — Pocketful: wallet screens and payment authorizations

Lines 1–202 carry the stage-1 ledger forward unchanged in id and text. They stay `done`
except where stage 2 changes the behaviour, which are reopened as `todo (S2: …)`. Lines 203+
are this stage's normative statements (ids `S2-…`). Status: `todo` | `done` | `n/a (reason)`.

Serialization point: unchanged from stage 1, one global `Server.mu` for all state. Expiry is
applied lazily: every locked operation first expires open authorisations whose `expires_at` is
at or before the clock, so reads and writes always see the expired state.

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
| 12 | DL-05 | Runtime assets (fonts, scripts, stylesheets) included in the image | todo (S2: UI assets — CSS/JS/fonts — must be in the image, no CDN) |
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
| 53 | FX-01 | Fixture shape: `currency`, `minor_units`, `users[]` (id, email, password, display_name, handle, balance), `payments[]` (id, from_user_id, to_user_id, amount, note, visibility), `requests[]` (id, requester_id, payer_id, amount, note, status) | todo (S2: fixture adds authorization_ttl_seconds, authorizations) |
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
| 91 | ID-01 | Idempotency key required on `POST /payments`, `POST /requests`, `POST /requests/{id}/pay`, `POST /splits`, `POST /settlements`, each independently | todo (S2: seven idempotent write paths) |
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
| 103 | API-01 | `GET /me` → {user_id, display_name, handle, balance, currency, minor_units} | todo (S2: /me adds total, available, held) |
| 104 | PM-01 | `POST /payments` body {to_handle, amount, note?, visibility?}; `note` default `""`, `visibility` default `"public"` | done |
| 105 | PM-02 | 201 payment body: payment_id, from_user_id, from_handle, to_user_id, to_handle, amount, currency, note, visibility, request_id (null), created_at (+ settlement_id null per ST-09) | todo (S2: payment body adds authorization_id) |
| 106 | PM-03 | Caller balance below `amount` → 409 `insufficient_funds` | todo (S2: insufficient_funds now against available) |
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
| 125 | PY-05 | Payer balance below amount → 409 `insufficient_funds` | todo (S2: insufficient_funds now against available) |
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
| 137 | LR-01 | `GET /requests` returns only requests where caller is requester or payer, newest first by `created_at` | todo (S2: GET /requests with Accept: text/html serves the UI) |
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
| 174 | EX-10 | Preserve accounts, hashed-password login, existing bearer tokens, currency, balances, payments, requests, permissions (operators) | todo (S2: export/import must also carry authorizations, ttl, capture records; stage-1 exports must import) |
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
| 188 | ST-08 | Affordable iff every wallet's net balance after all transfers ≥ 0; else 409 `insufficient_funds` | todo (S2: net debits against available) |
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
| 203 | S2-SC-01 | The stage-1 requirements continue to apply, with the stage-2 additions | todo |
| 204 | S2-SC-02 | Users manage payments, requests and bill splits in a browser, and can reserve money for a recipient to collect later in one or more captures | todo |
| 205 | S2-RT-01 | Route `/` reachable by URL: balance, pay form, request form and activity feed | todo |
| 206 | S2-RT-02 | Route `/requests` reachable by URL: incoming and outgoing requests with pay, decline, cancel | todo |
| 207 | S2-RT-03 | Route `/split` reachable by URL: split form | todo |
| 208 | S2-RT-04 | Route `/signup` reachable by URL: signup | todo |
| 209 | S2-RT-05 | Route `/login` reachable by URL: login | todo |
| 210 | S2-RT-06 | Route `/authorizations` reachable by URL: authorisation list (with capture/void) | todo |
| 211 | S2-RT-07 | Other screens reachable through the UI (consistent navigation) | todo |
| 212 | S2-RT-08 | `GET /requests` with `Accept: text/html` returns the UI; without that header returns JSON (stage-1 API unchanged) | todo |
| 213 | S2-RT-09 | `GET /authorizations` with `Accept: text/html` returns the UI; otherwise JSON | todo |
| 214 | S2-RT-10 | UI exposes every listed `data-testid`; extra elements allowed | todo |
| 215 | S2-UX-01 | Coherent, presentation-ready consumer finance product with a calm, trustworthy character (not a test harness) | todo |
| 216 | S2-UX-02 | Available funds are the clearest monetary value once holds exist; total and held visibly secondary | todo |
| 217 | S2-UX-03 | Payments, requests, splits and authorisations easy to scan; status, direction, privacy and money movement understandable without raw API data | todo |
| 218 | S2-UX-04 | Consistent visual system for typography, spacing, colour, controls and feedback | todo |
| 219 | S2-UX-05 | Primary actions easy to identify | todo |
| 220 | S2-UX-06 | Available, held, pending, loading, successful, refused and uncertain states are visually distinct | todo |
| 221 | S2-UX-07 | People, amounts and timestamps formatted for people; technical ids only where they help | todo |
| 222 | S2-UX-08 | Required flows usable at a 375 CSS-px viewport with no horizontal page scrolling | todo |
| 223 | S2-UX-09 | Required flows usable at conventional desktop widths with no horizontal page scrolling | todo |
| 224 | S2-UX-10 | Every input has a visible label | todo |
| 225 | S2-UX-11 | Keyboard focus is visibly apparent | todo |
| 226 | S2-UX-12 | Text and controls have sufficient contrast (target WCAG AA 4.5:1) | todo |
| 227 | S2-UX-13 | Considered empty states | todo |
| 228 | S2-UX-14 | Considered loading states | todo |
| 229 | S2-UX-15 | Considered error states | todo |
| 230 | S2-UX-16 | Navigation consistent across the required routes | todo |
| 231 | S2-UX-17 | All UI assets (CSS, JS, any fonts) served from the image; no CDN or external fetch at run time | todo |
| 232 | S2-AU-01 | `signup-email`, `signup-password`, `signup-display-name` inputs | todo |
| 233 | S2-AU-02 | `signup-submit` button | todo |
| 234 | S2-AU-03 | `login-email`, `login-password` inputs and `login-submit` button | todo |
| 235 | S2-AU-04 | `auth-error` error message, present only when there is one | todo |
| 236 | S2-AU-05 | `current-user` visible on every screen when signed in; text contains the display name | todo |
| 237 | S2-AU-06 | `current-handle` text is exactly the caller's handle (no `@`, no other words) | todo |
| 238 | S2-AU-07 | `logout-button` button | todo |
| 239 | S2-WL-01 | `wallet-balance` text is exactly the formatted `total`; carries `data-amount="{minor units}"` | todo |
| 240 | S2-WL-02 | `pay-handle`, `pay-amount`, `pay-note` inputs; `pay-amount` is a decimal string as typed (e.g. `15.00`) | todo |
| 241 | S2-WL-03 | `pay-visibility` selects `public` or `private`; option values are exactly those strings | todo |
| 242 | S2-WL-04 | `pay-submit` button | todo |
| 243 | S2-WL-05 | `pay-error` shown when the payment is refused, including insufficient funds | todo |
| 244 | S2-WL-06 | Request form: `request-handle`, `request-amount`, `request-note`, `request-submit` | todo |
| 245 | S2-WL-07 | `request-error` shown when the request is refused | todo |
| 246 | S2-WL-08 | Pay form keeps its values after success | todo |
| 247 | S2-WL-09 | Resubmitting the unchanged pay form sends no new payment: balance falls once, feed has one payment, `pay-error` absent (same key + body replay) | todo |
| 248 | S2-WL-10 | Changing any pay field makes the next submission a new payment (new key) | todo |
| 249 | S2-WL-11 | UI retries follow §7 | todo |
| 250 | S2-WL-12 | Formatted amount: exactly `minor_units` decimals, one space, currency code (`100.00 EUR`) | todo |
| 251 | S2-WL-13 | `minor_units` 0: no decimal point (`1200 JPY`) | todo |
| 252 | S2-WL-14 | Formatted amounts carry no sign | todo |
| 253 | S2-WL-15 | Decimal input converts to minor units exactly: `15.00` and `15` → 1500, `15.5` → 1550 (minor_units 2); no floats | todo |
| 254 | S2-WL-16 | Nonnumeric input, or more than `minor_units` decimals (`15.005`), shows the form's error element and sends no request; never rounds | todo |
| 255 | S2-FD-01 | `activity-list` container; children newest first in the DOM | todo |
| 256 | S2-FD-02 | `activity-item-{payment_id}` one per visible payment, with `data-visibility="public"|"private"` | todo |
| 257 | S2-FD-03 | `activity-parties-{payment_id}` text contains both handles | todo |
| 258 | S2-FD-04 | `activity-amount-{payment_id}` text is exactly the formatted amount | todo |
| 259 | S2-FD-05 | `activity-note-{payment_id}` text is exactly the note; present even when the note is empty | todo |
| 260 | S2-FD-06 | `empty-activity` shown instead of the list when nothing is visible | todo |
| 261 | S2-RQ-01 | `incoming-list` and `outgoing-list` containers on `/requests` | todo |
| 262 | S2-RQ-02 | `request-item-{request_id}` one per request, with `data-status="{status}"` | todo |
| 263 | S2-RQ-03 | `request-amount-{request_id}` text is exactly the formatted amount | todo |
| 264 | S2-RQ-04 | `request-pay-{request_id}` button present only on a pending incoming request | todo |
| 265 | S2-RQ-05 | `request-decline-{request_id}` button present only on a pending incoming request | todo |
| 266 | S2-RQ-06 | `request-cancel-{request_id}` button present only on a pending outgoing request | todo |
| 267 | S2-RQ-07 | `request-error` shown when a pay, decline or cancel is refused | todo |
| 268 | S2-RQ-08 | `empty-requests` shown when both lists are empty | todo |
| 269 | S2-SP-01 | `split-amount` decimal input, same rule as `pay-amount` | todo |
| 270 | S2-SP-02 | `split-handles` text input: comma-separated handles, in order | todo |
| 271 | S2-SP-03 | `split-note` input and `split-submit` button | todo |
| 272 | S2-SP-04 | `split-preview` shows computed shares before submitting, with one `split-share-{handle}` per participant | todo |
| 273 | S2-SP-05 | `split-share-{handle}` text is exactly the formatted share | todo |
| 274 | S2-SP-06 | `split-error` shown when the split is refused | todo |
| 275 | S2-SP-07 | Preview uses the §9 rule; preview and submitted split have identical shares | todo |
| 276 | S2-RF-01 | After any successful action, balance, feed and request lists on the same page show the new state without manual reload | todo |
| 277 | S2-RF-02 | Refresh after a write waits for the write to succeed | todo |
| 278 | S2-RF-03 | No live-update/polling requirement (only own actions and explicit refresh) | n/a (no requirement: stated as not required) |
| 279 | S2-CC-01 | `wallet-refresh` button on `/` refreshes balance and feed without clearing the pay form | todo |
| 280 | S2-CC-02 | Latest refresh wins: an earlier read arriving late never overwrites a later refresh (out-of-order responses) | todo |
| 281 | S2-CC-03 | Refused payment shows `pay-error`, refreshes balance and feed, and preserves all pay inputs | todo |
| 282 | S2-CC-04 | Request cancelled elsewhere: refused pay shows `request-error` and refreshes the list so the stale pay button disappears | todo |
| 283 | S2-CC-05 | Lost payment response (incl. after commit) shows `pay-uncertain` (nonempty text), not `pay-error` | todo |
| 284 | S2-CC-06 | The unchanged form stays retryable with the same key and body after an uncertain outcome | todo |
| 285 | S2-CC-07 | Successful retry removes `pay-error` and `pay-uncertain`, refreshes balance and feed, money moves exactly once | todo |
| 286 | S2-CC-08 | Unknown outcomes are never shown as confirmed rejections | todo |
| 287 | S2-CC-09 | Refresh rules also apply to available and held amounts | todo |
| 288 | S2-CC-10 | No background polling, live sync or reload recovery required | n/a (no requirement: stated as not required) |
| 289 | S2-UP-01 | A stage-2 service accepts an export produced by our stage-1 service (`POST /_test/import`) | todo |
| 290 | S2-UP-02 | A browser signed in before the export/import upgrade stays signed in afterwards | todo |
| 291 | S2-UP-03 | Pending requests remain payable through the request screen after the upgrade | todo |
| 292 | S2-UP-04 | A payment whose response was lost before export is retryable after import with the same key and body; the UI recovers the original payment and refreshes the imported balance | todo |
| 293 | S2-UP-05 | No page reload or new screen needed; the form and pending retry identity survive the upgrade (import between browser requests only) | todo |
| 294 | S2-AZ-01 | An authorisation can be captured later for the full amount or less | todo |
| 295 | S2-AZ-02 | An authorisation places a hold on the payer: reserves money without moving it | todo |
| 296 | S2-AZ-03 | Capturing moves money; a final capture releases whatever was not captured | todo |
| 297 | S2-AZ-04 | Nonfinal captures keep the remainder held | todo |
| 298 | S2-AZ-05 | An open authorisation expires on its own and releases its remainder | todo |
| 299 | S2-AZ-06 | Invariant: sum of wallet `total` always equals the last reset's seeded total; holds move no money | todo |
| 300 | S2-AZ-07 | Invariant: `available = total − held` is never negative | todo |
| 301 | S2-AZ-08 | Held funds cannot fund new payments, authorisations or settlement net debits | todo |
| 302 | S2-AZ-09 | Captures may spend the money reserved for them | todo |
| 303 | S2-AZ-10 | Invariant: cumulative captures never exceed the authorised amount | todo |
| 304 | S2-AZ-11 | Each idempotent capture moves money once | todo |
| 305 | S2-AZ-12 | A closed hold cannot be captured again | todo |
| 306 | S2-API-01 | `GET /me` keeps `balance`; `balance` always equals `total`; adds `total`, `available`, `held` | todo |
| 307 | S2-API-02 | `held` is the sum of open holds; `available = total − held` | todo |
| 308 | S2-API-03 | With no open holds: `balance` = `total` = `available`, `held` = 0, earlier behaviour unchanged | todo |
| 309 | S2-API-04 | `POST /payments` stays an immediate transfer; no intermediate hold or capture | todo |
| 310 | S2-API-05 | `409 insufficient_funds` on `POST /payments` is evaluated against `available` | todo |
| 311 | S2-API-06 | `409 insufficient_funds` on `POST /requests/{id}/pay` is evaluated against `available` | todo |
| 312 | S2-API-07 | Settlement affordability uses `available` (net debits cannot spend held funds) | todo |
| 313 | S2-API-08 | Paying a request stays immediate; authorising a request is out of scope | todo |
| 314 | S2-API-09 | `POST /splits` unchanged | todo |
| 315 | S2-API-10 | Seven idempotent write paths: stage 1's five, `POST /authorizations`, `POST /authorizations/{id}/capture`; replay rules apply to each independently | todo |
| 316 | S2-API-11 | Payments not created by a capture carry `authorization_id: null`; `request_id` semantics unchanged | todo |
| 317 | S2-FX-01 | Fixture `authorization_ttl_seconds` applies to every API-created authorisation; default 600 when omitted | todo |
| 318 | S2-FX-02 | `authorization_ttl_seconds`, if supplied, must be a positive integer number of seconds, otherwise 422 `validation_failed` | todo |
| 319 | S2-FX-03 | Fixture `authorizations[]`: id, from_user_id, to_user_id, amount, note, visibility, status, absolute `expires_at` | todo |
| 320 | S2-FX-04 | Seeded `balance` is `total`; `available` is derived, never seeded (seeded open holds subtracted) | todo |
| 321 | S2-FX-05 | Sum of a user's seeded unexpired open holds > their `balance` → 422 `validation_failed` from reset, nothing changed | todo |
| 322 | S2-FX-06 | Seeded `status` is `open`, `captured`, `voided` or `expired`; only `open` holds funds | todo |
| 323 | S2-FX-07 | `authorizations` omitted → empty list (stage-1 fixtures stay valid) | todo |
| 324 | S2-FX-08 | Seeded authorisation validation: unknown user ids, duplicate ids, bad status/visibility/amount, unparseable `expires_at` → 422 (fixture-validation rule) | todo |
| 325 | S2-EXP-01 | An authorisation whose `expires_at` is at or before now is `expired` and holds no funds | todo |
| 326 | S2-EXP-02 | Reads and writes reflect expiry even if no request happened at the deadline | todo |
| 327 | S2-EXP-03 | `GET /authorizations` shows `status: "expired"` for clock-expired authorisations | todo |
| 328 | S2-EXP-04 | `GET /me` includes the released remainder in `available` | todo |
| 329 | S2-EXP-05 | Seeded `expires_at` may be in the past (expired at reset) or the future; API-created ones may be short-lived | todo |
| 330 | S2-AC-01 | `POST /authorizations` requires `Idempotency-Key`; the caller is the payer | todo |
| 331 | S2-AC-02 | Body `to_handle`, `amount`, optional `note` (default "") and `visibility` (default "public") | todo |
| 332 | S2-AC-03 | 201 body: authorization_id, from_user_id, from_handle, to_user_id, to_handle, amount, captured_amount 0, currency, note, visibility, status `open`, expires_at, payment_id null, created_at, remaining_amount, payment_ids [] | todo |
| 333 | S2-AC-04 | `expires_at` = `created_at` + `authorization_ttl_seconds` | todo |
| 334 | S2-AC-05 | Caller's `available` below `amount` → 409 `insufficient_funds` | todo |
| 335 | S2-AC-06 | `amount` below 1, above 1000000000 or not an integer → 422 `validation_failed` | todo |
| 336 | S2-AC-07 | `to_handle` is the caller's own → 422 `self_payment` | todo |
| 337 | S2-AC-08 | `note` over 200 characters or bad `visibility` → 422 `validation_failed` | todo |
| 338 | S2-AC-09 | No user has that handle → 404 `not_found` | todo |
| 339 | S2-AC-10 | An authorisation is never an activity-feed item | todo |
| 340 | S2-CP-01 | `POST /authorizations/{id}/capture` requires `Idempotency-Key`; only the receiver may capture | todo |
| 341 | S2-CP-02 | Body `amount` optional, defaulting to the remaining amount | todo |
| 342 | S2-CP-03 | A replay must send the identical body: `{}` vs `{"amount":2000}` with one key → 409 `idempotency_key_reuse` | todo |
| 343 | S2-CP-04 | 201 with a payment in exactly the `POST /payments` shape, `authorization_id` set, `request_id: null` | todo |
| 344 | S2-CP-05 | Payment amount = captured amount; note and visibility copied from the authorisation; appears in the feed by the ordinary rule | todo |
| 345 | S2-CP-06 | Default (final) capture: status `captured`, `captured_amount`, `payment_id` set, uncaptured remainder released in the same step | todo |
| 346 | S2-CP-07 | A capture after a final capture → 409 `authorization_not_open` | todo |
| 347 | S2-CP-08 | `final` is boolean, default `true` (wrong type → 400 `malformed_request` by the §5 wrong-type rule) | todo |
| 348 | S2-CP-09 | `final: false` with a remainder left: status stays `open`; further captures allowed up to the remainder | todo |
| 349 | S2-CP-10 | Capturing the entire remainder closes the authorisation even with `final: false` | todo |
| 350 | S2-CP-11 | A final capture closes the authorisation and releases any remainder | todo |
| 351 | S2-CP-12 | `capture_exceeds_authorization` compares with the remaining amount; omitted amount = remainder | todo |
| 352 | S2-CP-13 | `captured_amount` is cumulative; `payment_id` is the latest capture; `payment_ids` lists every capture in order | todo |
| 353 | S2-CP-14 | Every authorisation response includes `remaining_amount` (amount still held, 0 when closed) | todo |
| 354 | S2-CP-15 | Void and expiry can close a partially captured authorisation, release only the remainder, and keep all capture records | todo |
| 355 | S2-CP-16 | New fields do not change idempotency body equality | todo |
| 356 | S2-CP-17 | Not `open` → 409 `authorization_not_open` | todo |
| 357 | S2-CP-18 | `expires_at` at or before now → 409 `authorization_expired` | todo |
| 358 | S2-CP-19 | `amount` above the uncaptured remainder → 422 `capture_exceeds_authorization` | todo |
| 359 | S2-CP-20 | `amount` below 1 or not an integer → 422 `validation_failed` | todo |
| 360 | S2-CP-21 | Caller is not the receiver (incl. neither party) → 403 `forbidden` | todo |
| 361 | S2-CP-22 | Unknown authorisation → 404 `not_found` | todo |
| 362 | S2-VD-01 | `POST /authorizations/{id}/void`: only the payer; no idempotency key | todo |
| 363 | S2-VD-02 | 200 with the authorisation, `status: "voided"`, hold released | todo |
| 364 | S2-VD-03 | Voiding an already-voided authorisation → 200 with the current state | todo |
| 365 | S2-VD-04 | Voiding a `captured` or `expired` authorisation → 409 `authorization_not_open` | todo |
| 366 | S2-VD-05 | Caller is not the payer (incl. neither party) → 403 `forbidden`; unknown → 404 `not_found` | todo |
| 367 | S2-LA-01 | `GET /authorizations` returns only authorisations where the caller is payer or receiver, newest first by `created_at` | todo |
| 368 | S2-LA-02 | `direction`: `outgoing` (payer), `incoming` (receiver), absent = both; unknown value → 422 | todo |
| 369 | S2-LA-03 | `status`: one of the four, or absent = all; clock-expired matches `expired`, never `open`; unknown value → 422 | todo |
| 370 | S2-LA-04 | `limit`, `offset`, `has_more` behave exactly as on `GET /requests`; body `{"authorizations": [...], "has_more": bool}` | todo |
| 371 | S2-UA-01 | `wallet-balance` shows formatted `total` with `data-amount`, as before | todo |
| 372 | S2-UA-02 | `wallet-available` shows formatted `available` with `data-amount`, presented as the headline number | todo |
| 373 | S2-UA-03 | `wallet-held` shows formatted `held` with `data-amount`; absent when `held` is 0 | todo |
| 374 | S2-UA-04 | Authorise form `authorize-handle`, `authorize-amount`, `authorize-note`, `authorize-visibility`, `authorize-submit`, with the pay-form input rules | todo |
| 375 | S2-UA-05 | `authorize-error` shown when an authorisation is refused, including insufficient available funds | todo |
| 376 | S2-UA-06 | `authorization-list` container on `/authorizations`; children newest first in the DOM | todo |
| 377 | S2-UA-07 | `authorization-item-{id}` with `data-status="{status}"` | todo |
| 378 | S2-UA-08 | `authorization-amount-{id}` text is exactly the formatted authorised amount | todo |
| 379 | S2-UA-09 | `authorization-captured-{id}` formatted captured amount, present only when status is `captured` | todo |
| 380 | S2-UA-10 | `authorization-expires-{id}` text is the RFC 3339 `expires_at` | todo |
| 381 | S2-UA-11 | `authorization-capture-amount-{id}` decimal input pre-filled with the remaining amount; only on an incoming open authorisation | todo |
| 382 | S2-UA-12 | `authorization-capture-{id}` button only on an incoming open authorisation | todo |
| 383 | S2-UA-13 | `authorization-void-{id}` button only on an outgoing open authorisation | todo |
| 384 | S2-UA-14 | `authorization-error` shown when a capture or void is refused | todo |
| 385 | S2-UA-15 | `empty-authorizations` shown when the list is empty | todo |
| 386 | S2-UA-16 | UI reflects seeded and new holds; available shown as the spending balance, including right after a reset with open holds | todo |
| 387 | S2-CON-01 | Concurrent requests give the same results as some serial order (linearizable) | todo |
| 388 | S2-CON-02 | Every requirement holds at every read | todo |
| 389 | S2-EX-01 | Export/import carry authorisations, ttl, capture records and the seven paths' idempotency receipts; format_version stays 1 | todo |
