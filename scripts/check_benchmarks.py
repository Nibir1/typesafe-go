#!/usr/bin/env python3
"""Compare two `go test -bench` runs and fail on a regression.

    check_benchmarks.py old.txt new.txt [--threshold 10]

A committed baseline would be worthless: benchmark numbers are specific to the
machine that produced them, and a figure from a developer's laptop says nothing
about a CI runner. So both sides are measured on the same machine in the same
job, and only the ratio between them is used.

Each benchmark may appear several times (`-count=N`). The median is taken, not
the mean, because one slow run from a noisy neighbour should not move the
comparison.

A benchmark missing from the new run is reported: a deleted benchmark is
usually deliberate and sometimes an accident, and either way it should not pass
silently.
"""

import argparse
import re
import statistics
import sys

# BenchmarkName-8   1000000   123.4 ns/op   56 B/op   7 allocs/op
LINE = re.compile(
    r"^(?P<name>Benchmark[^\s]*?)(?:-\d+)?\s+"
    r"(?P<iters>\d+)\s+"
    r"(?P<ns>[\d.]+)\s+ns/op"
)

OK = "\033[32m"
ERR = "\033[31m"
WARN = "\033[33m"
DIM = "\033[2m"
OFF = "\033[0m"


def parse(path: str) -> dict[str, float]:
    """Median ns/op per benchmark."""
    samples: dict[str, list[float]] = {}
    with open(path) as f:
        for line in f:
            m = LINE.match(line.strip())
            if m:
                samples.setdefault(m.group("name"), []).append(float(m.group("ns")))
    return {name: statistics.median(vals) for name, vals in samples.items()}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("old")
    ap.add_argument("new")
    ap.add_argument(
        "--threshold",
        type=float,
        default=10.0,
        help="percent regression that fails the build (default 10)",
    )
    args = ap.parse_args()

    old = parse(args.old)
    new = parse(args.new)

    if not old or not new:
        print(f"  {ERR}✗ no benchmark results parsed "
              f"({len(old)} old, {len(new)} new){OFF}")
        return 1

    regressions: list[str] = []
    improvements: list[str] = []
    missing = sorted(set(old) - set(new))

    for name in sorted(new):
        if name not in old:
            continue  # a new benchmark has nothing to compare against
        before, after = old[name], new[name]
        if before <= 0:
            continue
        delta = (after - before) / before * 100
        line = f"{name}: {before:,.0f} -> {after:,.0f} ns/op ({delta:+.1f}%)"
        if delta > args.threshold:
            regressions.append(line)
        elif delta < -args.threshold:
            improvements.append(line)

    for line in improvements:
        print(f"  {OK}faster{OFF}  {line}")
    for name in missing:
        print(f"  {WARN}!{OFF} {name} is in the baseline but not in this run")

    if regressions:
        print(f"\n  {ERR}✗ {len(regressions)} benchmark(s) regressed by more than "
              f"{args.threshold:g}%:{OFF}")
        for line in regressions:
            print(f"    {ERR}{line}{OFF}")
        print(f"\n  {DIM}If the regression is intended, say so in the commit message "
              f"and raise the threshold for this run.{OFF}")
        return 1

    compared = len(set(old) & set(new))
    print(f"  {OK}✓ {compared} benchmark(s) compared, none regressed by more than "
          f"{args.threshold:g}%{OFF}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
