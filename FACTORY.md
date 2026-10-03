# FACTORY.md — draft (fill measured numbers after the submitted run)

Team: gheware · Track: pocketful · Band Desktop 0.4.12

## Seats

| Seat | Harness | Model | Owns |
|---|---|---|---|
| coordinator | Claude Code | claude-sonnet-5-5 | planning, dispatch, routing evidence, acceptance, final report |
| implementer | Claude Code | claude-opus-5-5 | requirements ledger, code, own tests, commits |
| reviewer | Claude Code | claude-opus-5-5 | fresh-checkout verification, probe suite, accept/reject |

All three are Band-owned headless Claude Code runtimes created from Band Desktop
(New local agent), working directory = the absolute path of the result repository,
owner instructions = the mandate file for that seat (live-linked).

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

1. Install Band Desktop, sign in, install the Claude Code plugin, pass readiness.
2. Create three local headless Claude Code seats named `coordinator`, `implementer`,
   `reviewer`; paste each `mandates/<seat>.md` as the seat's instructions; set the
   working directory to the absolute path of an empty result repository.
3. Create a room, add all three seats, dispatch one message to `@coordinator`
   containing the task, the absolute spec path(s), the absolute result repository path
   and the check command to run. Send nothing else until the final report.

## What it cost (to be measured in the submitted run)

| Stage | Dispatch → accept (wall clock) | Reject rounds | Tokens / spend | Harness result |
|---|---|---|---|---|
| 1 | | | | |
| 2 | | | | |
| 3 | | | | |
| 4 | | | | |

## A bad result it caught (fill from room log)

## What we tried that failed (fill from practice runs)
