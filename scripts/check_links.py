#!/usr/bin/env python3
"""Assert every relative link in the Markdown docs resolves to a real file.

A broken cross-reference is the most common documentation defect and the least
visible: nothing fails, the reader just lands nowhere. Files get renamed and
the links that pointed at them are not what anybody re-reads.

Fenced code blocks and inline code are stripped first. Go calls look exactly
like Markdown links — `TypedChoice[Topic]("Which team?", ...)` matches the link
pattern perfectly — and a checker that flags those is one nobody will keep.

External URLs are not fetched. A network call in a repository check makes the
build depend on somebody else's uptime.
"""

import pathlib
import re
import sys

OK = "\033[32m"
ERR = "\033[31m"
OFF = "\033[0m"

LINK = re.compile(r"\[[^\]]+\]\(([^)]+)\)")


def markdown_files(root: pathlib.Path) -> list[pathlib.Path]:
    out: list[pathlib.Path] = []
    for path in root.rglob("*.md"):
        parts = set(path.parts)
        if ".git" in parts or "node_modules" in parts or "testdata" in parts:
            continue
        out.append(path)
    return sorted(out)


def strip_code(text: str) -> str:
    text = re.sub(r"```.*?```", "", text, flags=re.S)
    return re.sub(r"`[^`\n]*`", "", text)


def main() -> int:
    root = pathlib.Path(__file__).resolve().parent.parent
    broken: list[str] = []
    checked = 0

    for md in markdown_files(root):
        text = strip_code(md.read_text())
        for m in LINK.finditer(text):
            target = m.group(1).split("#")[0].strip()
            if not target or target.startswith(("http://", "https://", "mailto:")):
                continue
            checked += 1
            if not (md.parent / target).exists():
                broken.append(f"{md.relative_to(root)} -> {target}")

    for b in broken:
        print(f"  {ERR}✗ broken link: {b}{OFF}")
    if broken:
        return 1

    print(f"  {OK}✓ {checked} relative link(s) resolve{OFF}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
