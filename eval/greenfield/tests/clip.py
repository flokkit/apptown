import sys
sys.path.insert(0, sys.path[0] + "/..")
from kit import check, finish, run_bin

STAGE = int(sys.argv[1]) if len(sys.argv) > 1 else 1

CSV = """name,category,amount
coffee,fun,4.5
rent,housing,1800
"beans, dried",groceries,7
book,fun,20
salary,income,-3200
"quote""d",other,1
"""


def stage1():
    ec, out, err, sym = run_bin(["select", "name,amount"], CSV)
    check("select keeps order and quoting",
          ec == 0 and out == 'name,amount\ncoffee,4.5\nrent,1800\n"beans, dried",7\nbook,20\nsalary,-3200\n"quote""d",1\n', sym)
    ec, out, err, sym = run_bin(["select", "amount,name"], CSV)
    check("select reorders columns", ec == 0 and out.splitlines()[0] == "amount,name"
          and out.splitlines()[1] == "4.5,coffee", sym)
    ec, out, err, sym = run_bin(["select", "nope"], CSV)
    check("unknown column exit 2 + stderr", ec == 2 and out == "" and "no such column: nope" in err, sym)

    ec, out, err, sym = run_bin(["filter", "category = fun"], CSV)
    check("filter equality", ec == 0 and out == "name,category,amount\ncoffee,fun,4.5\nbook,fun,20\n", sym)
    ec, out, err, sym = run_bin(["filter", "amount > 10"], CSV)
    check("filter numeric compare", ec == 0 and [l.split(",")[0] for l in out.splitlines()[1:]] == ["rent", "book"], sym)
    ec, out, err, sym = run_bin(["filter", "name > coffee"], CSV)
    names = [l.split(",")[0] for l in out.splitlines()[1:]]
    check("filter string compare", ec == 0 and "rent" in names and "book" not in names, sym)

    ec, out, err, sym = run_bin(["head", "2"], CSV)
    check("head keeps header + 2", ec == 0 and len(out.splitlines()) == 3, sym)
    ec, out, err, sym = run_bin(["head", "0"], CSV)
    check("head 0 = header only", ec == 0 and out == "name,category,amount\n", sym)
    ec, out, err, sym = run_bin(["head", "5"], "a,b\n")
    check("empty input ok", ec == 0 and out == "a,b\n", sym)
    ec, out, err, sym = run_bin(["select", "name"], 'x,name\n1,"line\nbreak"\n')
    check("multiline quoted field", ec == 0 and out == 'name\n"line\nbreak"\n', sym)


def stage2():
    ec, out, err, sym = run_bin(["sort", "name"], CSV)
    got = [l.split(",")[0] for l in out.splitlines()[1:]]
    check("sort string asc", ec == 0 and got[0] == '"beans, dried"' and got[-1] == "salary", sym)
    ec, out, err, sym = run_bin(["sort", "amount", "--num", "--desc"], CSV)
    first = out.splitlines()[1].split(",")[0]
    check("sort numeric desc", ec == 0 and first == "rent", sym)
    mixed = "v\n3\nx\n1\ny\n2\n"
    ec, out, err, sym = run_bin(["sort", "v", "--num"], mixed)
    check("non-numeric rows sort after, stable",
          ec == 0 and out == "v\n1\n2\n3\nx\ny\n", sym)

    ec, out, err, sym = run_bin(["stats", "amount"], CSV + "bad,fun,oops\n")
    ok = (ec == 0 and "count: 7 (1 non-numeric skipped)" in out
          and "sum: -1367.5" in out and "min: -3200" in out and "max: 1800" in out)
    check("stats with skip note", ok, sym)
    ec, out, err, sym = run_bin(["stats", "amount"], "amount\n1\n2\n")
    check("mean trims zeros", ec == 0 and "mean: 1.5" in out and "1.5000" not in out, sym)

    ec, out, err, sym = run_bin(["group", "category", "sum(amount)"], CSV)
    lines = out.splitlines()
    check("group sum, first-appearance order",
          ec == 0 and lines[0] == "category,sum(amount)" and lines[1].startswith("fun,24.5")
          and lines[2].startswith("housing,1800"), sym)
    ec, out, err, sym = run_bin(["group", "category", "count(name)"], CSV)
    check("group count", ec == 0 and "fun,2" in out, sym)


def stage3():
    left = "id,who\n1,ann\n2,bob\n3,cee\n"
    right = "id,role,who\n2,dev,X\n1,ops,Y\n2,qa,Z\n"
    import tempfile, os
    rf = tempfile.NamedTemporaryFile("w", suffix=".csv", delete=False)
    rf.write(right); rf.close()
    ec, out, err, sym = run_bin(["join", rf.name, "--on", "id"], left)
    ok = (ec == 0 and out.splitlines()[0] == "id,who,role,r_who"
          and out.splitlines()[1] == "1,ann,ops,Y"
          and out.splitlines()[2] == "2,bob,dev,X"
          and out.splitlines()[3] == "2,bob,qa,Z"
          and len(out.splitlines()) == 4)
    check("inner join: left order, right order, prefix collision", ok, sym)

    sf = tempfile.NamedTemporaryFile("w", suffix=".clip", delete=False)
    sf.write("# pipeline\nfilter amount > 5\n\nselect name,amount\nhead 2\n"); sf.close()
    ec, out, err, sym = run_bin(["run", sf.name], CSV)
    check("script pipeline", ec == 0 and out == "name,amount\nrent,1800\n\"beans, dried\",7\n", sym)
    os.unlink(rf.name); os.unlink(sf.name)


stage1()
if STAGE >= 2:
    stage2()
if STAGE >= 3:
    stage3()
finish()
