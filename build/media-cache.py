#!/usr/bin/env python3
"""Identify and verify reusable native media bundles; never cache CLI packages."""
import hashlib
import json
import os
import re
from pathlib import Path
import sys

INPUTS = ("build/ffmpeg.sh", "build/libvpl-mingw.patch", "build/media-cache.py",
          "build/media-toolchain.sh", "build/windows-toolchain.cmake",
          "build/windows-media.Dockerfile", ".github/workflows/media-bundle.yml")


def digest(path):
    value = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


def identity(root, toolchain, environment):
    fields = {key: environment[key] for key in ("RUNNER_OS", "RUNNER_ARCH", "ImageOS", "ImageVersion", "MEDIA_TARGET")}
    if any(not value for value in fields.values()) or not toolchain.strip():
        raise ValueError("builder/target/toolchain identity is missing")
    fields["build_environment"] = {key: environment.get(key, "") for key in (
        "CC", "CXX", "CFLAGS", "CXXFLAGS", "CPPFLAGS", "LDFLAGS", "AR", "RANLIB",
        "MAKEFLAGS", "PKG_CONFIG_PATH", "PKG_CONFIG_LIBDIR", "CMAKE_TOOLCHAIN_FILE",
        "SDKROOT", "MACOSX_DEPLOYMENT_TARGET")}
    fields["toolchain"] = toolchain
    fields["inputs"] = {name: digest(root / name) for name in INPUTS}
    return "media-v2-" + hashlib.sha256(json.dumps(fields, sort_keys=True).encode()).hexdigest()


def inventory(bundle):
    if bundle.is_symlink() or not bundle.is_dir():
        raise ValueError("bundle directory is missing or linked")
    files = {}
    for path in sorted(bundle.rglob("*")):
        if path.is_symlink():
            raise ValueError("bundle contains a symbolic link")
        if path.is_dir():
            continue
        if not path.is_file():
            raise ValueError("bundle contains a non-regular file")
        files[path.relative_to(bundle).as_posix()] = digest(path)
    if not files or not all(any(name.startswith(prefix) for name in files) for prefix in ("ffmpeg/", "source/", "licenses/")):
        raise ValueError("bundle binaries, sources or licenses are missing")
    return files


def seal(directory, key):
    manifest = {"input_key": key, "files": inventory(directory / "bundle")}
    (directory / "cache-manifest.json").write_text(json.dumps(manifest, sort_keys=True) + "\n")


def verify(directory, key):
    manifest_path = directory / "cache-manifest.json"
    if manifest_path.is_symlink():
        raise ValueError("manifest is linked")
    manifest = json.loads(manifest_path.read_text())
    if manifest != {"input_key": key, "files": inventory(directory / "bundle")}:
        raise ValueError("cached bundle identity or contents differ")


def handoff(directory, key, sha, target, check=False):
    if not re.fullmatch(r"[0-9a-f]{40}", sha) or target != "windows-amd64":
        raise ValueError("invalid source commit or Windows target")
    verify(directory, key)
    expected = {"source_sha": sha, "target": target, "input_key": key}
    path = directory / "handoff.json"
    if path.is_symlink():
        raise ValueError("linked handoff metadata")
    if check:
        if json.loads(path.read_text()) != expected:
            raise ValueError("producer source, target or input identity differs")
    else:
        path.write_text(json.dumps(expected, sort_keys=True) + "\n")


def main():
    operation, directory = sys.argv[1:3]
    if operation == "key":
        print(identity(Path(__file__).resolve().parent.parent, Path(directory).read_text(), os.environ))
    elif operation == "seal":
        seal(Path(directory), sys.argv[3])
    elif operation in ("handoff", "verify-handoff"):
        handoff(Path(directory), *sys.argv[3:6], check=operation == "verify-handoff")
    elif operation == "verify":
        verify(Path(directory), sys.argv[3])
    else:
        raise ValueError("unknown media cache operation")


if __name__ == "__main__":
    main()
