#!/usr/bin/env python3
"""Validate the committed Grafana dashboards.

A dashboard that ships broken is worse than no dashboard: it looks like
monitoring right up until someone relies on it. This checks the three ways one
silently stops working.

  1. It is valid JSON with the fields Grafana requires.
  2. Every PromQL expression references a metric this SDK actually exports,
     which is what breaks when a metric is renamed.
  3. Every panel has a title and a datasource, so nothing renders as an
     unlabelled empty box.

The metric names are read out of the Go source rather than listed here, so
renaming a metric and forgetting the dashboard is a failure rather than a
surprise in production.
"""

import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
DASHBOARDS = ROOT / "deploy" / "grafana"
PROM_SOURCE = ROOT / "typesafeprom" / "prom.go"

OK = "\033[32m"
ERR = "\033[31m"
DIM = "\033[2m"
OFF = "\033[0m"


def exported_metrics() -> set[str]:
    """Metric names typesafeprom exports, read from its source."""
    src = PROM_SOURCE.read_text()
    namespace = re.search(r'Namespace\s*=\s*"([^"]+)"', src)
    if not namespace:
        sys.exit("check_dashboards: cannot find the metric Namespace in prom.go")
    ns = namespace.group(1)

    names = set()
    for m in re.finditer(r'Name:\s*"([a-z0-9_]+)"', src):
        names.add(f"{ns}_{m.group(1)}")
    if not names:
        sys.exit("check_dashboards: found no metric names in prom.go")

    # Histograms expose the derived series a dashboard actually queries.
    for base in list(names):
        names.update({f"{base}_bucket", f"{base}_sum", f"{base}_count"})
    return names


def metrics_in(expr: str) -> set[str]:
    """Metric names referenced by a PromQL expression."""
    # Identifiers that look like our namespace. Anything else in the
    # expression is a function, label or literal, and not ours to check.
    return set(re.findall(r"\btypesafe_[a-z0-9_]+", expr))


def check(path: pathlib.Path, known: set[str]) -> list[str]:
    problems: list[str] = []
    try:
        doc = json.loads(path.read_text())
    except json.JSONDecodeError as e:
        return [f"{path.name}: invalid JSON: {e}"]

    if not doc.get("title"):
        problems.append(f"{path.name}: has no title")
    if "panels" not in doc:
        problems.append(f"{path.name}: has no panels")

    for i, panel in enumerate(doc.get("panels", [])):
        where = f"{path.name} panel {i}"
        if not panel.get("title"):
            problems.append(f"{where}: has no title")
        if panel.get("type") != "row" and not panel.get("datasource"):
            problems.append(f"{where}: has no datasource")

        for target in panel.get("targets", []):
            expr = target.get("expr", "")
            if not expr:
                problems.append(f"{where}: a target has no expression")
                continue
            for name in metrics_in(expr):
                if name not in known:
                    problems.append(
                        f"{where}: references {name}, which typesafeprom does not export"
                    )
    return problems


def main() -> int:
    if not DASHBOARDS.is_dir():
        print(f"  {ERR}✗ {DASHBOARDS} does not exist{OFF}")
        return 1

    files = sorted(DASHBOARDS.glob("*.json"))
    if not files:
        print(f"  {ERR}✗ no dashboards committed under {DASHBOARDS}{OFF}")
        return 1

    known = exported_metrics()
    problems: list[str] = []
    for f in files:
        problems.extend(check(f, known))

    if problems:
        for p in problems:
            print(f"  {ERR}✗ {p}{OFF}")
        return 1

    panels = sum(len(json.loads(f.read_text()).get("panels", [])) for f in files)
    print(f"  {OK}✓ {len(files)} dashboard(s), {panels} panels, every metric exported{OFF}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
