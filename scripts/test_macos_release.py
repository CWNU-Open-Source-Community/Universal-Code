"""Archive corruption/identity tests. Fixtures are synthetic, not Mac GUI evidence."""
import importlib.util
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import stat
import struct
import subprocess
import tempfile
import unittest
from unittest import mock
import zipfile


spec = importlib.util.spec_from_file_location("macos_release", Path(__file__).with_name("macos-release.py"))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)
VERSION = "v1.0.0-rc.1"
REVISION = "1234567890abcdef" * 2 + "12345678"


class MacToolchainTest(unittest.TestCase):
    def test_build_requires_the_exact_patched_toolchain(self):
        root = Path(__file__).resolve().parents[1]
        if os.name == "nt":
            git = shutil.which("git")
            bash = Path(git).parents[1] / "bin/bash.exe" if git else None
        else:
            bash = shutil.which("bash")
        if not bash or not Path(bash).is_file():
            self.skipTest("Bash is required for the macOS builder preflight")
        source = (root / "scripts/build-desktop-darwin.sh").read_text(encoding="utf-8")
        preflight = source[source.index('expectedGoVersion='):source.index('mkdir -p "$OutputDirectory"')]
        module = (root / "go.mod").read_text(encoding="utf-8")
        pinned = "go" + re.search(r"(?m)^go ([0-9]+\.[0-9]+\.[0-9]+)$", module).group(1)
        shell = 'set -eu\ndie() { echo "$1" >&2; exit 1; }\ngo() { printf "%s\\n" "$TEST_GO_VERSION"; }\n'
        with tempfile.TemporaryDirectory() as temporary:
            # CRLF is a valid Go module file too; do not reject it as unpinned.
            Path(temporary, "go.mod").write_bytes(module.replace("\n", "\r\n").encode())
            for version in (pinned, "go1.26.8", "go1.27.0", "go1.27.1", "go1.27.2"):
                with self.subTest(version=version):
                    result = subprocess.run([str(bash), "-c", shell + preflight], cwd=temporary,
                                            env=dict(os.environ, TEST_GO_VERSION=version),
                                            capture_output=True, text=True)
                    if version == pinned:
                        self.assertEqual(result.returncode, 0, result.stderr)
                    else:
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn("Desktop build requires " + pinned, result.stderr)


class MacArchiveTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.contents = {name: b"fixture" for name in release.FILES}
        self.contents[release.BINARY] = bytes.fromhex("cffaedfe") + struct.pack("<I", 0x0100000C) + b"fixture"
        self.metadata = {
            "protocol_version": "portable_release_metadata.v1", "app_version": VERSION,
            "revision": REVISION, "target_os": "darwin", "target_arch": "arm64",
            "app_bundle_name": release.APP, "binary_name": "cyberagent-desktop",
            "sha256": release.sha(self.contents[release.BINARY]),
            "operator_preview_launcher_name": release.LAUNCHER,
            "operator_preview_launcher_sha256": release.sha(self.contents[release.LAUNCHER]),
            "local_test_guide_name": "LOCAL-TEST-GUIDE.txt",
            "local_test_guide_sha256": release.sha(self.contents["LOCAL-TEST-GUIDE.txt"]),
            "modified": False, "reproducibility_checked": True, "reproducible": True,
            "app_bundle_ad_hoc_signed": True, "app_bundle_notarized": False,
            "manual_macos_matrix_required": True,
        }
        self.compat = {
            "protocol_version": "macos_portable_compatibility.v1",
            "sha256": self.metadata["sha256"], "machine": "arm64", "app_bundle_name": release.APP,
            "automated_checks_passed": True, "release_ready": False, "ad_hoc_signed": True,
            "notarized": False, "manual_macos_matrix_required": True,
            "checks": [{"id": check, "status": "pass"} for check in (
                "macho_magic", "macho_arch", "macho_deployment_target", "sha256_binding",
                "release_identity", "go_target", "non_installing_boundary", "operator_preview_package",
                "operator_preview_safe_flags", "local_test_guide", "default_ui_language",
                "go_build_metadata", "codesign_signature", "consecutive_build_hash",
            )] + [{"id": "macos_matrix", "status": "manual"}],
        }
        self.contents[release.APP + "/Contents/Info.plist"] = plistlib.dumps({
            "CFBundleShortVersionString": "1.0.0", "CFBundleVersion": "1.0.0",
            "CFBundleIdentifier": "workbench.prayu.desktop", "CFBundleExecutable": "cyberagent-desktop",
            "LSMinimumSystemVersion": "12.0.0",
        })

    def write(self, modes=None, extra=None):
        self.contents["release-metadata.json"] = json.dumps(self.metadata).encode()
        self.contents["macos-compatibility.json"] = json.dumps(self.compat).encode()
        archive_name, manifest_name, checksum_name = release.names(VERSION, REVISION, "arm64")
        path = self.directory / archive_name
        rows = []
        with zipfile.ZipFile(path, "w") as archive:
            for name, data in {**self.contents, **(extra or {})}.items():
                mode = (modes or {}).get(name, 0o755 if name in release.EXECUTABLES else 0o644)
                info = zipfile.ZipInfo(name)
                info.create_system = 3
                info.external_attr = (stat.S_IFREG | mode) << 16
                archive.writestr(info, data)
                rows.append({"path": name, "size": len(data), "sha256": release.sha(data), "mode": mode})
        manifest = {
            "protocol_version": "macos_release_archive.v1", "version": VERSION, "revision": REVISION,
            "target_os": "darwin", "target_arch": "arm64", "channel": "preview", "archive_name": archive_name,
            "archive_sha256": release.sha(path.read_bytes()), "ad_hoc_signed": True, "notarized": False,
            "release_ready": False, "files": sorted(rows, key=lambda row: row["path"]),
        }
        (self.directory / manifest_name).write_text(json.dumps(manifest), encoding="utf-8")
        (self.directory / checksum_name).write_bytes(
            f"{manifest['archive_sha256']}  {archive_name}\n{release.sha((self.directory / manifest_name).read_bytes())}  {manifest_name}\n".encode("utf-8"))
        return path

    def verify(self, **kwargs):
        return release.verify(self.directory, VERSION, REVISION, "arm64", **kwargs)

    def test_valid_preview_and_stable_rejection(self):
        self.write()
        self.assertFalse(self.verify()["release_ready"])
        with self.assertRaisesRegex(ValueError, "Stable macOS publication requires"):
            self.verify(require_ready=True)

    def test_package_writes_portable_sidecars(self):
        self.write()
        source = self.directory / "source"
        for name, data in self.contents.items():
            path = source / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        (self.directory / "LICENSE").write_bytes(self.contents["LICENSE"])

        # Exercise package(), including its sidecar writers, with synthetic
        # inputs. Native signature checks remain covered by the Mac builder.
        def archive_stage(command, check):
            self.assertEqual(command[:4], ["/usr/bin/ditto", "-c", "-k", "--norsrc"])
            self.assertTrue(check)
            stage = Path(command[4])
            with zipfile.ZipFile(command[5], "w") as archive:
                for path in stage.rglob("*"):
                    if path.is_file():
                        name = path.relative_to(stage).as_posix()
                        info = zipfile.ZipInfo(name)
                        info.create_system = 3
                        mode = 0o755 if name in release.EXECUTABLES else 0o644
                        info.external_attr = (stat.S_IFREG | mode) << 16
                        archive.writestr(info, path.read_bytes())

        output = self.directory / "packaged"
        with mock.patch.object(release, "ROOT", self.directory.resolve()), \
                mock.patch.object(release.sys, "platform", "darwin"), \
                mock.patch.object(release.subprocess, "run", side_effect=archive_stage), \
                mock.patch.object(release, "native_verify") as native_verify:
            manifest = release.package(source, output, VERSION, REVISION, "arm64")
        native_verify.assert_called_once()
        self.assertEqual(release.verify(output, VERSION, REVISION, "arm64"), manifest)
        for name in release.names(VERSION, REVISION, "arm64")[1:]:
            data = (output / name).read_bytes()
            self.assertTrue(data.endswith(b"\n"))
            self.assertNotIn(b"\r", data)

    def test_old_revision_inside_rehashed_archive(self):
        self.metadata["revision"] = "f" * 40
        self.write()
        with self.assertRaisesRegex(ValueError, "metadata differs: revision"):
            self.verify()

    def test_incorrect_minimum_os_inside_rehashed_archive(self):
        name = release.APP + "/Contents/Info.plist"
        plist = plistlib.loads(self.contents[name])
        for minimum in ("11.0.0", "13.0.0"):
            with self.subTest(minimum=minimum):
                self.contents[name] = plistlib.dumps({**plist, "LSMinimumSystemVersion": minimum})
                self.write()
                with self.assertRaisesRegex(ValueError, "bundle identity differs: LSMinimumSystemVersion"):
                    self.verify()

    def test_wrong_cpu_even_when_metadata_claims_arm64(self):
        self.contents[release.BINARY] = bytes.fromhex("cffaedfe") + struct.pack("<I", 0x01000007)
        self.metadata["sha256"] = release.sha(self.contents[release.BINARY])
        self.compat["sha256"] = self.metadata["sha256"]
        self.write()
        with self.assertRaisesRegex(ValueError, "Mach-O architecture differs"):
            self.verify()

    def test_executable_permission_lost(self):
        self.write(modes={release.BINARY: 0o644})
        with self.assertRaisesRegex(ValueError, "executable mode differs"):
            self.verify()

    def test_extra_file_and_traversal(self):
        for name in ("unexpected.txt", "../outside", release.APP + "/Contents/extra"):
            with self.subTest(name=name):
                self.write(extra={name: b"unexpected"})
                with self.assertRaisesRegex(ValueError, "unexpected"):
                    self.verify()

    def test_missing_guide(self):
        del self.contents["LOCAL-TEST-GUIDE.txt"]
        self.write()
        with self.assertRaisesRegex(ValueError, "allowlist differs"):
            self.verify()

    def test_changed_binary_with_rehashed_zip(self):
        self.contents[release.BINARY] += b"changed after signing"
        self.write()
        with self.assertRaisesRegex(ValueError, "metadata differs: sha256"):
            self.verify()

    def test_no_boolean_promotion_to_release_ready(self):
        self.compat["release_ready"] = True
        self.write()
        with self.assertRaisesRegex(ValueError, "compatibility report differs: release_ready"):
            self.verify()

    def test_no_manual_acceptance_removal(self):
        self.compat["checks"] = [{"id": "signature", "status": "pass"}]
        self.write()
        with self.assertRaisesRegex(ValueError, "check inventory"):
            self.verify()

    def test_automated_checks_cannot_be_deleted_downgraded_or_duplicated(self):
        original = list(self.compat["checks"])
        mutations = [original[1:], original + [original[0]],
                     [{**row, "status": "manual"} if row["id"] == "codesign_signature" else row
                      for row in original]]
        for checks in mutations:
            with self.subTest(checks=checks):
                self.compat["checks"] = checks
                self.write()
                with self.assertRaisesRegex(ValueError, "compatibility check"):
                    self.verify()

    def test_archive_symlink_is_rejected(self):
        self.write(modes={release.BINARY: stat.S_IFLNK | 0o777})
        with self.assertRaisesRegex(ValueError, "non-regular archive entry"):
            self.verify()

    def test_mismatched_manifest_and_checksum(self):
        self.write()
        _, manifest, checksum = release.names(VERSION, REVISION, "arm64")
        (self.directory / checksum).write_text("forged", encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "checksum sidecar differs"):
            self.verify()
        self.write()
        data = json.loads((self.directory / manifest).read_text())
        data["channel"] = "stable"
        (self.directory / manifest).write_text(json.dumps(data), encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "archive manifest differs"):
            self.verify()

    def test_exact_download_inventory(self):
        self.write()
        (self.directory / "old-archive.zip").write_bytes(b"old run")
        with self.assertRaisesRegex(ValueError, "artifact inventory differs"):
            self.verify()

    def test_identity_parser_and_duplicate_json(self):
        for version in ("v1x0x0", "v1.0.0/escape", "v1.0.0\n", "v1.0.0-rc 1"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                release.names(version, REVISION, "arm64")
        with self.assertRaisesRegex(ValueError, "duplicate JSON key"):
            release.read_json('{"release_ready":false,"release_ready":true}')


if __name__ == "__main__":
    unittest.main()
