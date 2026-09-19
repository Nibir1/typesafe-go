#!/usr/bin/env python3
"""Assert every third-party module compiled into this repository is
redistributable under Apache-2.0.

The core has no dependencies, so this is about the optional modules. It reads
the licence of every module that `go list -deps` says is actually built —
not everything in `go list -m all`, which includes modules the build never
touches and whose licences are therefore nobody's problem.

No network. A repository check that depends on someone else's uptime is a
check that fails for reasons unrelated to the change being tested. The one
module whose licence cannot be read from its zip is recorded below with the
evidence, rather than fetched at runtime.
"""

import json
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# Modules with third-party dependencies. The core, typesafecache,
# integrations/nethttp and examples have none, so they have nothing to audit —
# `make deps-graph` is what asserts that stays true.
MODULES = [
    "lint",
    "typesafeotel",
    "typesafeprom",
    "integrations/gin",
    "integrations/echo",
    "integrations/fiber",
    "integrations/langchaingo",
    "integrations/temporal",
    "integrations/mcp",
    "deploy/example",
]

LICENSE_FILES = [
    "LICENSE", "LICENSE.txt", "LICENSE.md", "LICENCE", "LICENCE.txt",
    "COPYING", "COPYING.txt", "LICENSE-MIT", "LICENSE.BSD", "LICENSE-APACHE",
]

PERMISSIVE = re.compile(
    r"Apache License|MIT License|BSD 3-Clause|BSD 2-Clause|ISC License"
    r"|Permission is hereby granted, free of charge"
    r"|Redistribution and use in source and binary forms",
    re.I,
)

# Copyleft and weak-copyleft terms that would constrain Apache-2.0
# redistribution. Finding one is not automatically fatal — MPL files can be
# shipped alongside — but it is a decision for a person, so the build stops.
COPYLEFT = re.compile(
    r"GNU GENERAL PUBLIC LICENSE|GNU LESSER GENERAL|GNU AFFERO"
    r"|Mozilla Public License|Eclipse Public License|Common Development",
    re.I,
)

# Modules whose zip carries no licence file, with the evidence for allowing
# them. Each entry names what was checked and where, so the next reader does
# not have to repeat the work.
KNOWN_MISSING = {
    "github.com/nexus-rpc/nexus-proto-annotations": (
        "MIT. The Go module is rooted at the repository's go/ subdirectory and "
        "the LICENSE sits at the repository root, so the module zip omits it. "
        "Verified against the GitHub licence API for nexus-rpc/"
        "nexus-proto-annotations on 2026-09-19: spdx_id MIT. Reached only as a "
        "transitive dependency of go.temporal.io/sdk."
    ),
}

OK = "\033[32m"
ERR = "\033[31m"
WARN = "\033[33m"
DIM = "\033[2m"
OFF = "\033[0m"


def built_modules(module_dir: str) -> dict[str, str]:
    """Every third-party module actually compiled, mapped to its cache dir."""
    proc = subprocess.run(
        ["go", "list", "-deps", "-json", "./..."],
        cwd=module_dir, capture_output=True, text=True,
    )
    if proc.returncode != 0:
        print(f"  {ERR}✗ go list failed in {module_dir}:{OFF}\n{proc.stderr}")
        raise SystemExit(1)

    out: dict[str, str] = {}
    decoder = json.JSONDecoder()
    text, i = proc.stdout, 0
    while i < len(text):
        while i < len(text) and text[i] in " \n\t\r":
            i += 1
        if i >= len(text):
            break
        obj, i = decoder.raw_decode(text, i)
        mod = obj.get("Module")
        if not mod or mod.get("Main"):
            continue
        path = mod.get("Path", "")
        if not path or path.startswith("github.com/nibir1/typesafe-go"):
            continue
        out.setdefault(path, mod.get("Dir") or "")
    return out


def classify(path: str, cache_dir: str) -> tuple[str, str | None]:
    """Return (label, problem)."""
    if not cache_dir or not os.path.isdir(cache_dir):
        return "not downloaded", None

    for name in LICENSE_FILES:
        candidate = os.path.join(cache_dir, name)
        if os.path.isfile(candidate):
            text = open(candidate, errors="replace").read(4000)
            if COPYLEFT.search(text):
                found = COPYLEFT.search(text).group(0)
                return found, f"{path}: {found}"
            if PERMISSIVE.search(text):
                return PERMISSIVE.search(text).group(0), None
            return "unrecognised", f"{path}: licence text not recognised"

    if path in KNOWN_MISSING:
        return "no file (allowed)", None
    return "no licence file", f"{path}: no licence file in the module zip"


def main() -> int:
    seen: dict[str, str] = {}
    problems: list[str] = []

    for module in MODULES:
        directory = os.path.join(ROOT, module)
        if not os.path.isdir(directory):
            continue
        for path, cache_dir in built_modules(directory).items():
            if path in seen:
                continue
            label, problem = classify(path, cache_dir)
            seen[path] = label
            if problem:
                problems.append(problem)

    counts: dict[str, int] = {}
    for label in seen.values():
        counts[label] = counts.get(label, 0) + 1

    for label in sorted(counts):
        print(f"  {DIM}{counts[label]:4d}  {label}{OFF}")

    for path, reason in KNOWN_MISSING.items():
        if seen.get(path) == "no file (allowed)":
            print(f"  {WARN}!{OFF} {path}: {reason}")

    if problems:
        print(f"\n  {ERR}✗ {len(problems)} licence problem(s):{OFF}")
        for p in problems:
            print(f"    {ERR}{p}{OFF}")
        print(f"\n  {DIM}Add an entry to KNOWN_MISSING with evidence, or drop "
              f"the dependency.{OFF}")
        return 1

    print(f"  {OK}✓ {len(seen)} third-party module(s), every licence compatible "
          f"with Apache-2.0 redistribution{OFF}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
