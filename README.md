# pocketful-dark-factory — WeAreDevelopers x BAND Dark Factory, pocketful track

Team **gheware** (solo). Built by a three-seat Band Desktop factory; see `FACTORY.md`.

| Path | What |
|---|---|
| `FACTORY.md` | seats, design, costs, failure handling |
| `mandates/` | one generic mandate per seat |
| `room.json` | full Band session download |
| `stage-N/` | complete buildable service for stage N (`Dockerfile`, `RUN.md`, source, `LEDGER.md`, implementer tests, reviewer probes) |

Check: `python -m harness check <clone> --track pocketful` · build and grade:
`python -m harness run --track pocketful --repo <clone> --all --mode isolated`.
