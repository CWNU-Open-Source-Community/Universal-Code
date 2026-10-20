#!/usr/bin/env python3
"""Package and verify the macOS preview, including the bytes inside its ZIP.

The Windows publisher runs the same verifier without executing Mac binaries.
Only a native Mac build can create the archive and verify its extracted bundle.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import plistlib
import re
import shutil
import stat
import struct
import subprocess
import sys
import tempfile
import zipfile


ROOT = Path(__file__).resolve().parent.parent
PROTOCOL = "macos_release_archive.v1"
APP = "TraverseBoard.app"
BINARY = APP + "/Contents/MacOS/cyberagent-desktop"
LAUNCHER = "Start-Prayu-Operator-Preview.command"
FILES = frozenset({
    BINARY, APP + "/Contents/Info.plist", APP + "/Contents/Resources/TraverseBoard.icns",
    APP + "/Contents/_CodeSignature/CodeResources", LAUNCHER, "LOCAL-TEST-GUIDE.txt",
    "release-metadata.json", "macos-compatibility.json", "sbom.json", "NOTICE", "LICENSE",
})
EXECUTABLES = frozenset({BINARY, LAUNCHER})
LIMIT = 768 * 1024 * 1024
AUTOMATED_CHECKS = frozenset({
    "macho_magic", "macho_arch", "macho_deployment_target", "sha256_binding", "release_identity",
    "go_target", "non_installing_boundary", "operator_preview_package", "operator_preview_safe_flags",
    "local_test_guide", "default_ui_language", "go_build_metadata", "codesign_signature",
    "consecutive_build_hash",
})


def require(value, message):
    if not value:
        raise ValueError(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate JSON key: " + key)
        result[key] = value
    return result


def read_json(data):
    return json.loads(data, object_pairs_hook=unique_object)


def names(version, revision, arch):
    require(re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?", version),
            "invalid release version")
    require(re.fullmatch(r"[0-9a-f]{40}", revision), "invalid release revision")
    require(arch in ("arm64", "amd64"), "invalid macOS architecture")
    stem = f"TraverseBoard-{version}-macos-{arch}-{revision[:12]}-preview"
    return stem + ".zip", stem + ".manifest.json", stem + ".sha256"


def inspect_zip(path, version, revision, arch):
    require(path.is_file() and not path.is_symlink() and 0 < path.stat().st_size <= LIMIT,
            "archive missing, linked, empty or oversized")
    contents, entries, seen = {}, [], set()
    directories = {str(parent) for name in FILES for parent in PurePosixPath(name).parents
                   if str(parent) != "."}
    with zipfile.ZipFile(path) as archive:
        infos = archive.infolist()
        require(len(infos) <= len(FILES) + len(directories), "unexpected archive entry count")
        require(sum(info.file_size for info in infos) <= LIMIT, "unpacked archive is oversized")
        for info in infos:
            name = info.filename
            normalized = name.rstrip("/")
            require(normalized.casefold() not in seen, "duplicate archive path")
            seen.add(normalized.casefold())
            require(not info.flag_bits & 1, "encrypted archive entry")
            mode = info.external_attr >> 16
            if info.is_dir():
                require(normalized in directories and stat.S_ISDIR(mode), "unexpected archive directory")
                continue
            require(name in FILES and stat.S_ISREG(mode), "unexpected or non-regular archive entry: " + name)
            require(bool(mode & 0o111) == (name in EXECUTABLES), "archive executable mode differs: " + name)
            require(not mode & 0o7022, "unsafe archive file permissions: " + name)
            data = archive.read(info)
            contents[name] = data
            entries.append({"path": name, "size": len(data), "sha256": sha(data), "mode": mode & 0o777})
    require(set(contents) == FILES, "archive file allowlist differs")
    metadata = read_json(contents["release-metadata.json"])
    for key, expected in {
        "protocol_version": "portable_release_metadata.v1", "app_version": version,
        "revision": revision, "target_os": "darwin", "target_arch": arch,
        "app_bundle_name": APP, "binary_name": "cyberagent-desktop",
        "sha256": sha(contents[BINARY]), "operator_preview_launcher_name": LAUNCHER,
        "operator_preview_launcher_sha256": sha(contents[LAUNCHER]),
        "local_test_guide_name": "LOCAL-TEST-GUIDE.txt",
        "local_test_guide_sha256": sha(contents["LOCAL-TEST-GUIDE.txt"]),
        "modified": False, "reproducibility_checked": True, "reproducible": True,
        "app_bundle_ad_hoc_signed": True, "app_bundle_notarized": False,
        "manual_macos_matrix_required": True,
    }.items():
        require(type(metadata.get(key)) is type(expected) and metadata[key] == expected,
                "release metadata differs: " + key)
    binary = contents[BINARY]
    cpu = {"arm64": 0x0100000C, "amd64": 0x01000007}[arch]
    require(len(binary) >= 8 and binary[:4] == bytes.fromhex("cffaedfe") and
            struct.unpack("<I", binary[4:8])[0] == cpu, "Mach-O architecture differs")
    plist = plistlib.loads(contents[APP + "/Contents/Info.plist"])
    numeric = re.match(r"v([0-9]+\.[0-9]+\.[0-9]+)", version).group(1)
    for key, value in {"CFBundleShortVersionString": numeric, "CFBundleVersion": numeric,
                       "CFBundleIdentifier": "workbench.prayu.desktop",
                       "CFBundleExecutable": "cyberagent-desktop", "LSMinimumSystemVersion": "12.0.0"}.items():
        require(plist.get(key) == value, "bundle identity differs: " + key)
    compat = read_json(contents["macos-compatibility.json"])
    for key, value in {"protocol_version": "macos_portable_compatibility.v1",
                       "sha256": sha(binary), "machine": arch, "app_bundle_name": APP,
                       "automated_checks_passed": True, "release_ready": False,
                       "ad_hoc_signed": True, "notarized": False,
                       "manual_macos_matrix_required": True}.items():
        require(type(compat.get(key)) is type(value) and compat[key] == value,
                "compatibility report differs: " + key)
    checks = compat.get("checks", [])
    require(isinstance(checks, list) and all(isinstance(row, dict) for row in checks),
            "compatibility checks are invalid")
    require(len(checks) == len(AUTOMATED_CHECKS) + 1 and
            {row.get("id") for row in checks} == AUTOMATED_CHECKS | {"macos_matrix"},
            "compatibility check inventory is incomplete or duplicated")
    for row in checks:
        expected = "manual" if row["id"] == "macos_matrix" else "pass"
        require(row.get("status") == expected, "compatibility check status differs: " + row["id"])
    return sorted(entries, key=lambda row: row["path"])


def native_verify(path, entries):
    require(sys.platform == "darwin", "native archive verification requires macOS")
    with tempfile.TemporaryDirectory(prefix="macos-extracted-", dir=path.parent) as folder:
        target = Path(folder)
        subprocess.run(["/usr/bin/ditto", "-x", "-k", str(path), str(target)], check=True)
        for row in entries:
            file = target / row["path"]
            require(file.is_file() and not file.is_symlink() and sha(file.read_bytes()) == row["sha256"],
                    "extracted content differs: " + row["path"])
            require(stat.S_IMODE(file.stat().st_mode) == row["mode"], "extracted permissions differ")
        subprocess.run([str(ROOT / "scripts/check-macos-compat.sh"),
                        "-BinaryPath", str(target / BINARY),
                        "-MetadataPath", str(target / "release-metadata.json"),
                        "-OutputPath", str(target / "extracted-compatibility.json")], check=True)


def verify(directory, version, revision, arch, native=False, require_ready=False):
    # No evidence can turn this preview-only implementation into a stable Mac release.
    require(not require_ready, "Stable macOS publication requires Developer ID signing, notarization and final-package manual acceptance; this pipeline currently produces previews only")
    archive_name, manifest_name, checksum_name = names(version, revision, arch)
    require(directory.is_dir() and not directory.is_symlink(), "artifact directory is invalid")
    require({p.name for p in directory.iterdir()} == {archive_name, manifest_name, checksum_name},
            "artifact inventory differs from exact Mac allowlist")
    for name in (manifest_name, checksum_name):
        file = directory / name
        require(file.is_file() and not file.is_symlink() and file.stat().st_size <= 1024 * 1024,
                "invalid archive sidecar")
    path = directory / archive_name
    entries = inspect_zip(path, version, revision, arch)
    manifest = read_json((directory / manifest_name).read_bytes())
    expected = {
        "protocol_version": PROTOCOL, "version": version, "revision": revision,
        "target_os": "darwin", "target_arch": arch, "channel": "preview",
        "archive_name": archive_name, "archive_sha256": sha(path.read_bytes()),
        "ad_hoc_signed": True, "notarized": False, "release_ready": False, "files": entries,
    }
    require(manifest == expected and all(type(manifest.get(k)) is type(v) for k, v in expected.items()),
            "archive manifest differs from inspected contents or expected identity")
    checksum = f"{expected['archive_sha256']}  {archive_name}\n{sha((directory / manifest_name).read_bytes())}  {manifest_name}\n"
    require((directory / checksum_name).read_text(encoding="utf-8") == checksum, "checksum sidecar differs")
    if native:
        native_verify(path, entries)
    return manifest


def package(source, directory, version, revision, arch):
    require(sys.platform == "darwin", "Mac archives must be packaged and verified on macOS")
    require(directory.resolve().is_relative_to(ROOT) and directory.resolve() != ROOT,
            "Mac package output must be below the repository")
    require(not directory.exists(), "Mac package output already exists; use a fresh directory")
    archive_name, manifest_name, checksum_name = names(version, revision, arch)
    directory.mkdir(parents=True)
    with tempfile.TemporaryDirectory(prefix="macos-stage-", dir=directory.parent) as folder:
        stage = Path(folder)
        # Copy just the allowlisted build products; reject extra files in the app itself.
        app = source / APP
        actual = {p.relative_to(source).as_posix() for p in app.rglob("*") if p.is_file() or p.is_symlink()}
        require(actual == {name for name in FILES if name.startswith(APP + "/")}, "app bundle inventory differs")
        for name in FILES:
            input_file = ROOT / "LICENSE" if name == "LICENSE" else source / name
            require(input_file.is_file() and not input_file.is_symlink(), "missing or linked package input: " + name)
            destination = stage / name
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(input_file, destination)
            os.chmod(destination, 0o755 if name in EXECUTABLES else 0o644)
        path = directory / archive_name
        subprocess.run(["/usr/bin/ditto", "-c", "-k", "--norsrc", str(stage), str(path)], check=True)
    entries = inspect_zip(path, version, revision, arch)
    manifest = {"protocol_version": PROTOCOL, "version": version, "revision": revision,
                "target_os": "darwin", "target_arch": arch, "channel": "preview",
                "archive_name": archive_name, "archive_sha256": sha(path.read_bytes()),
                "ad_hoc_signed": True, "notarized": False, "release_ready": False, "files": entries}
    # Keep the sidecars byte-stable on Windows and compatible with the Python
    # 3.9 bundled with Xcode; Path.write_text(newline=...) requires Python 3.10.
    (directory / manifest_name).write_bytes((json.dumps(manifest, indent=2) + "\n").encode("utf-8"))
    checksum = f"{manifest['archive_sha256']}  {archive_name}\n{sha((directory / manifest_name).read_bytes())}  {manifest_name}\n"
    (directory / checksum_name).write_bytes(checksum.encode("utf-8"))
    verify(directory, version, revision, arch, native=True)
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("package", "verify"))
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--source", type=Path, default=Path("build/desktop"))
    parser.add_argument("--version", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--arch", choices=("arm64", "amd64"), required=True)
    parser.add_argument("--native", action="store_true")
    parser.add_argument("--require-release-ready", action="store_true")
    args = parser.parse_args()
    try:
        if args.command == "package":
            require(not args.require_release_ready, "This packager only creates Mac previews")
            result = package(args.source.resolve(), args.directory.resolve(), args.version, args.revision, args.arch)
        else:
            result = verify(args.directory, args.version, args.revision, args.arch, args.native, args.require_release_ready)
    except (ValueError, KeyError, OSError, zipfile.BadZipFile, subprocess.CalledProcessError) as exc:
        parser.exit(1, f"macos_archive: {exc}\n")
    print(f"macos_archive: {result['archive_name']}\nmacos_archive_sha256: {result['archive_sha256']}\nmacos_release_ready: false")


if __name__ == "__main__":
    main()
