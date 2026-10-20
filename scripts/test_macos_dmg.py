"""Synthetic DMG packaging tests; actual mounting/signatures need a native Mac."""
import importlib.util
import os
from pathlib import Path
import plistlib
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location("macos_dmg", Path(__file__).with_name("package-desktop-dmg.py"))
dmg = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(dmg)
VERSION = "v1.0.0-preview"
REVISION = "1" * 40


class MacDMGTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.payload = self.root / "fixture"
        self.binary = self.payload / dmg.release.BINARY
        self.binary.parent.mkdir(parents=True)
        self.binary.write_bytes(b"signed-binary-fixture")
        self.binary.chmod(0o755)
        self.entries = [{"path": dmg.release.BINARY, "mode": stat.S_IMODE(self.binary.stat().st_mode),
                         "sha256": dmg.release.sha(self.binary.read_bytes())}]
        self.source = self.root / "zip"
        self.source.mkdir()
        self.archive_name = dmg.release.names(VERSION, REVISION, "arm64")[0]
        (self.source / self.archive_name).write_bytes(b"verified-zip-fixture")
        self.manifest = {"archive_name": self.archive_name, "files": self.entries}
        self.output = self.root / "dmg"
        self.commands = []
        self.corrupt_mount = False

    def run_native(self, command, **kwargs):
        self.commands.append(command)
        if command[0] == "/usr/bin/ditto":
            shutil.copytree(self.payload, Path(command[-1]), dirs_exist_ok=True)
        elif command[:2] == [dmg.HDIUTIL, "create"]:
            stage = Path(command[command.index("-srcfolder") + 1])
            dmg.verify_payload(stage, self.entries, applications=True, exact=True)
            Path(command[-1]).write_bytes(b"disk-image-fixture")
        elif command[:2] == [dmg.HDIUTIL, "attach"]:
            self.assertTrue(all(flag in command for flag in ("-readonly", "-nobrowse", "-noautoopen")))
            mountpoint = Path(command[command.index("-mountpoint") + 1])
            shutil.copytree(self.payload, mountpoint, dirs_exist_ok=True)
            (mountpoint / "Applications").symlink_to("/Applications")
            if self.corrupt_mount:
                (mountpoint / dmg.release.BINARY).write_bytes(b"changed")
            return subprocess.CompletedProcess(command, 0, plistlib.dumps({
                "system-entities": [{"mount-point": str(mountpoint)}],
            }))
        return subprocess.CompletedProcess(command, 0, b"")

    def package(self):
        with mock.patch.object(dmg, "ROOT", self.root), \
                mock.patch.object(dmg.sys, "platform", "darwin"), \
                mock.patch.object(dmg.release, "verify", return_value=self.manifest) as verified, \
                mock.patch.object(dmg.subprocess, "run", side_effect=self.run_native):
            result = dmg.package(VERSION, REVISION, "arm64", self.source, self.output)
        verified.assert_called_once_with(self.source, VERSION, REVISION, "arm64", native=True)
        return result

    def test_package_keeps_zip_and_binds_dmg_checksum(self):
        image, digest, checksum = self.package()
        self.assertEqual(image.read_bytes(), b"disk-image-fixture")
        self.assertEqual(checksum.read_bytes(), (digest + "  " + image.name + "\n").encode("utf-8"))
        self.assertEqual((self.source / self.archive_name).read_bytes(), b"verified-zip-fixture")
        self.assertEqual({path.name for path in self.output.iterdir()}, {image.name, checksum.name})
        self.assertFalse(list(self.root.glob("macos-dmg-*")))
        self.assertTrue(any(command[:2] == [dmg.HDIUTIL, "verify"] for command in self.commands))
        self.assertTrue(any(command[0] == "/usr/bin/codesign" for command in self.commands))
        self.assertTrue(any(command[:2] == [dmg.HDIUTIL, "detach"] for command in self.commands))

    def test_help_does_not_create_checkout_bytecode(self):
        scripts = self.root / "scripts"
        scripts.mkdir()
        for source in (Path(dmg.__file__), Path(dmg.release.__file__)):
            shutil.copyfile(source, scripts / source.name)
        env = dict(os.environ)
        for key in ("PYTHONDONTWRITEBYTECODE", "PYTHONPYCACHEPREFIX"):
            env.pop(key, None)
        # Apple Python redirects its cache by default. Clear that prefix so
        # this also catches regressions on Python builds using __pycache__.
        result = subprocess.run([sys.executable, "-X", "pycache_prefix=",
                                 str(scripts / "package-desktop-dmg.py"), "--help"],
                                env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("usage:", result.stdout)
        self.assertFalse((scripts / "__pycache__").exists())

    def test_changed_mounted_binary_is_rejected_and_detached(self):
        self.corrupt_mount = True
        with self.assertRaisesRegex(ValueError, "hash differs"):
            self.package()
        self.assertEqual(list(self.output.iterdir()), [])
        self.assertFalse(list(self.root.glob("macos-dmg-*")))
        self.assertTrue(any(command[:2] == [dmg.HDIUTIL, "detach"] for command in self.commands))

    def test_previous_output_is_not_replaced(self):
        self.output.mkdir()
        previous = self.output / (self.archive_name[:-4] + ".dmg")
        previous.write_bytes(b"previous")
        with self.assertRaisesRegex(ValueError, "already exists"):
            self.package()
        self.assertEqual(previous.read_bytes(), b"previous")
        self.assertEqual(self.commands, [])

    def test_changed_source_payload_is_rejected(self):
        self.binary.write_bytes(b"changed")
        with self.assertRaisesRegex(ValueError, "hash differs"):
            self.package()
        self.assertFalse(any(command[:2] == [dmg.HDIUTIL, "create"] for command in self.commands))
        self.assertFalse(list(self.root.glob("macos-dmg-*")))

    def test_applications_shortcut_is_required(self):
        with self.assertRaisesRegex(ValueError, "Applications shortcut"):
            dmg.verify_payload(self.payload, self.entries, applications=True)
        (self.payload / "Applications").symlink_to("/tmp")
        with self.assertRaisesRegex(ValueError, "Applications shortcut"):
            dmg.verify_payload(self.payload, self.entries, applications=True)

    def test_detach_retries_owned_mount_on_failure_or_timeout(self):
        for first in (subprocess.CompletedProcess([], 1), subprocess.TimeoutExpired([], 30)):
            with self.subTest(first=first), mock.patch.object(dmg.subprocess, "run", side_effect=[
                first, subprocess.CompletedProcess([], 0),
            ]) as run:
                dmg.detach(self.root / "mounted")
                self.assertEqual(run.call_args_list[1].args[0],
                                 [dmg.HDIUTIL, "detach", "-force", str(self.root / "mounted")])


if __name__ == "__main__":
    unittest.main()
