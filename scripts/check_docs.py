#!/usr/bin/env python3
"""Assert every exported Go symbol carries a doc comment.

Go's own convention is that exported identifiers are documented, and pkg.go.dev
renders an undocumented one as a bare signature. Enforcing it here keeps the
published reference readable without anyone having to remember.

Deliberately simple: it reads the source rather than running `go doc`, so it
needs no build and no network. It checks the declaration forms this codebase
actually uses.
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# Directories whose contents are not part of the published surface.
SKIP_DIRS = {".git", "testdata", "scripts", "internal", "tests", ".github"}

DECL = re.compile(
    r"^(?P<kind>func|type|const|var)\s+"
    r"(?:\((?P<recv>[^)]*)\)\s*)?"
    r"(?P<name>[A-Z][A-Za-z0-9_]*)"
)
# A method on an unexported receiver is not part of the public surface.
UNEXPORTED_RECV = re.compile(r"\*?\s*[a-z][A-Za-z0-9_]*\s*$")


def go_files():
    for dirpath, dirnames, filenames in os.walk(ROOT):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for name in sorted(filenames):
            if name.endswith(".go") and not name.endswith("_test.go"):
                yield os.path.join(dirpath, name)


def main():
    missing = []
    checked = 0

    for path in go_files():
        with open(path) as f:
            lines = f.read().split("\n")

        in_block = False
        for i, line in enumerate(lines):
            # Skip grouped declarations: their members are documented inline
            # and the group itself carries the comment.
            stripped = line.strip()
            if stripped.endswith("(") and re.match(r"^(const|var|type)\s+\($", stripped):
                in_block = True
                continue
            if in_block:
                if stripped == ")":
                    in_block = False
                continue

            m = DECL.match(line)
            if not m:
                continue
            recv = m.group("recv")
            if recv and UNEXPORTED_RECV.search(recv.split()[-1] if recv.split() else ""):
                continue

            checked += 1
            prev = lines[i - 1].strip() if i > 0 else ""
            if not prev.startswith("//"):
                rel = os.path.relpath(path, ROOT)
                missing.append(f"{rel}:{i + 1}: {m.group('kind')} {m.group('name')}")

    if missing:
        print(f"  {len(missing)} exported symbol(s) missing a doc comment:", file=sys.stderr)
        for m in missing[:40]:
            print(f"    {m}", file=sys.stderr)
        if len(missing) > 40:
            print(f"    ... and {len(missing) - 40} more", file=sys.stderr)
        return 1

    print(f"  ✓ all {checked} exported symbols documented")
    return 0


if __name__ == "__main__":
    sys.exit(main())
