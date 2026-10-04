# Stage 1 reviewer probes

Independent probes written from the stage-1 requirements text (Python 3 stdlib only).
Start two instances of the stage-1 image (the second is the cross-process import target), then:

```sh
python3 probes.py http://127.0.0.1:18080 http://127.0.0.1:18081   # 291 probes
python3 extra.py  http://127.0.0.1:18080                          # 13 probes
```

Each prints `PASS`/`FAIL` per probe and a `TOTAL` line. Reviewed revision: d309c71 (291/291, 13/13).
