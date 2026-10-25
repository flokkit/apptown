"""Named jobs that constitute full heavy execution, shared by evidence readers."""
from collections import Counter

EXECUTION_JOBS = {'web', 'heavy-ok', 'journeys', 'opt-in-tiers'} | {
    'race-' + part for part in ('root', 'backup', 'workflow', 'rest')} | {
    'integration-' + part for part in ('root', 'dependency', 'worker', 'rest')}


def complete(jobs):
    counts = Counter(job['name'] for job in jobs)
    by_name = {job['name']: job for job in jobs}
    return all(counts[name] == 1 and by_name[name]['status'] == 'completed' and
               by_name[name]['conclusion'] == 'success' for name in EXECUTION_JOBS)


def complete_page(page):
    return page['total_count'] == len(page['jobs']) and complete(page['jobs'])
