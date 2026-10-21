#!/usr/bin/env python3
"""Create a drag-to-Applications DMG from an independently verified Mac preview ZIP.

This is an additional preview download, not a replacement for the ZIP release
protocol or evidence of Developer ID signing/notarization.
"""
import argparse
import importlib.util
import os
from pathlib import Path
import plistlib
import shutil
import stat
import subprocess
import sys
import tempfile
import time


# Importing the ZIP verifier must not add scripts/__pycache__ to an otherwise
# clean release checkout, including when this entry point is run without -B.
sys.dont_write_bytecode = True
SPEC = importlib.util.spec_from_file_location("macos_release", Path(__file__).with_name("macos-release.py"))
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)
ROOT = Path(__file__).resolve().parent.parent
HDIUTIL = "/usr/bin/hdiutil"


def verify_payload(directory, entries, applications=False, exact=False):
    """Bind the copied/mounted payload to the already validated ZIP manifest."""
    expected = {row["path"] for row in entries}
    for row in entries:
        path = directory / row["path"]
        release.require(path.is_file() and not path.is_symlink(),
                        "missing or linked DMG payload: " + row["path"])
        release.require(release.sha(path.read_bytes()) == row["sha256"],
                        "DMG payload hash differs: " + row["path"])
        release.require(stat.S_IMODE(path.stat().st_mode) == row["mode"],
                        "DMG payload permissions differ: " + row["path"])
    if applications:
        link = directory / "Applications"
        release.require(link.is_symlink() and os.readlink(link) == "/Applications",
                        "DMG Applications shortcut differs")
        expected.add("Applications")
    if exact:
        actual = {path.relative_to(directory).as_posix() for path in directory.rglob("*")
                  if path.is_file() or path.is_symlink()}
        release.require(actual == expected, "DMG payload inventory differs")


def detach(mountpoint):
    try:
        result = subprocess.run([HDIUTIL, "detach", str(mountpoint)], capture_output=True, timeout=30)
    except subprocess.TimeoutExpired:
        result = None
    if result is None or result.returncode != 0:
        # This mountpoint belongs only to the image created by this invocation.
        result = subprocess.run([HDIUTIL, "detach", "-force", str(mountpoint)],
                                capture_output=True, timeout=30)
    release.require(result.returncode == 0, "could not detach the temporary DMG mount")


def create_image(stage, image, volume):
    command = [HDIUTIL, "create", "-srcfolder", str(stage), "-volname", volume,
               "-fs", "HFS+", "-format", "UDZO", str(image)]
    for attempt in range(3):
        # hdiutil create reports EBUSY as exit 1, shared with permanent errors.
        # Keep its diagnostic and retry only the explicit transient failure.
        # Use a file, not a pipe inherited by diskimages-helper.
        with tempfile.TemporaryFile(mode="w+t", encoding="utf-8") as log:
            result = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT,
                                    env=dict(os.environ, LC_ALL="C"), timeout=600)
            log.seek(0)
            diagnostic = log.read()
        print(diagnostic, end="", flush=True)
        if result.returncode == 0:
            return
        if result.returncode != 1 or "Resource busy" not in diagnostic or attempt == 2:
            result.check_returncode()
        # This image is inside this invocation's private staging directory;
        # a failed attempt may leave a partial file that cannot be reused.
        image.unlink(missing_ok=True)
        delay = 2 ** (attempt + 1)
        print("macos_dmg: resource busy; retrying creation in %ss" % delay, flush=True)
        time.sleep(delay)


def package(version, revision, arch, source, output):
    release.require(sys.platform == "darwin", "DMG packaging requires macOS")
    source = source.resolve()
    output = output.resolve()
    release.require(output.is_relative_to(ROOT) and output != ROOT,
                    "DMG output must be below the repository")
    manifest = release.verify(source, version, revision, arch, native=True)
    archive_name = manifest["archive_name"]
    image_name = archive_name[:-4] + ".dmg"
    checksum_name = image_name + ".sha256"
    output.mkdir(parents=True, exist_ok=True)
    image = output / image_name
    checksum = output / checksum_name
    release.require(not image.exists() and not image.is_symlink() and
                    not checksum.exists() and not checksum.is_symlink(),
                    "DMG or checksum already exists; choose a fresh output directory")

    temporary = Path(tempfile.mkdtemp(prefix="macos-dmg-", dir=output.parent))
    stage = temporary / "payload"
    mountpoint = temporary / "mounted"
    stage.mkdir()
    mountpoint.mkdir()
    temporary_image = temporary / image_name
    mounted = False
    image_written = False
    checksum_written = False
    try:
        subprocess.run(["/usr/bin/ditto", "-x", "-k", str(source / archive_name), str(stage)], check=True)
        verify_payload(stage, manifest["files"], exact=True)
        (stage / "Applications").symlink_to("/Applications")
        create_image(stage, temporary_image, "Universal-Code " + version + " " + arch)
        subprocess.run([HDIUTIL, "verify", str(temporary_image)], check=True, timeout=120)
        attached = subprocess.run([HDIUTIL, "attach", "-readonly", "-nobrowse", "-noautoopen",
                                   "-mountpoint", str(mountpoint), "-plist", str(temporary_image)],
                                  check=True, capture_output=True, timeout=60)
        mounted = True
        entities = plistlib.loads(attached.stdout).get("system-entities", [])
        release.require(any(row.get("mount-point") == str(mountpoint) for row in entities),
                        "DMG did not attach at the private mountpoint")
        # HFS+ may add its own volume metadata; all product bytes and POSIX modes
        # must still match the verified ZIP, including the signed Mach-O binary.
        verify_payload(mountpoint, manifest["files"], applications=True)
        subprocess.run(["/usr/bin/codesign", "--verify", "--strict", str(mountpoint / release.APP)],
                       check=True, timeout=60)
        detach(mountpoint)
        mounted = False

        digest = release.sha(temporary_image.read_bytes())
        # Exclusive creation never replaces a previous deliverable, including a
        # path created by another invocation while this image was being built.
        os.link(temporary_image, image)
        image_written = True
        with checksum.open("xb") as handle:
            checksum_written = True
            handle.write((digest + "  " + image_name + "\n").encode("utf-8"))
        return image, digest, checksum
    except BaseException:
        if checksum_written:
            checksum.unlink()
        if image_written:
            image.unlink()
        raise
    finally:
        try:
            # A timed-out attach can have mounted the image before returning.
            if mounted or mountpoint.is_mount():
                detach(mountpoint)
        finally:
            # Never recursively remove a still-mounted filesystem after a
            # failed detach; retain the private staging directory for recovery.
            if not mountpoint.is_mount():
                shutil.rmtree(temporary)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version")
    parser.add_argument("revision")
    parser.add_argument("arch", choices=("arm64", "amd64"))
    parser.add_argument("--source", type=Path, default=ROOT / "build/macos-public")
    parser.add_argument("--output-directory", type=Path, default=ROOT / "build/macos-dmg")
    args = parser.parse_args()
    try:
        image, digest, checksum = package(args.version, args.revision, args.arch,
                                          args.source, args.output_directory)
    except (ValueError, KeyError, OSError, subprocess.SubprocessError, plistlib.InvalidFileException) as exc:
        parser.exit(1, "macos_dmg: %s\n" % exc)
    print("macos_dmg: %s\nmacos_dmg_sha256: %s\nmacos_dmg_checksum: %s\n"
          "macos_dmg_ad_hoc_signed_app: true\nmacos_dmg_notarized: false\nmacos_release_ready: false"
          % (image, digest, checksum))


if __name__ == "__main__":
    main()
