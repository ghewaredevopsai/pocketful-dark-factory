# Stage 2 reviewer probes

Independent checks written from the stage-2 requirements text by the reviewer seat.

- `api_probes.py` — API probes (authorizations, captures, void, expiry, holds in `/me`, fixture rules,
  Accept negotiation, export/import incl. a stage-1 export, concurrency). Stdlib only; reuses the
  stage-1 probe helpers in `../review/probes.py`.
- `ui_review.py` — real-browser checks (Playwright Chromium): every `data-testid` rule, amount
  parsing, form replay, latest-refresh-wins, refused/uncertain outcomes, stage-1 → stage-2 upgrade
  without reload, holds UI, layout at 375/768/1280 px, labels, focus, contrast, same-origin.
- `screens/` — a selection of the reviewer's screenshots.

Start two stage-2 containers and one stage-1 container, then:

```sh
python3 api_probes.py http://127.0.0.1:18090 http://127.0.0.1:18091 http://127.0.0.1:18092   # 191 probes
<kickoff>/.venv/bin/python ui_review.py http://127.0.0.1:18090 http://127.0.0.1:18092 ./screens  # 200 checks
```

Results at 620406d: api_probes 191/191, ui_review 200/200.
