"""Regression coverage for stale links and missing public download assets."""

import copy
import importlib.util
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("download_docs", Path(__file__).with_name("check-download-docs.py"))
docs = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(docs)


class DownloadDocsTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for name in docs.DOCUMENTS:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text((docs.ROOT / name).read_text(encoding="utf-8"), encoding="utf-8")
        self.version = docs.check(self.root)
        self.revision = "1234567890ab" + "c" * 28
        self.release = {"tag_name": self.version, "draft": False, "assets": [
            {"name": name, "state": "uploaded", "size": 1} for name in (
                "TraverseBoard.exe", "SHA256SUMS",
                *(f"TraverseBoard-{self.version}-macos-{arch}-{self.revision[:12]}-preview.dmg"
                  for arch in ("arm64", "amd64")),
            )]}

    def test_published_release_and_assets_match(self):
        self.assertEqual(docs.check(self.root, release=self.release, revision=self.revision), self.version)

    def test_each_stale_translation_or_platform_guide_is_rejected(self):
        for name in docs.DOCUMENTS:
            with self.subTest(name=name):
                path = self.root / name
                original = path.read_text(encoding="utf-8")
                path.write_text(original.replace(self.version, "v0.1.0-old"), encoding="utf-8")
                with self.assertRaises(ValueError):
                    docs.check(self.root, version=self.version)
                path.write_text(original, encoding="utf-8")

    def test_next_release_requires_documentation_update(self):
        with self.assertRaisesRegex(ValueError, "tag differs"):
            docs.check(self.root, version="v1.0.0-next")

    def test_missing_or_empty_asset_is_rejected(self):
        for index in range(len(self.release["assets"])):
            for field, value in (("size", 0), ("state", "new")):
                with self.subTest(index=index, field=field):
                    release = copy.deepcopy(self.release)
                    release["assets"][index][field] = value
                    with self.assertRaises(ValueError):
                        docs.check(self.root, release=release)

    def test_draft_needs_explicit_prepublish_mode(self):
        self.release["draft"] = True
        with self.assertRaisesRegex(ValueError, "not public"):
            docs.check(self.root, release=self.release)
        docs.check(self.root, release=self.release, allow_draft=True)

    def test_wrong_release_revision_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "exact arm64 DMG"):
            docs.check(self.root, release=self.release, revision="d" * 40)

    def test_old_link_outside_download_block_is_rejected(self):
        path = self.root / "README.en.md"
        with path.open("a", encoding="utf-8") as handle:
            handle.write(f"\n[Old download]({docs.REPOSITORY}/releases/tag/v0.1.0-old)\n")
        with self.assertRaisesRegex(ValueError, "stale"):
            docs.check(self.root)

    def test_latest_alias_cannot_hide_prereleases(self):
        path = self.root / "README.md"
        text = path.read_text(encoding="utf-8")
        path.write_text(text.replace("/tag/" + self.version, "/latest"), encoding="utf-8")
        with self.assertRaises(ValueError):
            docs.check(self.root)


if __name__ == "__main__":
    unittest.main()
