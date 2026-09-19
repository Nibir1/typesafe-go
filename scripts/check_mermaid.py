#!/usr/bin/env python3
"""Sanity-check the mermaid diagrams in the Markdown docs.

Not a renderer — rendering needs a browser, and a repository check should not.
This catches the mistakes that make a diagram fail to draw at all, which on
GitHub shows the reader a red error box where a picture should be:

  * a block whose first line is not a recognised diagram type;
  * unbalanced quotes or brackets, which swallow the rest of the diagram;
  * a sequence arrow to a participant nobody declared;
  * an arrow with no label, which renders as a bare line.
"""

import pathlib
import re
import sys

OK, ERR, OFF = "\033[32m", "\033[31m", "\033[0m"

HEAD = re.compile(
    r"^(flowchart|graph|sequenceDiagram|classDiagram|stateDiagram(-v2)?"
    r"|erDiagram|journey|gantt|pie|mindmap|timeline)\b")
ARROW = re.compile(r"^\s*(\w+)\s*(?:-{1,2}>>?|-{1,2}\)|-{1,2}x)\s*\+?(\w+)\s*:(.*)$")


def check(path: pathlib.Path) -> list[str]:
    problems: list[str] = []
    text = path.read_text()
    for index, block in enumerate(re.findall(r"```mermaid\n(.*?)```", text, re.S), 1):
        where = f"{path}: diagram {index}"
        lines = [ln for ln in block.split("\n") if ln.strip()]
        if not lines:
            problems.append(f"{where}: empty")
            continue
        if not HEAD.match(lines[0].strip()):
            problems.append(f"{where}: unrecognised type {lines[0].strip()!r}")
            continue

        for line in lines:
            if line.count('"') % 2:
                problems.append(f"{where}: unbalanced quote: {line.strip()[:60]}")
            for opener, closer in (("[", "]"), ("(", ")"), ("{", "}")):
                if line.count(opener) != line.count(closer):
                    problems.append(
                        f"{where}: unbalanced {opener}{closer}: {line.strip()[:60]}")

        if lines[0].strip() == "sequenceDiagram":
            declared = set(re.findall(r"participant\s+(\w+)", block))
            for line in lines:
                m = ARROW.match(line)
                if not m:
                    continue
                src, dst, label = m.group(1), m.group(2), m.group(3)
                for name in (src, dst):
                    if name not in declared:
                        problems.append(f"{where}: undeclared participant {name!r}")
                if not label.strip():
                    problems.append(f"{where}: arrow {src}->{dst} has no label")
    return problems


def main() -> int:
    root = pathlib.Path(__file__).resolve().parent.parent
    problems, count = [], 0
    for md in sorted(root.rglob("*.md")):
        parts = set(md.parts)
        if {".git", ".venv", "node_modules", "testdata"} & parts:
            continue
        text = md.read_text()
        count += len(re.findall(r"```mermaid\n", text))
        problems += check(md)

    for p in problems:
        print(f"  {ERR}✗ {p}{OFF}")
    if problems:
        return 1
    print(f"  {OK}✓ {count} mermaid diagram(s), no syntax problems{OFF}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
