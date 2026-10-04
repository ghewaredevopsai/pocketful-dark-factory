# Stage 3 reviewer probes

Independent checks written from the stage-3 requirements text by the reviewer seat. Stdlib Python;
they reuse the stage-1 probe helpers in `../review/probes.py`. `s3_probes.py` carries its own naive
oracle (own RFC 3339 parser, revision selection, balance scan).

- `s3_probes.py` — as_of, statements, corrections (check order, field matrix, replay, failures change
  nothing), known_at, historical overdraft (incl. holds), snapshots, historical holds, concurrency,
  seeded history, stage-1/stage-2/stage-3 export import. Args: `<s3> <s3-2> <stage1> <stage2>`.
- `attack_clock.py` — event-clock ordering across all event types, ±1µs views, TTL, stage-2 import with
  same-second events, authorize/correct/pay race. Args: `<s3> <stage2>`.
- `attack_seed.py` — seeded timestamp combinations vs the reset instant, snapshots amid writes,
  reset/import racing in-flight requests, random fractional-seed oracle. Args: `<s3> <stage1> <stage2>`.
- `repro_hold_trunc.py`, `repro_seed_floor.py` — reproductions of review findings 1 and 2 (both fixed).

Results at 225faf5: s3_probes 195/195, attack_clock 22/22, attack_seed 87/87, both repros fixed.
