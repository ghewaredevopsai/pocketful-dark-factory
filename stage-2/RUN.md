# Pocketful stage 2 — run instructions

Build and start (from this folder):

```sh
docker build -t pocketful-stage2 . && docker run --rm -e PORT=8080 -p 8080:8080 pocketful-stage2
```

Then open http://localhost:8080/ in a browser (sign up, or log in as a user from a reset fixture).

The service listens on `0.0.0.0:$PORT` (default `8080`). `GET /health` returns
`200 {"status":"ok"}` as soon as the process is up. It needs no network, volume or other
service at run time, writes no files (it runs with `--read-only --network none`), and runs as an
unprivileged user from a `scratch` image. All state is in memory: `POST /_test/reset` replaces
it with a fixture, `GET /_test/export` snapshots it and `POST /_test/import` restores a snapshot
(including exports made by the stage-1 service).

The browser UI is one HTML shell, one stylesheet and one script embedded in the binary
(`app/ui/`), rendered client-side from the JSON API. It uses the system font stack and makes no
requests outside the service. `/requests` and `/authorizations` serve the UI to
`Accept: text/html` and JSON otherwise.

Own tests (Go 1.26, standard library only), from this folder:

```sh
go test ./tests/ -count=1 -v
CGO_ENABLED=1 go test -race ./tests/ -count=1
```

Real-browser checks (Playwright Chromium from the kickoff venv; containers run on an internal
Docker network with no outbound access; screenshots go to `review-ui/screens/`):

```sh
docker build -t pocketful-stage2 . && docker build -t pocketful-stage1 ../stage-1
<kickoff>/.venv/bin/python review-ui/ui_check.py    # screens, flows, layout, contrast
<kickoff>/.venv/bin/python review-ui/ui_check2.py   # out-of-order refresh, competing clients,
                                                     # lost/timed-out/5xx writes, stage-1 -> stage-2
                                                     # upgrade without reload, long values, paging
```

UI behaviour worth knowing: every idempotent form keeps its Idempotency-Key while its body is
unchanged, so resubmitting (or retrying after an unknown outcome) is a replay; a definite 4xx
starts a new key. A request with no answer within 8 s, a network error or a 5xx shows the
"not confirmed" (uncertain) state, never a refusal. Lists show 50 items with "Show more".

Shipped checks (from the kickoff folder):

```sh
.venv/bin/python -m harness run --track pocketful --repo <result-repo> --stage 2 --out <new-dir>
```
