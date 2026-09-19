#!/usr/bin/env python3
"""Assert the repository is in a state that can actually be released.

Run before tagging. Everything here is a mistake that is invisible until
somebody tries to depend on the result, which is the worst time to find it.

The one that motivated this file: every submodule carries

    replace github.com/nibir1/typesafe-go => ../
    require github.com/nibir1/typesafe-go v0.0.0

so that local development builds against local code. **Go ignores a `replace`
in a dependency's go.mod.** A consumer running

    go get github.com/nibir1/typesafe-go/typesafecache@v1.0.0

reads that file, ignores the replace, looks for `typesafe-go v0.0.0` on the
proxy, does not find it, and cannot build. The module is tagged, published, and
unusable.

`make release-prep VERSION=v1.2.3` rewrites those files. This checks the
rewrite happened.
"""

import argparse
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

SUBMODULES = [
    "lint", "typesafecache", "typesafeotel", "typesafeprom",
    "integrations/nethttp", "integrations/gin", "integrations/echo",
    "integrations/fiber", "integrations/langchaingo", "integrations/temporal",
    "integrations/mcp",
]

# Not published, so their replaces are nobody's problem.
UNPUBLISHED = ["examples", "deploy/example"]

# Both spellings of a replace: the single-line form and the block form, where
# the `replace` keyword is on its own line and the entries below it are bare.
# The first version of this file only matched the single-line form and let
# integrations/echo and integrations/fiber through.
IN_REPO = re.compile(
    r"^\s*(?:replace\s+)?(github\.com/nibir1/typesafe-go\S*)\s*=>", re.M)
REQUIRE = re.compile(r"^\s*(github\.com/nibir1/typesafe-go\S*)\s+(v\S+)", re.M)

OK, ERR, WARN, DIM, OFF = "\033[32m", "\033[31m", "\033[33m", "\033[2m", "\033[0m"


def check_replaces(version: str | None) -> list[str]:
    problems = []
    for module in SUBMODULES:
        gomod = os.path.join(ROOT, module, "go.mod")
        if not os.path.isfile(gomod):
            problems.append(f"{module}: no go.mod")
            continue
        text = open(gomod).read()

        for replaced in IN_REPO.findall(text):
            problems.append(
                f"{module}/go.mod still replaces {replaced}. A published module's "
                f"replace is ignored by consumers — run `make release-prep`."
            )

        for path, ver in REQUIRE.findall(text):
            if ver in ("v0.0.0", "v0.0.0-00010101000000-000000000000"):
                problems.append(
                    f"{module}/go.mod requires {path} {ver}, which is not a real "
                    f"published version."
                )
            elif version and ver != version:
                problems.append(
                    f"{module}/go.mod requires {path} {ver}, but this release is "
                    f"{version}."
                )
    return problems


def check_version_constant(version: str | None) -> list[str]:
    """Version must be a var, or -ldflags cannot stamp it."""
    doc = open(os.path.join(ROOT, "doc.go")).read()
    if re.search(r"^const Version\b", doc, re.M):
        return ["doc.go declares Version as a const; -ldflags -X can only set a "
                "variable, so a release build would report the wrong version."]
    return []


def check_changelog(version: str | None) -> list[str]:
    if not version:
        return []
    text = open(os.path.join(ROOT, "CHANGELOG.md")).read()
    if f"[{version.lstrip('v')}]" not in text:
        return [f"CHANGELOG.md has no entry for {version}."]
    if "unreleased" in text.split(f"[{version.lstrip('v')}]")[1][:40].lower():
        return [f"CHANGELOG.md still marks {version} as unreleased."]
    return []


def check_clean_tree() -> list[str]:
    out = subprocess.run(["git", "status", "--porcelain"], cwd=ROOT,
                         capture_output=True, text=True).stdout.strip()
    return [f"the working tree is not clean:\n{out}"] if out else []


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--version", help="the version about to be tagged, e.g. v1.0.0")
    ap.add_argument("--allow-dirty", action="store_true")
    args = ap.parse_args()

    problems: list[str] = []
    problems += check_replaces(args.version)
    problems += check_version_constant(args.version)
    problems += check_changelog(args.version)
    if not args.allow_dirty:
        problems += check_clean_tree()

    if problems:
        print(f"  {ERR}✗ not ready to release:{OFF}")
        for p in problems:
            print(f"    {ERR}{p}{OFF}")
        return 1

    scope = args.version or "the current tree"
    print(f"  {OK}✓ ready to release {scope}{OFF}")
    print(f"  {DIM}  {len(SUBMODULES)} submodule(s) checked; "
          f"{', '.join(UNPUBLISHED)} are not published{OFF}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
