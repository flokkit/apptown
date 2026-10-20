#!/usr/bin/env python3
"""Send the companies set (eval/companies) through a forge.

    python3 eval/companies/run.py --forge URL --token-file F --state FILE [--company NAME|all] <command>

Commands:
  create [--wave N] [--limit N] [--app APP] [--target T] [--accept-capacity-risk WHY]
                      make each app's project with the pack stacks.yaml names
                      (the forge's picker is asked too and its suggestion only
                      recorded) and open its 00-create request as a build
  drive [--target T] [--accept-capacity-risk WHY] [--on-behalf-of NAME]
                      follow every open change: approve its questions under the
                      delegation below, land it when its work is done, record
                      how it ends
  next                open each app's next request, one per app, in order
  report              a table per company
  reconcile APP REQUEST CHANGE|none
                      record what a person found for an opening whose answer
                      was lost (APP is company/app, REQUEST like 03-foo)

It acts only through the product: the forge CLI with --json, and the reads the
CLI has no command for (the pack picker, a target's capacity, a project's
deployments). Waiting is `apptown wait`. State lives outside the repository:
--state names its file, or the FORGE_EVAL_STATE environment variable names a
directory that holds one file per forge (<forge host>.json); with neither the
runner refuses. It is written before every mutation and after its answer, so a
rerun never opens the same request twice. A mutation whose answer was lost
stays unknown until `reconcile`.

The delegation: `drive` approves a change's questions on behalf of the person
who runs it, and says so in each approval's note. --on-behalf-of names that
person; by default it is the user the forge says the token acts as.
"""
import argparse
import json
import os
import re
import subprocess
import sys
import threading
import urllib.parse
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
SHORT = {
    "harbourline-freight": "harbourline",
    "marrow-finch-vets": "marrowfinch",
    "coldwater-shire": "coldwater",
    "bramble-street-cu": "bramble",
    "tidewell-grammar": "tidewell",
}


def approval_note(on_behalf_of):
    """The note every approval the runner sends carries: whom it acts for."""
    if on_behalf_of:
        return f"Approved on behalf of {on_behalf_of}, delegated to the eval/companies runner"
    return "Approved by the eval/companies runner"


MIB = 1 << 20
ESTIMATE = 256 * MIB   # what one small app uses running: an estimate, not its limits
RESERVE = 0.15         # keep this share of the host free
FINISHED = {"shipped", "closed", "stopped"}


# ---------------------------------------------------------------- the dataset

def parse_request(path):
    """A request file: its header fields (after: may repeat) and its text."""
    text = Path(path).read_text()
    m = re.match(r"---\n(.*?)\n---\n\n?(.*)", text, re.S)
    if not m:
        raise ValueError(f"{path}: no header")
    head, after = {}, []
    for line in m.group(1).splitlines():
        if ": " not in line:
            continue
        k, v = line.split(": ", 1)
        if k == "after":
            after.append(v.strip())
        else:
            head[k] = v.strip()
    return {"id": Path(path).stem, "number": int(Path(path).name[:2]),
            "slug": Path(path).stem[3:], "from": head.get("from", ""),
            "size": head.get("size", ""), "kind": head.get("kind", ""),
            "after": after, "body": m.group(2).strip()}


def load_companies(root=HERE, only="all"):
    """company → app → [requests in number order]."""
    out = {}
    for co in sorted(p.name for p in Path(root).iterdir() if (p / "COMPANY.md").exists()):
        if only not in ("all", co):
            continue
        apps = {}
        for app in sorted(d.name for d in (Path(root) / co).iterdir() if d.is_dir()):
            reqs = [parse_request(f) for f in sorted((Path(root) / co / app).glob("[0-9][0-9]-*.md"))]
            if reqs:
                apps[app] = reqs
        out[co] = apps
    return out


def load_stacks(path=HERE / "stacks.yaml"):
    """stacks.yaml: `waves:` (company per line) and `apps:` company → app →
    { pack: P, why: W }. A small strict reader for this file's one shape (the
    standard library has no YAML); anything else in it is an error."""
    waves, apps, section, co = [], {}, None, None
    for n, line in enumerate(Path(path).read_text().splitlines(), 1):
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if line in ("waves:", "apps:"):
            section = line[:-1]
            continue
        m = re.fullmatch(r"  - ([a-z0-9-]+)", line)
        if section == "waves" and m:
            waves.append(m.group(1))
            continue
        m = re.fullmatch(r"  ([a-z0-9-]+):", line)
        if section == "apps" and m:
            co = m.group(1)
            apps[co] = {}
            continue
        m = re.fullmatch(r"    ([a-z0-9-]+): \{ pack: ([a-z0-9-]+), why: (.+) \}", line)
        if section == "apps" and co and m:
            apps[co][m.group(1)] = {"pack": m.group(2), "why": m.group(3)}
            continue
        raise ValueError(f"{path}:{n}: not understood: {line!r}")
    return {"waves": waves, "apps": apps}


def stock_packs(path=HERE.parent.parent / "stock" / "packs.yaml"):
    return set(re.findall(r"(?m)^  - name: ([a-z0-9-]+)$", Path(path).read_text()))


def slug_for(company, app):
    return f"{SHORT.get(company, company)}-{app}"


def message(req):
    """The request text, then who asked. Last, because the forge titles a
    change from its first line."""
    who = f"\n\nAsked by {req['from']}." if req["from"] else ""
    return req["body"] + who


def workflow_for(req):
    if req["kind"] == "create":
        return "build"
    if req["kind"] == "fix":
        return "fix"
    if req["size"] == "short":
        return "quick"
    return "plan"


# ---------------------------------------------------------------- state

class State:
    def __init__(self, path):
        self.path = Path(path)
        self.lock = threading.Lock()
        self.data = json.loads(self.path.read_text()) if self.path.exists() else {"apps": {}, "risks": []}

    def app(self, key):
        return self.data["apps"].setdefault(key, {"requests": {}})

    def save(self):
        with self.lock:
            self.path.parent.mkdir(parents=True, exist_ok=True)
            tmp = self.path.with_suffix(".tmp")
            tmp.write_text(json.dumps(self.data, indent=1, sort_keys=True))
            os.replace(tmp, self.path)


def request_state(st, key, rid):
    return st.app(key)["requests"].get(rid)


def finished(r):
    return bool(r) and r.get("outcome") in FINISHED


# ---------------------------------------------------------------- ordering

def next_requests(companies, st):
    """What `next` may open now: [(key, request)]. Rules (README, plan):
    no 01 before every create in the company has finished; one open request
    per app; an `after:` waits for what it names; a cross-app slug goes to all
    its holders together, once each is ready for it."""
    out = []
    for co, apps in companies.items():
        creates_done = all(finished(request_state(st, f"{co}/{a}", reqs[0]["id"])) for a, reqs in apps.items())
        if not creates_done:
            continue
        ready = {}
        for app, reqs in apps.items():
            key = f"{co}/{app}"
            pending = [r for r in reqs if request_state(st, key, r["id"]) is None]
            opened = [r for r in reqs if request_state(st, key, r["id"]) is not None]
            if not pending or any(not finished(request_state(st, key, r["id"])) for r in opened):
                continue
            nxt = pending[0]
            if all(finished(request_state(st, f"{co}/{d.split('/')[0]}", _find(apps, d))) for d in nxt["after"]):
                ready[app] = nxt
        taken = set()
        for app, req in ready.items():
            if app in taken:
                continue
            if req["kind"] != "cross-app":
                out.append((f"{co}/{app}", req))
                taken.add(app)
                continue
            holders = [a for a, rs in apps.items() if any(r["slug"] == req["slug"] for r in rs)]
            if all(a in ready and ready[a]["slug"] == req["slug"] for a in holders):
                for a in holders:
                    out.append((f"{co}/{a}", ready[a]))
                    taken.add(a)
    return out


def _find(apps, dep):
    app, num = dep.split("/")
    for r in apps.get(app, []):
        if r["number"] == int(num):
            return r["id"]
    raise ValueError(f"after: {dep} names no request")


# ---------------------------------------------------------------- capacity

def room_after(cap, pending_apps, admitting=1):
    """Free memory after the apps the runner has opened but the host does not
    show yet, plus `admitting` more (1 for a new app; 0 when landing an app
    already counted among the pending); None when it cannot be told."""
    mem = (cap or {}).get("memory") or {}
    total = (mem.get("total") or {})
    if not total.get("known"):
        return None
    host = mem.get("hostUsed") or {}
    if host.get("known"):
        used = host["value"]
    else:
        parts = [(mem.get("inUse") or {}).get(k) or {} for k in ("forge", "other")]
        if not all(p.get("known") for p in parts):
            return None
        used = sum(p["value"] for p in parts)
    return total["value"], total["value"] - used - ESTIMATE * (pending_apps + admitting)


def capacity_ok(api, st, target, risk, admitting=1):
    """(ok, sentence) for admitting `admitting` more apps on target (0 when
    landing an app the pending count already includes)."""
    if not target:
        return True, "no target named: capacity not checked"
    cap = api.get(f"/api/v1/targets/{urllib.parse.quote(target)}/capacity")
    if cap.get("elastic"):
        return True, f"{target} grows on demand: capacity not checked"
    pending = sum(1 for a in st.data["apps"].values() if a.get("pending"))
    r = room_after(cap, pending, admitting)
    if r is None:
        why = f"{target}: in-use memory is unknown, so free memory cannot be told"
    else:
        total, room = r
        if room >= RESERVE * total:
            return True, f"{target}: {room // MIB} MiB would stay free of {total // MIB} MiB"
        why = (f"{target}: only {max(room, 0) // MIB} MiB would stay free of {total // MIB} MiB "
               f"counting {pending + admitting} app(s) not yet running (estimate {ESTIMATE // MIB} MiB each)")
    if risk:
        st.data["risks"].append({"target": target, "why": risk, "refused": why})
        return True, why + f"; proceeding: {risk}"
    return False, why


# ---------------------------------------------------------------- the forge

class Forge:
    def __init__(self, url, token, cli):
        self.url, self.token, self.cli = url.rstrip("/"), token, cli
        self.env = {**os.environ, "FORGE_URL": self.url, "FORGE_TOKEN": token}

    def run(self, *args):
        """(exit code, parsed JSON or None, stderr)."""
        p = subprocess.run([self.cli, *args], env=self.env, capture_output=True, text=True, stdin=subprocess.DEVNULL)
        try:
            doc = json.loads(p.stdout) if p.stdout.strip() else None
        except json.JSONDecodeError:
            doc = None
        return p.returncode, doc, (p.stderr.strip() or p.stdout.strip())[-600:]

    def text(self, *args):
        p = subprocess.run([self.cli, *args], env=self.env, capture_output=True, text=True, stdin=subprocess.DEVNULL)
        return (p.stdout + p.stderr).strip()

    def call(self, method, path, body=None):
        req = urllib.request.Request(self.url + path, method=method,
                                     data=json.dumps(body).encode() if body is not None else None,
                                     headers={"Authorization": "Bearer " + self.token, "Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=120) as r:
            return json.loads(r.read() or b"null")

    def get(self, path):
        return self.call("GET", path)


def project_exists(forge, slug):
    """The forge's project list (the CLI's `projects` has no --json)."""
    rows = forge.get("/api/v1/projects") or []
    rows = rows.get("projects", rows) if isinstance(rows, dict) else rows
    return any(r.get("slug") == slug for r in rows)


def observe_main(forge, slug):
    """("running", url) | ("not running", None) | ("unknown", why). A failed
    read or a null `running` is unknown, never "not deployed"."""
    try:
        rows = forge.get(f"/api/v1/projects/{slug}/deployments") or []
    except Exception as e:
        return "unknown", f"the deployments read failed: {e}"
    for d in rows:
        if d.get("name") == "main":
            if d.get("running") is True:
                return "running", d.get("url")
            if d.get("running") is False:
                return "not running", None
            return "unknown", d.get("observationIssue") or "the forge has no observation of it"
    return "not running", None


def settle_create(forge, st, key, slug):
    """Record a shipped create's deployment; it stops counting against the
    host only once the forge observes it running."""
    a = st.app(key)
    seen, x = observe_main(forge, slug)
    a["observed"] = seen
    a["deployed"] = x if seen == "running" else None
    if seen == "unknown":
        a["observationIssue"] = x
    if seen == "running":
        a["pending"] = False
    st.save()


# ---------------------------------------------------------------- commands

def cmd_create(forge, st, companies, args, stacks=None):
    stacks = stacks if stacks is not None else load_stacks()
    made = 0
    for co, apps in companies.items():
        for app, reqs in apps.items():
            if args.limit is not None and made >= args.limit:
                return
            if getattr(args, "app", None) and app != args.app:
                continue
            key, slug = f"{co}/{app}", slug_for(co, app)
            a = st.app(key)
            a["slug"] = slug
            if a.get("project") in (None, "creating") and project_exists(forge, slug):
                # A lost `new`, or a lost state file: slugs are unique, so the
                # project on the forge is this app's.
                a["project"] = "adopted"
                st.save()
            if a.get("project") in (None, "creating"):
                chosen = stacks["apps"].get(co, {}).get(app)
                if not chosen:
                    print(f"{key}: no entry in stacks.yaml: not created (the stack is chosen there, not by the picker)")
                    continue
                ok, why = capacity_ok(forge, st, args.target, args.accept_capacity_risk)
                print(f"{key}: capacity: {why}")
                if not ok:
                    st.save()
                    print("create stopped: not enough room on the host")
                    return
                create = reqs[0]
                # The picker is measured, not used: its suggestion is recorded
                # beside the pack stacks.yaml chose.
                try:
                    pick = forge.call("POST", "/api/v1/packs/pick", {"description": create["body"]})
                except Exception as e:
                    pick = {"pack": None, "why": f"the picker did not answer: {e}"}
                a.update({"pack": chosen["pack"], "packWhy": chosen["why"],
                          "suggested": pick.get("pack"), "suggestedWhy": pick.get("why"), "project": "creating"})
                st.save()
                code, doc, err = forge.run("new", slug, "--pack", chosen["pack"], "--json")
                if code == 0 and doc:
                    a["project"] = "created"
                elif code == 4:
                    print(f"{key}: the forge's answer to new was lost; the next run looks the slug up")
                    st.save()
                    continue
                else:
                    a["project"] = None
                    a["projectError"] = err
                    st.save()
                    print(f"{key}: new refused: {err}")
                    continue
                st.save()
            if open_request(forge, st, key, slug, reqs[0]):
                made += 1


def open_request(forge, st, key, slug, req):
    """Open one request as a change; True when it opened. Intent is saved
    first; a lost answer leaves it unknown and never reopens it."""
    a = st.app(key)
    if req["id"] in a["requests"]:
        return False
    wf = workflow_for(req)
    r = a["requests"][req["id"]] = {"intent": "opening", "workflow": wf, "kind": req["kind"], "size": req["size"]}
    if req["kind"] == "create":
        a["pending"] = True
    st.save()
    code, doc, err = forge.run(wf, "-p", slug, "--json", message(req))
    if code == 0 and doc and doc.get("change"):
        r.update({"intent": "opened", "change": doc["change"]["id"], "name": doc["change"].get("name")})
        print(f"{key}: opened {req['id']} as {r['name']} ({wf})")
    elif code == 4:
        r.update({"intent": "unknown", "detail": "sent, answer lost: " + err})
        print(f"{key}: {req['id']}: answer lost; reconcile before it advances")
    else:
        r.update({"intent": "refused", "outcome": "stopped", "detail": err})
        if req["kind"] == "create":
            a["pending"] = False
        print(f"{key}: {req['id']} refused: {err}")
    st.save()
    return code == 0


def drive_one(forge, st, key, slug, rid, args, limit=40):
    a = st.app(key)
    r = a["requests"][rid]
    for _ in range(limit):
        code, doc, err = forge.run("wait", "-c", r["change"], "--json")
        outcome = (doc or {}).get("outcome")
        if doc is None:
            r["detail"] = f"wait gave no answer (exit {code}): {err}"
            st.save()
            return
        r["detail"] = doc.get("detail", "")
        if outcome == "needs_decision" and doc.get("step"):
            step = doc["step"]
            earlier = [x for x in r.get("approvals", []) if x["step"] == step]
            if earlier and earlier[-1].get("answer") != "approved":
                # Sent before and its answer lost (or the runner stopped
                # between sending and saving): never vote again on the same
                # question. A person looks, then approves by hand or reconciles.
                r["held"] = f"the approval of step {step} was sent and its answer lost; it still asks"
                st.save()
                return
            meaning = forge.text("approve", step)   # no terminal: prints what approving means, approves nothing
            vote = {"step": step, "meaning": meaning[-1500:], "answer": None}
            r.setdefault("approvals", []).append(vote)
            st.save()
            note = getattr(args, "approval_note", None) or approval_note("")
            c2, d2, e2 = forge.run("approve", step, "-y", "-m", note, "--json")
            if c2 == 0:
                vote["answer"] = "approved"
                r.pop("held", None)
            elif c2 == 4:
                vote["answer"] = "lost"
                r["held"] = f"the approval of step {step} was sent and its answer lost"
                st.save()
                return
            else:
                vote["answer"] = "refused"
                r.update({"outcome": "stopped", "detail": f"approval refused: {e2}"})
                st.save()
                return
            continue
        r.pop("held", None)   # the run moved on: a lost approval answer was settled by the forge
        if outcome in ("done", "shipped", "ended"):
            c3, show, e3 = forge.run("show", r["change"], "--json")
            state = (show or {}).get("state")
            if state == "shipped":
                r["outcome"] = "shipped"
                break
            if state == "abandoned":
                r.update({"outcome": "closed", "closedAs": (show or {}).get("closedAs")})
                break
            if state == "landing" or outcome == "needs_decision":
                continue
            if r["kind"] == "create":
                ok, why = capacity_ok(forge, st, args.target, args.accept_capacity_risk, admitting=0)
                if not ok:
                    r["detail"] = "held before landing: " + why
                    st.save()
                    return
            r.setdefault("lands", 0)
            r["lands"] += 1
            st.save()
            c4, d4, e4 = forge.run("land", r["change"], "--json")
            if c4 == 4:
                r["detail"] = "land sent, answer lost: " + e4   # wait finds out what happened
            elif c4 != 0:
                r.update({"outcome": "stopped", "detail": f"land refused: {e4}"})
                break
            continue
        if outcome in ("blocked", "failed", "abandoned"):
            r.update({"outcome": "stopped", "stoppedAs": outcome})
            break
        st.save()   # unknown, or needs_decision without a step: leave it for the next drive
        return
    if r["kind"] == "create" and r.get("outcome") == "shipped":
        settle_create(forge, st, key, slug)
    elif r["kind"] == "create" and r.get("outcome") in ("closed", "stopped"):
        a["pending"] = False   # nothing will be deployed for it
    st.save()


def cli_user(forge):
    """Who the forge says the token acts as, or "" when it does not say."""
    try:
        return (forge.get("/api/v1/auth/me") or {}).get("user", "")
    except Exception:
        return ""


def cmd_drive(forge, st, companies, args):
    who = args.on_behalf_of if getattr(args, "on_behalf_of", None) is not None else cli_user(forge)
    args.approval_note = approval_note(who)
    jobs = []
    for co, apps in companies.items():
        for app in apps:
            key = f"{co}/{app}"
            a = st.app(key)
            for rid, r in a["requests"].items():
                if r.get("intent") == "opened" and not finished(r):
                    jobs.append(threading.Thread(target=drive_one, args=(forge, st, key, a["slug"], rid, args)))
                elif rid.startswith("00-") and r.get("outcome") == "shipped" and a.get("observed") != "running":
                    settle_create(forge, st, key, a["slug"])   # look again at an unsettled deployment
    for j in jobs:
        j.start()
    for j in jobs:
        j.join()
    print(f"drove {len(jobs)} change(s)")


def cmd_next(forge, st, companies, args):
    todo = next_requests(companies, st)
    for key, req in todo:
        open_request(forge, st, key, st.app(key)["slug"], req)
    if not todo:
        print("nothing ready: creates still running, open requests unfinished, or all applied")


def cmd_reconcile(st, args):
    a = st.app(args.app)
    r = a["requests"].get(args.request)
    if not r or r.get("intent") not in ("unknown", "opening"):
        # "opening" persisted means the runner stopped between sending and
        # saving the answer: as unknown as a lost answer.
        sys.exit(f"{args.app} {args.request}: no unresolved opening to reconcile")
    if args.change == "none":
        del a["requests"][args.request]   # not on the forge: it may be opened again
        if args.request.startswith("00-"):
            a["pending"] = False
    else:
        r.update({"intent": "opened", "change": args.change, "detail": "reconciled by a person"})
    st.save()


def cmd_report(st, companies):
    for co, apps in companies.items():
        print(f"\n{co}")
        print(f"{'app':<22} {'pack':<16} {'picker said':<17} {'create':<10} {'deployed':<9} {'applied':>7}  refused/questioned")
        for app, reqs in apps.items():
            a = st.data["apps"].get(f"{co}/{app}", {"requests": {}})
            rs = a["requests"]
            create = rs.get(reqs[0]["id"], {})
            later = [rid for rid in rs if not rid.startswith("00-")]
            odd = [rid for rid in later if rs[rid].get("outcome") in ("closed", "stopped")]
            unresolved = [rid for rid in rs if rs[rid].get("intent") in ("unknown", "opening") or rs[rid].get("held")]
            deployed = {"running": "yes", "not running": "no", "unknown": "unknown"}.get(a.get("observed"), "no")
            print(f"{app:<22} {str(a.get('pack') or '-'):<16} {str(a.get('suggested') or '-'):<17} "
                  f"{str(create.get('outcome') or create.get('intent') or '-'):<10} "
                  f"{deployed:<9} {len(later):>7}  {', '.join(odd) or '-'}"
                  + (f"   NEEDS A PERSON: {', '.join(unresolved)}" if unresolved else ""))
    if st.data.get("risks"):
        print("\ncapacity risks accepted:")
        for x in st.data["risks"]:
            print(f"  {x['target']}: {x['why']} (the guard said: {x['refused']})")
    print(f"\nestimate used by the capacity guard: {ESTIMATE // MIB} MiB per app (use, not limits)")


def state_path(state, state_dir, host):
    """--state, else FORGE_EVAL_STATE/<host>.json; no default: the runner
    must not write its record somewhere the operator did not choose."""
    if state:
        return Path(state)
    if state_dir:
        return Path(state_dir) / f"{host}.json"
    sys.exit("no state file: pass --state FILE or set FORGE_EVAL_STATE to a directory")


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    p.add_argument("--forge", required=True)
    p.add_argument("--token-file", required=True)
    p.add_argument("--cli", default=os.environ.get("FORGE_CLI", "forge"))
    p.add_argument("--company", default="all")
    p.add_argument("--state", help="the state file (or set FORGE_EVAL_STATE to a directory)")
    sub = p.add_subparsers(dest="cmd", required=True)
    for name in ("create", "drive"):
        s = sub.add_parser(name)
        s.add_argument("--target")
        s.add_argument("--accept-capacity-risk")
        if name == "drive":
            s.add_argument("--on-behalf-of", help="whom the approvals are made for (default: the user the token acts as)")
        if name == "create":
            s.add_argument("--limit", type=int)
            s.add_argument("--app", help="create only this app (its directory name)")
            s.add_argument("--wave", type=int, help="create the company of this wave in stacks.yaml (1 = first)")
    sub.add_parser("next")
    sub.add_parser("report")
    r = sub.add_parser("reconcile")
    r.add_argument("app")
    r.add_argument("request")
    r.add_argument("change")
    args = p.parse_args(argv)
    host = urllib.parse.urlparse(args.forge).hostname
    st = State(state_path(args.state, os.environ.get("FORGE_EVAL_STATE"), host))
    only = args.company
    if getattr(args, "wave", None):
        waves = load_stacks()["waves"]
        if not 1 <= args.wave <= len(waves):
            sys.exit(f"--wave {args.wave}: stacks.yaml has {len(waves)} waves")
        only = waves[args.wave - 1]
    companies = load_companies(only=only)
    if args.cmd == "report":
        return cmd_report(st, companies)
    if args.cmd == "reconcile":
        return cmd_reconcile(st, args)
    forge = Forge(args.forge, Path(args.token_file).read_text().strip(), args.cli)
    {"create": cmd_create, "drive": cmd_drive, "next": cmd_next}[args.cmd](forge, st, companies, args)


if __name__ == "__main__":
    main()
