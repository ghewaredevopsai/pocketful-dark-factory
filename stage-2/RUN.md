# Pocketful stage 1 — run instructions

Build and start (from this folder):

```sh
docker build -t pocketful-stage1 . && docker run --rm -e PORT=8080 -p 8080:8080 pocketful-stage1
```

The service listens on `0.0.0.0:$PORT` (default `8080`). `GET /health` returns
`200 {"status":"ok"}` as soon as the process is up. It needs no network, volume or other
service at run time, writes no files (it runs with `--read-only --network none`), and runs as an
unprivileged user from a `scratch` image. All state is in memory: `POST /_test/reset` replaces
it with a fixture, `GET /_test/export` snapshots it and `POST /_test/import` restores a snapshot.

Own tests (Go 1.26, standard library only), from this folder:

```sh
go test ./tests/ -count=1 -v
CGO_ENABLED=1 go test -race ./tests/ -count=1
```

Shipped checks (from the kickoff folder):

```sh
.venv/bin/python -m harness run --track pocketful --repo <result-repo> --stage 1 --out <new-dir>
```
