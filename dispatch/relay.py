#!/usr/bin/env python3
"""Stage relay for the submitted run. Deterministic; posts ONLY pre-written dispatch files.

For the lowest stage N that has been dispatched but not yet relayed:
  - look for the coordinator's final report for stage N in the room;
  - if found: run the official harness on stage-N in isolated mode, log the outcome,
    then send dispatch N+1 (if it exists and was not sent before).
Never re-dispatches a stage. Never posts anything except a dispatch file.
Usage: relay.py [--dry-run]
"""
import datetime, fcntl, json, pathlib, re, subprocess, sys

ROOT = pathlib.Path(__import__('os').environ.get('FACTORY_ROOT', '/home/rajesh/wad-dark-factory-2026'))  # set FACTORY_ROOT for your layout
ROOM = 'a83d0fb4-1458-476d-9971-3d6b2e470ee2'
COORD_ID = '071c9411-a0cf-46b5-b7b6-95ff404e3485'
RES = ROOT / 'band-work/result'
DISP = ROOT / 'factory/dispatch'
LOG = ROOT / 'factory/run-log.md'
DRY = '--dry-run' in sys.argv

def now(): return datetime.datetime.now().astimezone().isoformat(timespec='seconds')
def sh(cmd, timeout=120, cwd=None): return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout, cwd=cwd)
def log(line):
    print(line)
    if not DRY:
        with LOG.open('a') as f: f.write(f'- {now()} {line}\n')

def room_text():
    out, page = [], 1
    while True:
        r = sh(['band', 'room', 'messages', ROOM, '--type', 'text', '--page', str(page)])
        out.append(r.stdout)
        m = re.search(r'page (\d+)/(\d+)', r.stdout)
        if not m or int(m.group(1)) >= int(m.group(2)): break
        page += 1
    return '\n'.join(out)

def final_report(text, n):
    """First coordinator message announcing the final report for stage n."""
    for line in text.splitlines():
        if '] coordinator (Agent):' in line and re.search(rf'FINAL REPORT\W+(pocketful\s+)?stage[ -]?{n}\b', line, re.I):
            return line[:300]
    return None

def dispatched(n): return (DISP / f'pocketful-stage-{n}.dispatched').exists()
def relayed(n): return (DISP / f'pocketful-stage-{n}.relayed').exists()

def main():
    lock = open('/tmp/pocketful-relay.lock', 'w')
    try: fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError: print('another relay is running; exit'); return
    stage = next((n for n in (1, 2, 3, 4) if dispatched(n) and not relayed(n)), None)
    if stage is None: print('nothing in flight (all dispatched stages relayed)'); return
    rep = final_report(room_text(), stage)
    head = sh(['git', '-C', str(RES), 'log', '--oneline', '-1']).stdout.strip()
    if not rep:
        print(f'stage {stage}: in progress; no final report yet; result HEAD {head}'); return
    log(f'stage {stage}: final report seen: {rep}')
    out = ROOT / f'band-work/checks/s{stage}-relay-{datetime.datetime.now():%H%M%S}'
    if DRY:
        print(f'[dry-run] would run isolated harness --stage {stage} --out {out}')
    else:
        r = sh([str(ROOT/'kickoff/.venv/bin/python'), '-m', 'harness', 'run', '--track', 'pocketful',
                '--repo', str(RES), '--stage', str(stage), '--mode', 'isolated', '--out', str(out)],
               timeout=3600, cwd=str(ROOT / 'kickoff'))
        tail = [l for l in (r.stdout + r.stderr).splitlines() if re.match(r'\s*(stage \d|highest|claimed)', l)] or [f'HARNESS ERROR exit {r.returncode}: ' + (r.stderr or r.stdout).strip()[-200:]]
        log(f'stage {stage}: isolated harness → ' + ' | '.join(t.strip() for t in tail) + f' (report {out}/report.json)')
    nxt = stage + 1
    f = DISP / f'pocketful-stage-{nxt}.md'
    if nxt <= 4 and f.exists() and not dispatched(nxt):
        if DRY:
            print(f'[dry-run] would send {f.name} ({len(f.read_text())} chars) to room {ROOM}')
        else:
            (DISP / f'pocketful-stage-{nxt}.dispatched').write_text(now() + '\n')
            r = sh(['band', 'room', 'send', ROOM, f.read_text(), '--mention', COORD_ID])
            log(f'stage {nxt}: dispatched ({r.stdout.strip() or r.stderr.strip()})')
    elif nxt > 4:
        log('all four stages reported; run complete')
    if not DRY:
        (DISP / f'pocketful-stage-{stage}.relayed').write_text(now() + '\n')

main()
