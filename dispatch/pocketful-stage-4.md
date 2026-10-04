@coordinator You are the lead seat of our factory. This is a dark-factory run: from this message until your final report, no seat asks the human anything.

Task: build stage 4 of the `pocketful` track.

Result repository (absolute path, commit only here):
/home/rajesh/wad-dark-factory-2026/band-work/result

Stage folder: stage-4/
Start it as a copy of the accepted stage-3/ folder (delete any nested .git inside the copy), then extend the copy. Do not edit stage-3/ or any earlier folder. stage-4/ must still pass every earlier stage's requirements; carry the earlier ledgers forward and add this stage's lines, and rerun the earlier stages' review probes against the new folder.

This stage changes many existing records at once. Every batch is all-or-nothing, and history must stay
truthful after it.

Specification for stage 4: the complete text follows. Section references such as §5 or §7 point into stage 1's specification, which you already hold from stage 1 and which is also on disk at /home/rajesh/wad-dark-factory-2026/kickoff/pocketful/spec/stage-1.md. This stage's text is also on disk, byte-identical, at /home/rajesh/wad-dark-factory-2026/kickoff/pocketful/spec/stage-4.md. Every handoff to another seat must still carry the complete text of this stage (numbered parts if needed), never only the path.

--- BEGIN SPEC (stage 4) ---
# Pocketful — Stage 4: refunds and batch corrections

Recipients can refund payments. Settlement operators can correct several payments in
one request, including payments that belong to a settlement. Existing receipts and saved
statements must remain available in their original form.

All requirements from stages 1–3 continue to apply. There are ten idempotent write paths:
stage 1's five, authorizations and captures from stage 2, corrections from stage 3, and refunds and
correction batches in this stage.

## Refunds and corrected history

`POST /payments/{payment_id}/refunds`, body `{"amount": 200}`, requires an idempotency key.
Only the original receiver may refund, else 403 `forbidden`; unknown payment is 404. The
target may be a direct payment, request payment or capture, but never a refund. Invalid amount
is 422 `validation_failed`. Refunds cumulatively may not exceed the payment's current corrected
amount: 422 `refund_exceeds_payment`. Refunds of refunds give 422 `invalid_refund_target`.

A refund is a new payment in the opposite direction, with `refund_of` naming the target,
`request_id: null`, `authorization_id: null`, and the original note/visibility. Return 201
with that payment; replay returns 200 with the original body. It moves existing money from
the receiver's **available** funds, or fails 409 `insufficient_funds`, atomically. Refunds
never reopen a request or authorization or restore a released hold. Other payments have
`refund_of: null`.

Stage-3 corrections remain available for ordinary direct/request payments. Captures and
refund payments cannot themselves be corrected: 422 `linked_payment_immutable`. A correction
cannot reduce a payment below its already-refunded amount: 422 `refund_exceeds_payment`.
Correction debits are checked against available funds.

## Batch corrections

`POST /correction-batches` requires a settlement operator and an idempotency key, with the
same 401/403 rules as settlements. Body:

```json
{"corrections": [{"payment_id": "p_a", "expected_revision": 1, "amount": 0,
                  "effective_at": "2026-09-20T12:00:00+00:00", "reason": "reversal"},
                 {"payment_id": "p_b", "expected_revision": 1, "amount": 0,
                  "effective_at": "2026-09-20T12:00:00+00:00", "reason": "reversal"}]}
```

corrections contains 1..32 objects with distinct payment_ids, else 422 `validation_failed`.
Every item has the ordinary correction fields and validation. Unknown payment is 404;
a stale expected revision is 409 `stale_revision`. The operator may correct ordinary,
request and settlement payments, but captures and refunds remain immutable. Correcting any
settlement member requires including every member of that settlement, else 422
`incomplete_settlement`. Members of one settlement must have identical effective instants
(offset spellings may differ), else 422 `validation_failed`. Ordinary single-payment
corrections remain available for nonmembers. Unknown fields are ignored.

Error precedence is: item errors in input order, settlement completeness, resulting
current available funds, then historical total and available funds at every effective/event
boundary. The existing codes apply: `linked_payment_immutable`, `refund_exceeds_payment`,
`insufficient_funds`, `historical_overdraft`. Affordability is determined by the combined
effect of all proposed revisions. A rejected batch leaves history, balances and idempotency
records unchanged.

Return 201 with `correction_batch_id`, `recorded_at` and `revisions` in input order. All new
revisions share recorded_at, strictly later than the previous recorded_at of every member;
each revision also exposes correction_batch_id. Effective times cannot be later than now.
Original payments and receipts never change. Original payment and settlement retries return
their original bodies. New statements reflect the new revisions; earlier snapshot tokens
continue to page their frozen entries. Replays return the original batch response with 200.
This adds one idempotent write path.

A settlement payment may be refunded under the existing refund rules, but refunds never
change settlement membership. Concurrent corrections sharing any expected payment revision
cannot
both succeed. A stage-4 service must accept exports produced by the same team's stages 1–3,
retaining settlement membership, corrections and snapshots.
--- END SPEC (stage 4) ---

Check command (every run needs a new --out directory, so number them):
  /home/rajesh/wad-dark-factory-2026/kickoff/.venv/bin/python -m harness run --track pocketful --repo /home/rajesh/wad-dark-factory-2026/band-work/result --stage 4 --out /home/rajesh/wad-dark-factory-2026/band-work/checks/s4-NN
This runs suites 1..4 against stage-4/. The goal is "claimed stage: 4". Final verification by the reviewer must add --mode isolated (internal network, no outbound, 2 vCPU, 2 GiB), because that is how judges run it. The shipped checks are only a small part of the graded suite for this stage: build to the specification text, not to the checks.

Constraints: 2 vCPU, 2 GiB, 60 s to healthy, up to 50 requests in flight, 5 s per request (10 s for test-control calls), no network at run time, listen on 0.0.0.0 at $PORT (default 8080), no 5xx under concurrent load.

Follow your mandate: ledger first, scoped work items to @implementer, independent verification by @reviewer from a fresh checkout in isolated mode, bounded repair loop, accept only a reviewer-accepted revision.
When the stage is finished, post your report to me as one message whose first line is exactly:
FINAL REPORT — stage 4: ACCEPTED <revision>
or, if it could not be accepted:
FINAL REPORT — stage 4: NOT ACCEPTED <last revision>
followed by the check counts, the rejections and what each changed, and the elapsed time from dispatch to report.
