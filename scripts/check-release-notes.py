#!/usr/bin/env python3
"""Require a changed human release summary before publishing a commit."""
import argparse
from pathlib import Path
import re
import subprocess


def current_summary(text):
    text = re.sub(r"<!--.*?(?:-->|$)", "", text, flags=re.DOTALL)
    match = re.search(r"(?ms)^## Current source\n(.*?)(?=^## |\Z)", text)
    if not match:
        raise ValueError("CHANGELOG.md needs a Current source section")
    summary = match[1].strip()
    for heading in ("Changes", "Upgrade impact"):
        section = re.search(r"(?ms)^### " + heading + r"\n(.*?)(?=^### |\Z)", summary)
        bullets = re.findall(r"(?m)^- (.+)", section[1]) if section else []
        if not any(any(char.isalpha() for char in bullet) and not re.fullmatch(
                r"https?://\S+|\[[^\]]+\]\([^)]+\)", bullet.strip()) for bullet in bullets):
            raise ValueError("Current source needs a nonempty " + heading + " bullet")
    return summary


def check_notes(text, previous):
    summary = current_summary(text)
    if previous and summary == current_summary(previous):
        raise ValueError("Current source notes are unchanged; describe this release")
    return summary


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", default="HEAD^", help="comparison commit; CI uses the first parent")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    base = subprocess.check_output(
        ["git", "rev-parse", "--verify", "--end-of-options", args.base + "^{commit}"],
        cwd=root, text=True).strip()
    exists = subprocess.check_output(
        ["git", "ls-tree", "--name-only", base, "--", "CHANGELOG.md"], cwd=root, text=True).strip()
    previous = subprocess.check_output(
        ["git", "show", base + ":CHANGELOG.md"], cwd=root, text=True) if exists else ""
    check_notes((root / "CHANGELOG.md").read_text(), previous)
    print("Release summary and upgrade impact present and changed from " + base[:12])


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        raise SystemExit(str(error)) from error
