# Stage 4 reviewer probes

Independent checks written from the stage-4 requirements text by the reviewer seat. Stdlib Python
(Playwright only for the UI part of `perf_and_ui.py`); they reuse `../review-s3p/s3_probes.py` (own naive
oracle) and `../review/probes.py`.

- `s4_probes.py` — refunds (idempotency, receiver-only, targets incl. request payments, captures and
  settlement members, refund-of-refund, cumulative vs current corrected amount, available funds with holds,
  no reopen/no hold restore, history at ±1µs), corrections vs refunds, correction batches (401/403/key/shape,
  item errors in input order, incomplete_settlement, identical member instants across offsets, combined
  affordability, historical overdraft by combination, atomic rollback, shared recorded_at, replay), concurrency
  with an oracle scan, imports of real stage-1/2/3 exports, stage-4 round trip, reset.
  Args: `<s4> <s4-2> <stage1> <stage2> <stage3>`.
- `perf_and_ui.py` — 10^4-payment wallet timings and a refund rendered in the feed at 375 px. Args: `<s4>`.

Results at 414765d: s4_probes 158/158, perf_and_ui 5/5.
