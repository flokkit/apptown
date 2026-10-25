#!/usr/bin/env python3
"""Report whether narrow Markdown changes could reuse complete heavy evidence.

Report-only: this script never suppresses a test or supplies release evidence.
"""
import argparse
import hashlib
import json
import subprocess
from pathlib import Path

EXECUTION_JOBS = {'web', 'heavy-ok', 'journeys', 'opt-in-tiers'} | {
    'race-' + part for part in ('root', 'backup', 'workflow', 'rest')} | {
    'integration-' + part for part in ('root', 'dependency', 'worker', 'rest')}


def command(args):
    result = subprocess.run(args, capture_output=True, timeout=60)
    if result.returncode:
        raise RuntimeError('command failed: ' + ' '.join(args[:3]))
    return result.stdout


def tree(sha):
    entries = {}
    for record in command(['git', 'ls-tree', '-rz', sha]).split(b'\0'):
        if record:
            header, path = record.split(b'\t', 1)
            mode, kind, oid = header.split(b' ')
            entries[path] = (mode, kind, oid)
    return entries


def excluded(path, entry):
    return (entry[0:2] == (b'100644', b'blob') and path.endswith(b'.md')
            and path.startswith((b'design/', b'releases/')))


def digest(entries):
    h = hashlib.sha256()
    for path, entry in sorted(entries.items()):
        if not excluded(path, entry):
            h.update(b' '.join(entry) + b'\t' + path + b'\0')
    return h.hexdigest()


def equivalent(base, candidate):
    changed = {path for path in base.keys() | candidate.keys() if base.get(path) != candidate.get(path)}
    if not changed:
        return False
    for path in changed:
        for entries in (base, candidate):
            if path in entries and not excluded(path, entries[path]):
                return False
    return digest(base) == digest(candidate)


def source_run(runs, sha):
    if any(run['head_sha'] != sha for run in runs):
        raise ValueError('source lookup returned a different SHA')
    eligible = [run for run in runs if
                (run['event'] == 'push' and run['head_branch'] == 'main') or
                (run['event'] == 'merge_group' and run['head_branch'].startswith('gh-readonly-queue/main/'))]
    latest = max(eligible, key=lambda run: (run['created_at'], run['id']), default=None)
    if latest is None or latest['status'] != 'completed' or latest['conclusion'] != 'success' or latest['run_attempt'] != 1:
        return None
    return latest


def complete(jobs):
    by_name = {job['name']: job for job in jobs}
    return all(name in by_name and by_name[name]['status'] == 'completed' and
               by_name[name]['conclusion'] == 'success' for name in EXECUTION_JOBS)


def api(path):
    return json.loads(command(['gh', 'api', path]))


def report(repo, base_sha, candidate_sha):
    result = {'mode': 'report-only', 'decision': 'execute', 'candidate_sha': candidate_sha,
              'base_sha': base_sha, 'policy': 1}
    try:
        if not base_sha:
            result['reason'] = 'this event requires full execution'
            return result
        base, candidate = tree(base_sha), tree(candidate_sha)
        result['input_digest'] = digest(candidate)
        if not equivalent(base, candidate):
            result['reason'] = 'changes are outside the narrow Markdown contract'
            return result
        run = source_run(api(f'repos/{repo}/actions/workflows/heavy.yml/runs?head_sha={base_sha}&per_page=100')['workflow_runs'], base_sha)
        if run is None:
            result['reason'] = 'newest source run is not complete first-attempt success'
            return result
        jobs = api(f'repos/{repo}/actions/runs/{run["id"]}/jobs?filter=latest&per_page=100')
        if jobs['total_count'] > len(jobs['jobs']) or not complete(jobs['jobs']):
            result['reason'] = 'source lacks complete execution jobs; admission/reuse is not a source'
            return result
        result.update(decision='would-reuse', reason='unchanged inputs and complete source execution',
                      source_run_id=run['id'], source_sha=base_sha, source_url=run['html_url'])
    except (RuntimeError, ValueError, KeyError, TypeError, AttributeError, subprocess.TimeoutExpired) as error:
        result['reason'] = 'cannot establish reuse: ' + str(error)
    return result


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repository', required=True)
    parser.add_argument('--base', default='')
    parser.add_argument('--candidate', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    result = report(args.repository, args.base, args.candidate)
    Path(args.output).write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result))
