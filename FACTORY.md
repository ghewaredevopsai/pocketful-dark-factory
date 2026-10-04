# FACTORY.md — the gheware dark factory

Team: gheware · Track: pocketful · Band Desktop 0.4.12

## Seats

| Seat | Harness | Model | Owns |
|---|---|---|---|
| coordinator | Claude Code | claude-sonnet-5-5 | planning, dispatch, routing evidence, acceptance, final report |
| implementer | Claude Code | claude-opus-5-5 | requirements ledger, code, own tests, commits |
| reviewer | Claude Code | claude-opus-5-5 | fresh-checkout verification, probe suite, accept/reject |

All three are Band-owned headless Claude Code runtimes on one machine, sharing one checkout of the result
repository. Each seat's owner instructions are its mandate file, live-linked, so editing the file changes the seat.

## Design choices and why

1. **Ledger before code.** The implementer's first deliverable is `LEDGER.md`: one line
   per normative statement in the spec. Every work item, handoff and review cites ledger
   ids. Reason: the shipped checks cover 79% / 35% / 9% / 16% of each stage; the ledger
   is how the factory targets what the checks never asked.
2. **Reviewer works from a fresh checkout and no-network container.** Judges build the
   Dockerfile on an internal network; the reviewer reproduces that so "works on my
   tree" never passes.
3. **Reviewer writes its own probes.** Retries, concurrent bursts, boundaries, rounding,
   time offsets, every error row, export/import round trips. Probes are committed in the
   stage folder and rerun at the next stage.
4. **Bounded repair loop.** Max six reject/fix rounds per stage; then the coordinator
   records open findings as the outcome instead of looping forever.
5. **Copy-forward stages.** Each stage folder is a copy of the accepted previous folder,
   nested `.git` removed, extended in place.
6. **Generic by construction.** Mandates name roles, evidence formats and gates only.
   Checked with `python -m harness check` against the track vocabulary before every run.

## Setup (another team)

1. Install Band Desktop, sign in (`band init`), install the Claude Code plugin (`band plugin install`), and check
   `band preflight`.
2. Create an empty result repository, then the three seats (repeat per seat with its model):
   ```sh
   band agent create --session seat-<name> --name <name> --description "Dark-factory seat: <name>" \
     --transport claude-code-cli --runtime-auth subscription --runtime-model <model> \
     --claude-context-mode local_config --claude-strict-mcp-config \
     --cwd <absolute result repo> --instructions-file mandates/<name>.md
   ```
   Models: coordinator `claude-sonnet-5-5`, implementer and reviewer `claude-opus-5-5`.
3. In the Band console, create a room as the human with all three seats.
4. Per stage, send one message to `@coordinator`. `dispatch/` holds the four we sent: task, absolute paths, the full
   stage spec inline, the check command, and the exact first line of the final report. Send nothing else until that
   report arrives.
5. Optional: `dispatch/relay.py` watches the room for `FINAL REPORT — stage N`, runs the isolated harness, logs it,
   and sends the next pre-written dispatch. It never posts anything else and never re-sends a stage.
## What it cost (submitted run, measured)

Times are dispatch (human message) → coordinator's final report, from the room log.
Stage 1 → 2 includes an overnight pause on the human side (the stage-1 report did not use the agreed wording, so the
relay held stage 2 for a human go-ahead); it is not factory time.

| Stage | Dispatch → final report | Reject rounds | Isolated harness (shipped checks) |
|---|---|---|---|
| 1 | 22:55 → 23:29 IST (33 min) | 0 | stage 1 pass · claimed 1 |
| 2 | 08:30 → 09:57 IST (1 h 27 min) | 0 (reviewer minor polish before accept) | stages 1–2 pass · claimed 2 |
| 3 | 09:58 → 11:23 IST (1 h 25 min) | 2 (both real timestamp-precision bugs) | stages 1–3 pass · claimed 3 |
| 4 | 11:23 → 12:02 IST (39 min) | 0 | stages 1–4 pass · claimed 4 |

Model usage for the whole run, from the seats' Claude Code transcripts (deduplicated per API message):

| Model (seats) | API calls | Output tokens | Cache-write tokens | Cache-read tokens |
|---|---|---|---|---|
| claude-opus-5-5 (implementer, reviewer) | 519 | 863,944 | 1,799,603 | 233,052,603 |
| claude-sonnet-5-5 (coordinator) | 123 | 103,845 | 360,191 | 20,334,242 |

Seats ran on a Claude subscription (flat fee), so the marginal spend for the run was $0. We did not convert tokens to a
list-price figure. `band usage` could not attribute sessions to individual seats on this machine, so the numbers above
were summed from the seats' own Claude Code session transcripts.

Wall clock for the whole factory: about 4 h 4 min of seat work across the four stages (33 + 87 + 85 + 39 min).

## A bad result it caught

Stage 3, round 1: the reviewer rejected `2f7fe23`. Hold timestamps were whole seconds while payments carried
microseconds, so a hold created right after a payment was dated before it. A historical view then showed a hold that
did not yet exist (available funds negative — forbidden in every view), and a valid correction was refused as a
historical overdraft. The shipped checks pass either way; the reviewer found it with its own spec-derived probes and
a five-step reproduction. Round 2 found the same defect class in seeded data (`d6e3a27`): seeded items without a
creation time took the start of the reset second. Both fixes came back through the room with regression tests and
new invariants in the implementer's oracle.

Process slip it caught: after the stage-3 accept the implementer kept editing on a side branch in the shared tree.
The coordinator saw the dirty tree, stopped the implementer, and only accepted a revision the reviewer had checked.

## What we tried that failed (practice and setup)

- **Seat-owned rooms.** A room created from a seat with `band chat new` is owned by that seat; the human cannot
  dispatch into it and adding participants threw spurious errors. Rooms are created by the human in the Band console.
- **Bare Claude Code seats.** `--claude-context-mode bare` refuses subscription auth. Seats use `local_config` with
  `--claude-strict-mcp-config`, so they inherit the login but none of the operator's own MCP servers.
- **Isolated checks on this host.** The judge-runner image could not download its browser over IPv6; we patched our
  local harness copy to build that one image on the host network. The suites were untouched.
- **Toy rehearsal lessons** (accepted in 8 minutes, round 1): the reviewer first wrote its probes outside the repository
  and the seats estimated timestamps. Both are now explicit mandate rules: probes live in `stage-N/review*/`, every time
  is read from the system clock.
- **Stage-1 report wording.** The stage-1 dispatch did not prescribe a report format, so the coordinator's closing
  message was addressed to the reviewer and our deterministic relay did not recognise it. Stages 2–4 prescribe the
  exact first line `FINAL REPORT — stage N: ACCEPTED <revision>`, and the relay handed over stages 3 and 4 unattended.

## Commit authorship

All commits carry the repository's default Git identity because the seats share one checkout and no per-seat identity
was configured. Which seat made each commit is recorded in the room: every hash appears in `room.json` in the seat's own
report, and `README.md` lists the mapping. History was pushed as the seats left it: no amend, rebase or squash.
