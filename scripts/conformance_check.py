#!/usr/bin/env python3
"""Cross-check docs/conformance.md against the test code.

Every ID in the matrix must have at least one test that claims it. A test claims an ID by

  * its name: Test_<ID without dashes>[_<more>], for example Test_CONCONC03_20Concurrent
    (Test_CTRFS02_03_... claims CTR-FS-02 and CTR-FS-03), or
  * a marker comment inside a *_test.go file (or "# conformance:" in scripts/*.py):
        // conformance: CON-TOOL-04 CON-TOOL-05

The check fails when a matrix ID has no claim, or when a marker names an ID that is not in
the matrix. A Test_<ID> name that points at an ID outside the matrix only warns, because the
matrix and the tests are edited on different branches (pass --strict to fail on it).
"""

import argparse
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
ID = r"(?:(?:CON|CTR)-[A-Z]+-\d+|INV-\d+|RUL-\d+)"
GO_TEST = re.compile(r"^func (Test_[A-Za-z0-9_]+)\(", re.M)
NAME_ID = re.compile(r"^Test_((?:CON|CTR)[A-Z]+\d+|INV\d+|RUL\d+)((?:_\d{2}(?!\d))*)")
MARKER = re.compile(r"^\s*(?://|#)\s*conformance:\s*(.+?)\s*$")
SKIP_DIRS = {"node_modules", "vendor"}


def matrix_ids(path):
    ids = []
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.startswith("|") or line.startswith("| ---") or line.startswith("| 编号"):
            continue
        cell = line.split("|")[1]
        ids += re.findall(r"\b" + ID + r"\b", cell)
    return list(dict.fromkeys(ids))


def dashed(name, ids):
    """Map a de-dashed name such as CTRFS02 back to the matrix spelling, or None."""
    by_plain = {i.replace("-", ""): i for i in ids}
    return by_plain.get(name)


def sources(root):
    for pattern, kind in (("**/*_test.go", "go"), ("scripts/*.py", "script")):
        for path in sorted(root.glob(pattern)):
            parts = path.relative_to(root).parts
            if SKIP_DIRS & set(parts) or any(part.startswith(".") for part in parts):
                continue
            yield path, kind


def collect(root, ids):
    claims = {i: [] for i in ids}
    problems, warnings = [], []
    for path, kind in sources(root):
        rel = path.relative_to(root).as_posix()
        text = path.read_text(encoding="utf-8", errors="replace")
        if kind == "go":
            for match in GO_TEST.finditer(text):
                found = NAME_ID.match(match.group(1))
                if not found:
                    continue
                plain, tail = found.group(1), found.group(2)
                family = re.sub(r"\d+$", "", plain)
                names = [plain] + [family + part for part in re.findall(r"\d{2}", tail)]
                line = text.count("\n", 0, match.start()) + 1
                for plain_id in names:
                    real = dashed(plain_id, ids)
                    if real:
                        claims[real].append(f"{rel}:{line} {match.group(1)}")
                    else:
                        warnings.append(f"{rel}:{line} {match.group(1)} names an ID that is not in the matrix")
        for number, line in enumerate(text.splitlines(), 1):
            marker = MARKER.match(line)
            comment = "//" if kind == "go" else "#"
            if not marker or not line.lstrip().startswith(comment):
                continue
            for claimed in re.findall(r"[A-Z]+(?:-[A-Z]+)?-\d+", marker.group(1)):
                if claimed in claims:
                    claims[claimed].append(f"{rel}:{number} marker")
                else:
                    problems.append(f"{rel}:{number} marker names {claimed}, which is not in the matrix")
    return claims, problems, warnings


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--root", type=pathlib.Path, default=ROOT)
    parser.add_argument("--matrix", type=pathlib.Path)
    parser.add_argument("--strict", action="store_true", help="fail on Test_<ID> names outside the matrix")
    parser.add_argument("--list", action="store_true", help="print the evidence for every ID")
    options = parser.parse_args()
    matrix = options.matrix or options.root / "docs" / "conformance.md"
    ids = matrix_ids(matrix)
    if not ids:
        print(f"no IDs found in {matrix}", file=sys.stderr)
        return 2
    claims, problems, warnings = collect(options.root, ids)
    missing = [i for i in ids if not claims[i]]
    if options.list:
        for i in ids:
            print(i, "->", "; ".join(claims[i]) or "NONE")
    for warning in warnings:
        print("warning:", warning, file=sys.stderr)
    for problem in problems:
        print("error:", problem, file=sys.stderr)
    for i in missing:
        print(f"error: {i} has no test (name it Test_{i.replace('-', '')}_... or add '// conformance: {i}')", file=sys.stderr)
    covered = len(ids) - len(missing)
    print(f"conformance: {covered}/{len(ids)} IDs have a claiming test")
    if missing or problems or (options.strict and warnings):
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
