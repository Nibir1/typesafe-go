#!/usr/bin/env python3
"""Validate every golden fixture against the vendored OpenAPI schema.

The fixtures are the executable form of the wire contract, so a fixture that
does not satisfy the schema is either a mistake in the fixture or a contract
change nobody noticed. Both are worth failing a build over.
"""
import glob
import json
import os
import sys

try:
    from jsonschema import Draft202012Validator
except ImportError:
    print("jsonschema is not installed; skipping (pip install jsonschema)")
    sys.exit(0)

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SPEC = os.path.join(ROOT, "testdata", "spec", "openapi.json")
FIXTURES = os.path.join(ROOT, "testdata", "contract", "*.json")


def schema_for(spec, name):
    """Return a standalone schema for one component, with $refs rebased."""
    s = dict(spec["components"]["schemas"][name])
    s["$defs"] = spec["components"]["schemas"]
    return json.loads(json.dumps(s).replace("#/components/schemas/", "#/$defs/"))


def main():
    with open(SPEC) as f:
        spec = json.load(f)

    validators = {
        ".request.json": Draft202012Validator(schema_for(spec, "SystemOneRequest")),
        ".response.json": Draft202012Validator(schema_for(spec, "SystemOneResponse")),
    }

    paths = sorted(glob.glob(FIXTURES))
    if not paths:
        print("no fixtures found", file=sys.stderr)
        return 1

    failed = 0
    for path in paths:
        suffix = ".request.json" if path.endswith(".request.json") else ".response.json"
        with open(path) as f:
            doc = json.load(f)
        errors = sorted(validators[suffix].iter_errors(doc), key=lambda e: list(e.path))
        if errors:
            failed += 1
            print(f"  FAIL {os.path.relpath(path, ROOT)}")
            for e in errors[:5]:
                print(f"       {list(e.path)}: {e.message}")

    if failed:
        print(f"\n  {failed} of {len(paths)} fixtures failed validation", file=sys.stderr)
        return 1

    print(f"  ✓ {len(paths)} fixtures validate against the OpenAPI schema")
    return 0


if __name__ == "__main__":
    sys.exit(main())
