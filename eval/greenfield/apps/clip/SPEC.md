# clip — a small CSV toolkit for the terminal

I live in CSVs and want one sharp tool instead of five awk scripts. A
single binary named `clip`, reads a CSV on stdin OR from a file argument,
writes CSV (or text) to stdout. Errors to stderr, nonzero exit.

CSV dialect: RFC 4180-ish. First row is the header. Fields may be quoted
with double quotes; quoted fields may contain commas, newlines, and
doubled quotes. Output must re-quote only when needed (field contains
comma, quote, or newline). Preserve input column order unless told
otherwise. A trailing newline at end of output.

## Stage 1 — select, filter, head

    clip select COL1,COL2 [file]       keep only these columns, in the order given
    clip filter 'COL OP VALUE' [file]  keep rows matching
    clip head N [file]                 first N data rows (plus header)

Filter operators: = != > < >= <=. If both the cell and the value parse as
numbers, compare numerically; otherwise compare as strings (byte order).
Quoting: the expression is one shell argument.

Unknown column → error "no such column: NAME", exit 2, nothing on stdout.
Empty input (header only) is fine: output is header only.

## Stage 2 — aggregate and sort

    clip sort COL [--desc] [--num] [file]
    clip stats COL [file]              → text: count, sum, min, max, mean
    clip group BYCOL AGG(COL) [file]   e.g. clip group category sum(amount)

- sort is STABLE. --num parses numbers (rows that don't parse sort after
  all rows that do, keeping their relative order).
- stats: non-numeric cells are skipped for sum/min/max/mean but counted
  in "count" as skipped, like:
      count: 42 (3 non-numeric skipped)
      sum: 1234.5
      min: -2
      max: 999
      mean: 30.8625
  mean = sum/numeric_count, printed with up to 4 decimals, no trailing
  zeros (30.5 not 30.5000).
- group AGG is one of sum, count, mean, min, max. Output CSV with header
  BYCOL,AGG(COL) — e.g. `category,sum(amount)` — groups in order of
  first appearance.

## Stage 3 — join and pipeline files

    clip join other.csv --on COL [file]   inner join on equal COL values

Left row order preserved; for each left row, all matching right rows in
their file order; right's join column not repeated; right's other columns
appended after left's, with `r_` prefix on any name collision.

    clip run script.clip [file]

where script.clip is one command per line (same syntax as the CLI, minus
the leading "clip"), applied top to bottom — the output of each is the
input of the next. Blank lines and lines starting with # are skipped.
