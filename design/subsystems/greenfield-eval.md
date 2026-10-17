# Greenfield evaluation tools

Status: historical experiment; runner requires contract update. Checked on 18 September 2026.

`eval/greenfield` contains specifications, private test suites and a containerized
runner for the ledger, clip, roomie, qkue and mdlite experiments. Its results assess
particular implementations against those suites, not general model capability or
production readiness.

The runner starts candidates and reports a JSON scorecard. Keeping test source
separate from symptom feedback is part of the experiment's protocol. Historical
model/cost comparisons depend on the recorded suite version and conditions.

## Current compatibility gap

`eval/greenfield/run.sh` still reads `.forge/app.json` and its retired from-source
build/start fields. The production runtime uses native `app.yaml`. Therefore this
runner is not a supported test of the current deployment contract without an
explicit update. The old document's “building” status is not current execution
work, and the historical scores do not validate the current runner.

Review the runner contract and pinned tool inputs before repeating the experiment.
No experiment, Docker launch or paid model request was run during this audit.

## Implementation references

- [eval/greenfield/run.sh](../../eval/greenfield/run.sh)
- [eval/greenfield/kit.py](../../eval/greenfield/kit.py)
- [eval/greenfield/tests/VERSION](../../eval/greenfield/tests/VERSION)
