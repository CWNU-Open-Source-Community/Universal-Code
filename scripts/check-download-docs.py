#!/usr/bin/env python3
"""Check the four public download entry points, optionally against Release assets.

Offline: python scripts/check-download-docs.py
Before publishing: add --version TAG --revision SHA --release-json release.json
--allow-draft. release.json must be the GitHub REST Release response after upload.
"""

import argparse
import json
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[1]
REPOSITORY = "https://github.com/CWNU-Open-Source-Community/Universal-Code"
DOCUMENTS = ("README.md", "README.en.md", "docs/windows-release.md", "docs/macos-release.md")
START, END = "<!-- public-downloads:start -->", "<!-- public-downloads:end -->"
VERSION = r"v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def check(root, version=None, release=None, revision=None, allow_draft=False):
    for name in DOCUMENTS:
        text = (root / name).read_text(encoding="utf-8")
        require(text.count(START) == text.count(END) == 1,
                f"{name}: public download block is missing or duplicated")
        block = text.split(START)[1].split(END)[0]
        tags = re.findall(re.escape(REPOSITORY) + r"/releases/tag/([^\s)<>#]+)", block)
        require(tags and all(re.fullmatch(VERSION, tag) for tag in tags),
                f"{name}: explicit public Release tag link is missing or invalid")
        if version is None:
            version = tags[0]
        require(set(tags) == {version}, f"{name}: download tag differs from {version}")
        # Check every Release URL, including accidentally retained old links
        # outside the marked download block. Actions candidates are separate.
        urls = re.findall(r"https://github\.com/[^\s)<>]+/releases/[^\s)<>]+", text)
        prefix = REPOSITORY + "/releases/"
        require(all(url.startswith((prefix + "tag/" + version + "#",
                                    prefix + "download/" + version + "/"))
                    or url == prefix + "tag/" + version for url in urls),
                f"{name}: stale, unpinned or foreign Release URL")
        for asset in ("SHA256SUMS",) + (() if name.endswith("macos-release.md") else ("TraverseBoard.exe",)):
            require(f"{prefix}download/{version}/{asset})" in block,
                    f"{name}: {asset} download link is missing")
        if not name.endswith("windows-release.md"):
            require(all(marker in block for marker in ("macos-arm64", "macos-amd64", ".dmg")),
                    f"{name}: both Mac DMG architectures must be documented")

    if release is not None:
        require(release.get("tag_name") == version, "Release tag differs from download documentation")
        require(release.get("draft") is False or (allow_draft and release.get("draft") is True),
                "Release is not public (use --allow-draft only before publishing)")
        assets = release.get("assets", [])
        names = [asset["name"] for asset in assets]
        require(len(names) == len(set(names)), "Release contains duplicate asset names")
        available = {asset["name"] for asset in assets
                     if asset.get("state") == "uploaded" and asset.get("size", 0) > 0}
        require({"TraverseBoard.exe", "SHA256SUMS"} <= available,
                "Release is missing the Windows EXE or SHA256SUMS")
        if revision is not None:
            require(re.fullmatch(r"[0-9a-f]{40}", revision), "Invalid release revision")
        for arch in ("arm64", "amd64"):
            short = re.escape(revision[:12]) if revision else r"[0-9a-f]{12}"
            pattern = rf"TraverseBoard-{re.escape(version)}-macos-{arch}-{short}-preview\.dmg"
            require(len([name for name in available if re.fullmatch(pattern, name)]) == 1,
                    f"Release is missing the exact {arch} DMG")
        for name in DOCUMENTS:
            text = (root / name).read_text(encoding="utf-8")
            linked = re.findall(re.escape(prefix + "download/" + version + "/") + r"([^\s)<>#]+)", text)
            require(set(linked) <= available, f"{name}: linked asset is absent from the Release")
    return version


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=ROOT)
    parser.add_argument("--version")
    parser.add_argument("--revision")
    parser.add_argument("--release-json", type=Path)
    parser.add_argument("--allow-draft", action="store_true")
    args = parser.parse_args()
    try:
        release = json.loads(args.release_json.read_text(encoding="utf-8-sig")) if args.release_json else None
        version = check(args.root, args.version, release, args.revision, args.allow_draft)
    except (OSError, ValueError, KeyError, TypeError) as exc:
        print(f"download-docs: {exc}", file=sys.stderr)
        return 1
    print(f"download-docs: all four entry points agree on {version}" +
          ("; Release assets verified" if release is not None else ""))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
