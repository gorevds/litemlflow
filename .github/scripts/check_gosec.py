#!/usr/bin/env python3
"""Gate CI on gosec output: fail if any HIGH finding isn't on the
accept-list in docs/security-audit.md.

Usage: check_gosec.py /path/to/gosec.json docs/security-audit.md

The accept-list is parsed from a fenced code block under the heading
"Accepted gosec false-positives". Format: one finding per line:

    G115 internal/server/middleware.go:271
    G202 internal/store/sqlite.go:467

Lines outside that fenced block are ignored.
"""
from __future__ import annotations

import json
import re
import sys
from pathlib import Path


def parse_accept_list(audit_md: Path) -> set[str]:
    text = audit_md.read_text(encoding="utf-8")
    in_block = False
    block_lines: list[str] = []
    started = False
    for line in text.splitlines():
        if not started and re.match(r"^#+\s*Accepted gosec false-positives", line):
            started = True
            continue
        if started and not in_block and line.startswith("```"):
            in_block = True
            continue
        if in_block and line.startswith("```"):
            break
        if in_block:
            block_lines.append(line.strip())
    accepts: set[str] = set()
    for line in block_lines:
        if not line or line.startswith("#"):
            continue
        parts = line.split()
        if len(parts) < 2:
            continue
        rule, loc = parts[0], parts[1]
        accepts.add(f"{rule}:{loc}")
    return accepts


def main() -> int:
    if len(sys.argv) != 3:
        print(f"usage: {sys.argv[0]} GOSEC_JSON SECURITY_AUDIT_MD", file=sys.stderr)
        return 2
    gosec = json.loads(Path(sys.argv[1]).read_text())
    accept = parse_accept_list(Path(sys.argv[2]))

    violators: list[dict] = []
    for issue in gosec.get("Issues", []):
        if issue.get("severity") != "HIGH":
            continue
        # Strip absolute prefix; CI runs in the repo root.
        f = issue["file"]
        for prefix in ("/home/runner/work/", "/home/sber/gorev/litemlflow/"):
            if f.startswith(prefix):
                # strip first two path components for github runner / local
                f = f[len(prefix):]
                if "/" in f:
                    parts = f.split("/")
                    # On runner: <repo>/<repo>/file/path → keep from index 2
                    if len(parts) >= 3 and parts[0] == parts[1]:
                        f = "/".join(parts[2:])
                break
        key = f"{issue['rule_id']}:{f}:{issue['line']}"
        if key not in accept:
            violators.append({
                "rule": issue["rule_id"],
                "file": f,
                "line": issue["line"],
                "details": issue["details"][:120],
            })

    if violators:
        print(f"::error::{len(violators)} new HIGH gosec finding(s) not on the accept-list")
        for v in violators:
            print(f"  {v['rule']:5} {v['file']}:{v['line']}  {v['details']}")
        print()
        print("Either fix the finding, or add it to the 'Accepted gosec false-positives'")
        print("block in docs/security-audit.md with a justification.")
        return 1

    print(f"all HIGH gosec findings are on the accept-list ({len(accept)} entries)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
