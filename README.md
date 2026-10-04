# pocketful — a three-seat Band dark factory

WeAreDevelopers x BAND "Dark Factory" hackathon · track **pocketful** · team **gheware** (solo)

A coordinator, an implementer and a reviewer, all Claude Code seats in one Band Desktop room, built a wallet and
payments service in four stages from four dispatched messages. Nobody wrote stage code by hand. Read `FACTORY.md`
for how the factory works and what it cost.

| Stage | What | Dispatch → final report | Reject rounds | Official harness, isolated |
|---|---|---|---|---|
| 1 | JSON API, idempotent writes, settlements, export/import | 33 min | 0 | claimed stage 1 |
| 2 | Browser UI, holds and partial captures, lost-response recovery | 1 h 27 min | 0 | claimed stage 2 |
| 3 | Payment corrections, two-axis history, statements and snapshots | 1 h 25 min | 2 | claimed stage 3 |
| 4 | Refunds, operator correction batches | 39 min | 0 | claimed stage 4 |

## Layout

| Path | What |
|---|---|
| `FACTORY.md` | Seats, design choices, setup, measured costs, how bad work is caught |
| `mandates/` | One generic mandate per seat: `coordinator.md`, `implementer.md`, `reviewer.md` |
| `room.json` | The full Band session download, unedited |
| `dispatch/` | The four messages the human sent (the only human input), and the relay script that handed stages over |
| `stage-N/` | A complete, buildable service: `Dockerfile`, `RUN.md`, Go source, `LEDGER.md` (requirements ledger), the implementer's `tests/`, and the reviewer's probe suites in `review*/` |

Each stage folder starts as a copy of the previous accepted folder and extends it. Build and run any stage with the
command in its `RUN.md`; the service needs no network at run time.

## Who made each commit

The seats share one checkout and commit with its default Git identity. The room log records which seat made each
commit: every hash below appears in `room.json` in that seat's own report.

| Commit | Seat | Summary |
|---|---|---|
| `c4c5fa2` | human (docs only) | Factory mandates and docs (human-authored; no stage code) |
| `4c1ad28` | implementer | stage-1: requirements ledger (LEDGER.md) |
| `7206ca8` | implementer | stage-1 work item 1: foundation (health, reset, auth, /me, error envelope, Dockerfile, RUN |
| `1b0b910` | implementer | stage-1 work item 2: payments, requests, pay/decline/cancel, listing, activity feed, idemp |
| `d309c71` | implementer | stage-1 work item 3: splits, settlements, export/import, hardening tests, ledger complete |
| `c3cd9ee` | reviewer | stage-1 review: reviewer probe suite (291 + 13 probes) for d309c71 |
| `cf9441b` | implementer | stage-2: start as copy of accepted stage-1 (c3cd9ee) + stage-2 requirements ledger |
| `93504d7` | implementer | stage-2 work item A: authorizations backend (holds, capture, void, lazy expiry, available- |
| `65d0957` | implementer | stage-2 work item B: browser UI (embedded client, visual system, testids, Accept negotiati |
| `edabea4` | implementer | stage-2 work item C: competing clients, uncertain outcomes, upgrade without reload, paging |
| `620406d` | implementer | stage-2 polish: authorize form on wallet, capture >1e9 is capture_exceeds_authorization, v |
| `efd83c2` | reviewer | stage-2 review: reviewer probe suites (api 191, ui 200) and screenshots for 620406d |
| `790f259` | implementer | stage-3: start as copy of accepted stage-2 (efd83c2) + stage-3 requirements ledger |
| `7401317` | implementer | stage-3 work item A: temporal core (revision log, as_of/known_at, statements, snapshots, r |
| `2f7fe23` | implementer | stage-3 work item B: corrections (eighth idempotent path), historical overdraft incl. hold |
| `d6e3a27` | implementer | stage-3 fix (reviewer finding 1): authorization creation on the µs event clock; regression |
| `225faf5` | implementer | stage-3 fix (reviewer finding 2): seeded defaults take the µs reset instant; deterministic |
| `ef56cb6` | reviewer | stage-3 review: reviewer probe suites (s3_probes 195, attack_clock 22, attack_seed 87) and |
| `88c6e5b` | implementer | stage-3 follow-up (finding-2 direction): pinnable clock, nowFloor for checks/sweep/reads,  |
| `dbf8341` | implementer | stage-4: start as copy of accepted stage-3 (88c6e5b) + stage-4 requirements ledger |
| `264d84e` | implementer | stage-4 work item A: refunds (ninth idempotent path), refund_of, correction refund rules,  |
| `414765d` | implementer | stage-4 work item B: correction batches (tenth idempotent path), settlement completeness,  |
| `1ae795b` | reviewer | stage-4 review: reviewer probe suites (s4_probes 158, perf_and_ui 5) for 414765d |

## Check it

```sh
python -m harness check <clone> --track pocketful
python -m harness run --track pocketful --repo <clone> --all --mode isolated
```

The harness is the event's, from `band-ai/dark-factory-wearedevs`.
