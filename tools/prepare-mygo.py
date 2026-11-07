#!/usr/bin/env python3
"""Prepare pinned MyGo renderer changes without storing dependency sources in Git."""

import hashlib
import fcntl
import json
import os
from pathlib import Path
import shutil
import subprocess


def _prepare(go="go"):
    root = Path(__file__).resolve().parent.parent
    patches = sorted((root / "patches/mygo").glob("*.patch"))
    if not patches:
        raise SystemExit("The pinned MyGo renderer patch is missing.")
    vendor = root / "vendor/github.com/egoist/mygo"
    vendored = vendor.is_dir()
    information = json.loads(subprocess.check_output(
        [go, "list", "-mod=vendor" if vendored else "-mod=mod", "-m", "-json",
         "github.com/egoist/mygo"], cwd=root, text=True
    ))
    if information.get("Version") != "v0.8.1" or information.get("Replace"):
        raise SystemExit("The renderer patch requires the official MyGo v0.8.1 module.")
    module = vendor if vendored else Path(information["Dir"])
    affected = set()
    for patch in patches:
        for line in patch.read_text().splitlines():
            if not line.startswith(("--- ", "+++ ")):
                continue
            name = line[4:].split("\t", 1)[0]
            if name == "/dev/null":
                continue
            if name.startswith(("a/", "b/")):
                name = name[2:]
            path = Path(name)
            if path.is_absolute() or ".." in path.parts:
                raise SystemExit(f"Invalid renderer patch path: {name}")
            affected.add(path)

    fingerprint = hashlib.sha256(str(module.resolve()).encode())
    fingerprint.update(Path(__file__).read_bytes())
    fingerprint.update(str(root).encode())
    fingerprint.update((root / "go.mod").read_bytes())
    fingerprint.update((root / "go.sum").read_bytes())
    if vendored:
        fingerprint.update((root / "vendor/modules.txt").read_bytes())
    for patch in patches:
        fingerprint.update(patch.read_bytes())
    for name in sorted(affected):
        original = module / name
        fingerprint.update(str(name).encode())
        fingerprint.update(original.read_bytes() if original.is_file() else b"missing")
    build = root / "build"
    build.mkdir(exist_ok=True)
    manifest = build / "mygo-build.json"
    if manifest.is_file():
        existing = json.loads(manifest.read_text())
        if (existing.get("fingerprint") == fingerprint.hexdigest()
                and existing.get("required")
                and all(Path(name).is_file() for name in existing["required"])):
            return existing["flags"]

    destination = build / ("mygo-overlay-source" if vendored else "mygo-source")
    if destination.exists():
        shutil.rmtree(destination)
    if vendored:
        destination.mkdir(parents=True)
        # A module boundary keeps application package patterns out of this sparse tree.
        (destination / "go.mod").write_text("module github.com/egoist/mygo\n")
        for name in sorted(affected):
            target = destination / name
            target.parent.mkdir(parents=True, exist_ok=True)
            original = module / name
            if original.is_file():
                shutil.copyfile(original, target)
    else:
        shutil.copytree(module, destination)
        for directory, _, names in os.walk(destination):
            Path(directory).chmod(0o755)
            for name in names:
                path = Path(directory) / name
                path.chmod(path.stat().st_mode | 0o200)
    for patch in patches:
        subprocess.run(
            ["patch", "--batch", "--fuzz=0", "-p1", "-i", str(patch)],
            cwd=destination, check=True, stdout=subprocess.DEVNULL
        )
    if vendored:
        overlay = build / "mygo-overlay.json"
        mapping = {
            str((module / name).resolve()):
            str((destination / name).resolve()) if (destination / name).is_file() else ""
            for name in sorted(affected)
        }
        overlay.write_text(json.dumps({"Replace": mapping}, indent=2) + "\n")
        flags = ["-overlay=" + str(overlay)]
    else:
        alternate = build / "mygo.mod"
        alternate.write_text((root / "go.mod").read_text() +
                             "\nreplace github.com/egoist/mygo => " + json.dumps(str(destination)) + "\n")
        shutil.copyfile(root / "go.sum", build / "mygo.sum")
        flags = ["-modfile=" + str(alternate)]
    required = [str(destination / name) for name in affected if (destination / name).is_file()]
    required.append(str(destination / "go.mod"))
    required += [flag.split("=", 1)[1] for flag in flags]
    manifest.write_text(json.dumps({"fingerprint": fingerprint.hexdigest(), "flags": flags,
                                    "required": required}, indent=2) + "\n")
    print(f"Prepared the MyGo renderer patch in the build directory ({len(affected)} files).", flush=True)
    return flags


def prepare(go="go"):
    root = Path(__file__).resolve().parent.parent
    (root / "build").mkdir(exist_ok=True)
    with (root / "build/mygo-prepare.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        return _prepare(go)


if __name__ == "__main__":
    prepare(os.environ.get("GO", "go"))
