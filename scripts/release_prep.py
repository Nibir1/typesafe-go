#!/usr/bin/env python3
"""Rewrite every submodule's go.mod to depend on a published version.

    scripts/release_prep.py --version v1.0.0
    scripts/release_prep.py --version v1.0.0 --revert

During development each submodule carries

    replace github.com/nibir1/typesafe-go => ../
    require github.com/nibir1/typesafe-go v0.0.0

so local builds use local code. That is fine locally and fatal once published:
**Go ignores a `replace` in a dependency's go.mod**, so a consumer reads
`require ... v0.0.0`, fails to find it on the proxy, and cannot build the
module at all.

This strips the in-repo replaces and pins the real version. `--revert` puts
them back, for continuing work after a release.

The order matters and is not negotiable:

  1. Tag the root:              git tag v1.0.0 && git push origin v1.0.0
  2. Wait for the proxy to have it (a minute or two).
  3. scripts/release_prep.py --version v1.0.0
  4. go mod tidy in each submodule, run the tests.
  5. Commit, then tag each submodule: typesafecache/v1.0.0, and so on.

The root must be published before step 3, because after it the submodules
genuinely resolve their dependency from the proxy — and if the tag is not there
yet, nothing builds.
"""

import argparse
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# Published modules, in dependency order: nethttp is depended on by the three
# framework integrations, so it has to be tagged before they can resolve it.
SUBMODULES = [
    "lint",
    "typesafecache",
    "typesafeotel",
    "typesafeprom",
    "integrations/nethttp",
    "integrations/gin",
    "integrations/echo",
    "integrations/fiber",
    "integrations/langchaingo",
    "integrations/temporal",
    "integrations/mcp",
]

# Not published: their replaces never reach anyone.
UNPUBLISHED = ["examples", "deploy/example"]

PLACEHOLDER = "v0.0.0"
OK, ERR, DIM, OFF = "\033[32m", "\033[31m", "\033[2m", "\033[0m"


def relative_path(module: str, target: str) -> str:
    """The replace path a module uses to reach another in this repo."""
    depth = module.count("/") + 1
    up = "../" * depth
    if target == "github.com/nibir1/typesafe-go":
        return up
    suffix = target[len("github.com/nibir1/typesafe-go/"):]
    return up + suffix


def strip_replaces(text: str) -> str:
    """Remove in-repo replace directives, single-line and block form."""
    # Single-line.
    text = re.sub(
        r"^\s*replace\s+github\.com/nibir1/typesafe-go\S*\s*=>.*\n", "", text, flags=re.M)

    # Block form: drop matching entries, then the block if it is now empty.
    def prune_block(m: re.Match) -> str:
        body = m.group(1)
        kept = [ln for ln in body.splitlines()
                if not re.match(r"\s*github\.com/nibir1/typesafe-go\S*\s*=>", ln)]
        kept = [ln for ln in kept if ln.strip()]
        if not kept:
            return ""
        return "replace (\n" + "\n".join(kept) + "\n)\n"

    text = re.sub(r"replace\s*\(\n(.*?)\n\)\n", prune_block, text, flags=re.S)
    return re.sub(r"\n{3,}", "\n\n", text)


# A require appears two ways: inside a `require (` block, where the line is
# just the path and version, and on its own as `require <path> <version>`.
# Matching only the first left single-line requires pinned at v0.0.0 while
# their replace had already been stripped — the exact broken state this script
# exists to prevent. check_release_ready.py caught it.
REQUIRE_LINE = re.compile(
    r"^(\s*(?:require\s+)?)(github\.com/nibir1/typesafe-go\S*)(\s+)v\S+", re.M)


def pin(text: str, version: str) -> str:
    """Point every in-repo require at version."""
    return REQUIRE_LINE.sub(
        lambda m: f"{m.group(1)}{m.group(2)}{m.group(3)}{version}", text)


def add_replaces(text: str, module: str) -> str:
    """Put development replaces back, if they are not already there.

    An earlier version searched for "\nrequire" and inserted before it. In a
    go.mod whose first require is a block written as "require (" preceded by
    something else, or where a replace already existed, that either inserted
    nothing or produced a duplicate — and the revert silently touched only 5 of
    11 modules.
    """
    targets = sorted({m.group(2) for m in REQUIRE_LINE.finditer(text)})
    if not targets:
        return text

    needed = [t for t in targets
              if not re.search(rf"^\s*(?:replace\s+)?{re.escape(t)}\s*=>", text, re.M)]
    if not needed:
        return text

    lines = "\n".join(f"replace {t} => {relative_path(module, t)}" for t in needed)

    # Insert after the `go` directive, which every go.mod has exactly one of.
    m = re.search(r"^go\s+\S+$", text, re.M)
    if m:
        return text[:m.end()] + "\n\n" + lines + text[m.end():]
    return text.rstrip() + "\n\n" + lines + "\n"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--version", required=True, help="the published version, e.g. v1.0.0")
    ap.add_argument("--revert", action="store_true",
                    help="restore development replaces instead")
    args = ap.parse_args()

    if not re.fullmatch(r"v\d+\.\d+\.\d+(-\S+)?", args.version):
        print(f"  {ERR}✗ {args.version} is not a semantic version tag{OFF}")
        return 2

    changed = 0
    for module in SUBMODULES:
        path = os.path.join(ROOT, module, "go.mod")
        if not os.path.isfile(path):
            print(f"  {ERR}✗ {module}/go.mod is missing{OFF}")
            return 1

        original = open(path).read()
        if args.revert:
            text = pin(original, PLACEHOLDER)
            text = add_replaces(text, module)
        else:
            text = strip_replaces(original)
            text = pin(text, args.version)

        if text != original:
            open(path, "w").write(text)
            changed += 1
            print(f"  {DIM}{'reverted' if args.revert else 'pinned  '} {module}{OFF}")

    verb = "reverted to development" if args.revert else f"pinned to {args.version}"
    print(f"  {OK}✓ {changed} module(s) {verb}{OFF}")
    if not args.revert:
        print(f"  {DIM}  Now: go mod tidy in each, run the tests, commit, then tag"
              f" each submodule as <module>/{args.version}{OFF}")
        print(f"  {DIM}  Not published, deliberately untouched: "
              f"{', '.join(UNPUBLISHED)}{OFF}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
