# Pocketful stage 1 — run instructions

Build and start (from this folder):

```sh
docker build -t pocketful-stage1 . && docker run --rm -e PORT=8080 -p 8080:8080 pocketful-stage1
```

The service listens on `0.0.0.0:$PORT` (default `8080`). `GET /health` returns
`200 {"status":"ok"}` as soon as the process is up. It needs no network, volume or other
service at run time. All state is in memory and is replaced by `POST /_test/reset`.

Own tests (Go 1.26, standard library only), from this folder:

```sh
go test ./tests/ -count=1 -v
```
