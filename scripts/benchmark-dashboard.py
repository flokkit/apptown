#!/usr/bin/env python3
"""Measure core projections on a disposable database in local AppTown-test-db.

Requires the test PostgreSQL container already running. Seeds synthetic projection
rows directly, not an application lifecycle fixture. Creates/drops only its own
unique database, uses the current schema/queries, and retains EXPLAIN JSON in /tmp.
Three executions per custom/generic plan at 1 and 1,000 visible projects.
Run from any directory: python3 scripts/benchmark-dashboard.py
"""
import json
import pathlib
import re
import statistics
import subprocess
import tempfile
import time

root=pathlib.Path(__file__).resolve().parent.parent
out=pathlib.Path(tempfile.mkdtemp(prefix='forge-dashboard-plans-'))
print('Execution plans:',out,flush=True)
database_name='forge_dashboard_bench_'+str(time.time_ns())
def sql(q,db=database_name):
    p=subprocess.run(['docker','exec','-i','forge-test-db','psql','-X','-qAt','-U','postgres','-v','ON_ERROR_STOP=1','-d',db],input=q,text=True,capture_output=True)
    if p.returncode:
        raise RuntimeError(p.stderr)
    return p.stdout
sql('CREATE DATABASE '+database_name,'postgres')
try:
    sql((root/'internal/forge/dbschema/schema.sql').read_text())
    sql("""
    INSERT INTO projects(slug,name,created_at) SELECT 'project-'||n,'Project '||n,timestamptz '2026-10-09 00:00:00+00' FROM generate_series(1,1000) n;
    INSERT INTO changes(project_id,type,title,state,created_at) SELECT p.id,'feature','Change '||n,'shipped',timestamptz '2026-10-09 00:00:00+00' FROM projects p CROSS JOIN generate_series(1,100) n;
    INSERT INTO runs(project_id,workflow_name,workflow_version,kind,status,current_stage,definition_json,change_id,created_at,updated_at)
    SELECT project_id,'implement',1,'change',CASE WHEN id<=1000 THEN 'running' ELSE 'done' END,'work','{}',id,timestamptz '2026-10-09 00:00:00+00'-id*interval '1 minute',timestamptz '2026-10-09 00:00:00+00'-id*interval '1 minute' FROM changes;
    UPDATE changes c SET run_id=r.id FROM runs r WHERE r.change_id=c.id;
    INSERT INTO steps(run_id,stage,name,type,status) SELECT r.id,'work','step-'||n,CASE WHEN n=5 THEN 'approval' ELSE 'agent' END,
    CASE WHEN r.status='running' AND r.id%2=0 AND n=5 THEN 'pending' ELSE 'pass' END FROM runs r CROSS JOIN generate_series(1,5) n;
    INSERT INTO adrs(project_id,number,title,created_at,updated_at) SELECT p.id,n,'ADR '||n,timestamptz '2026-10-09 00:00:00+00'-n*interval '1 hour',timestamptz '2026-10-09 00:00:00+00'-n*interval '1 hour' FROM projects p CROSS JOIN generate_series(1,20) n;
    ANALYZE;
    """)
    src=''.join(p.read_text() for p in sorted((root/'internal/forge/workflow/internal/workflowsql').glob('*.sql')))
    vals={'running_status':"'running'",'done_status':"'done'",'proposed_state':"'proposed'",'in_progress_state':"'in_progress'",'landing_state':"'landing'",'running':"'running'",'pending':"'pending'",'needs_human':"'needs_human'",'row_limit':'6'}
    for name in ['ProjectActivityReport','DashboardActiveRuns','RecentProjectIDs']:
        query=src.split('-- name: '+name+' :many\n')[1].split(';')[0]
        params=list(dict.fromkeys(re.findall(r'sqlc.arg\((\w+)\)',query)))
        query=re.sub(r'sqlc.arg\((\w+)\)',lambda m:'$'+str(params.index(m[1])+1),query)
        for size in [1,1000]:
            ids='ARRAY['+','.join(map(str,range(1,size+1)))+']'
            values=[ids if p in ['project_ids','run_project_ids'] else vals[p] for p in params]
            for mode in ['force_custom_plan','force_generic_plan']:
                # PREPARE/EXECUTE exercise the two PostgreSQL plan modes, on the final SQL.
                output=sql('SET plan_cache_mode='+mode+'; PREPARE bench AS '+query+';'+('EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) EXECUTE bench('+','.join(values)+');')*3)
                (out/f'{name}-{size}-{mode}.json').write_text(output)
                decoder=json.JSONDecoder()
                data=output.strip()
                times=[]
                while data:
                    obj,n=decoder.raw_decode(data)
                    times.append(obj[0]['Execution Time'])
                    data=data[n:].strip()
                print(name,size,mode,round(statistics.median(times),3),'ms',flush=True)
finally:
    sql('DROP DATABASE '+database_name,'postgres')
