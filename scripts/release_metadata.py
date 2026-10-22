#!/usr/bin/env python3
"""Validate the four project versions and extract only the current release notes."""
import argparse
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def metadata():
    def capture(path, pattern):
        match = re.search(pattern, (ROOT / path).read_text(encoding="utf-8"), re.M)
        if not match:
            raise ValueError(f"Cannot read version from {path}")
        return match.group(1)

    version = capture("backend/pkg/api/server.go", r'const CurrentVersion = "([^"]+)"')
    if not re.fullmatch(r"\d+\.\d+\.\d+", version):
        raise ValueError(f"Invalid version: {version}")
    versions = {
        "server": version,
        "manifest": capture("fpk-package/manifest", r"^version=([^\r\n]+)"),
        "handover": capture("HANDOVER.md", r"\| \*\*当前版本\*\* \| `([^`]+)`"),
        "notes": capture("RELEASE_NOTES.md", r"\A## 曲率 \(yinshu-ai\) v([^\r\n]+)"),
    }
    if len(set(versions.values())) != 1:
        raise ValueError(f"Version mismatch: {versions}")
    notes = (ROOT / "RELEASE_NOTES.md").read_text(encoding="utf-8")
    section = re.split(r"(?m)^## ", notes, maxsplit=2)[1]
    body = section.split("\n", 1)[1].strip()
    if not body:
        raise ValueError("Current release notes are empty")
    return version, body + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--notes", type=Path)
    args = parser.parse_args()
    version, notes = metadata()
    if args.notes:
        args.notes.write_text(notes, encoding="utf-8")
    print(version)


if __name__ == "__main__":
    main()
