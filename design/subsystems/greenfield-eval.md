# Greenfield evaluation tools

`eval/greenfield` contains specifications, private test suites and a containerized
runner for the ledger, clip, roomie, qkue and mdlite experiments. Its results assess
particular implementations against those suites, not general model capability or
production readiness.

The runner starts candidates and reports a JSON scorecard. Keeping test source
separate from symptom feedback is part of the experiment's protocol. Historical
model/cost comparisons depend on the recorded suite version and conditions.

## The runner does not read the current contract

`eval/greenfield/run.sh` reads `.forge/app.json` and its from-source build and
start fields, not the native `.forge/app.yaml` the forge runs. It is not a test
of the current deployment contract until it is updated, and earlier scores do
not validate it. Review the runner contract and pinned tool inputs before
repeating an experiment.

## Implementation references

- [eval/greenfield/run.sh](../../eval/greenfield/run.sh)
- [eval/greenfield/kit.py](../../eval/greenfield/kit.py)
- [eval/greenfield/tests/VERSION](../../eval/greenfield/tests/VERSION)
