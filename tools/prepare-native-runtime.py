#!/usr/bin/env python3
"""Build pinned application-private native libraries and GTK modules."""

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request


ROOT = Path(__file__).resolve().parent.parent
LOCK = ROOT / "native/runtime-lock.json"


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def command(arguments, directory=None, environment=None):
    return subprocess.check_output(
        arguments, cwd=directory, env=environment, text=True, stderr=subprocess.STDOUT
    ).strip()


def read_lock(path=LOCK):
    result = json.loads(Path(path).read_text())
    if result.get("schemaVersion") != 1 or set(result.get("libraries", {})) != {"gtk", "gdk", "fcitx"}:
        raise ValueError("The native runtime lock schema is invalid.")
    for name, library in result["libraries"].items():
        owner = library.get("buildOwner", name)
        if owner not in result["libraries"] or result["libraries"][owner].get("buildOwner", owner) != owner:
            raise ValueError("A native library must refer to a direct build owner.")
        if not re.fullmatch("[0-9a-f]{64}", library["exportsSha256"]):
            raise ValueError("A native runtime checksum is invalid.")
    modules = result.get("gtkModules", {})
    expected_modules = {
        "colorreload": "gtk-3.0/modules/libcolorreload-gtk-module.so",
        "windowDecorations": "gtk-3.0/modules/libwindow-decorations-gtk-module.so",
    }
    if bool(modules) != bool(result.get("gtkModuleBuild")) or (modules and set(modules) != set(expected_modules)):
        raise ValueError("The native GTK module lock must describe both approved modules and their source.")
    for name, module in modules.items():
        if module.get("file") != expected_modules[name] or module.get("buildOwner") != "kdeGtkModules":
            raise ValueError("A native GTK module has an invalid file or build owner.")
        if not re.fullmatch("[0-9a-f]{64}", module["exportsSha256"]):
            raise ValueError("A native runtime checksum is invalid.")
    for library in source_components(result).values():
        source = library["source"]
        if Path(source["archive"]).name != source["archive"]:
            raise ValueError("A native source archive must have a plain filename.")
        for checksum in [source["sha256"]]:
            if not re.fullmatch("[0-9a-f]{64}", checksum):
                raise ValueError("A native runtime checksum is invalid.")
    return result


def source_components(lock):
    result = {
        name: library for name, library in lock["libraries"].items()
        if library.get("buildOwner", name) == name
    }
    if lock.get("gtkModuleBuild"):
        result["kdeGtkModules"] = lock["gtkModuleBuild"]
    return result


def source_archive(source, destination, offline=False):
    destination = Path(destination)
    destination.mkdir(parents=True, exist_ok=True)
    target = destination / source["archive"]
    candidates = [target, ROOT / "native-inputs" / target.name, ROOT / "build/native-inputs" / target.name]
    for candidate in candidates:
        if candidate.is_file():
            if digest(candidate) != source["sha256"]:
                raise ValueError(f"Native source checksum mismatch: {candidate.name}")
            if candidate.resolve() != target.resolve():
                shutil.copyfile(candidate, target)
            return target
    if offline:
        raise ValueError(f"Missing offline native source archive: {target.name}")
    temporary = target.with_suffix(target.suffix + ".partial")
    try:
        with urllib.request.urlopen(source["url"], timeout=60) as response, temporary.open("wb") as output:
            shutil.copyfileobj(response, output)
        if digest(temporary) != source["sha256"]:
            raise ValueError(f"Downloaded native source checksum mismatch: {target.name}")
        temporary.replace(target)
    finally:
        temporary.unlink(missing_ok=True)
    return target


def extract_source(archive, destination, expected_directory):
    destination = Path(destination)
    destination.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive) as source:
        source.extractall(destination, filter="data")
    extracted = destination / expected_directory
    if not extracted.is_dir():
        raise ValueError("The native source archive has an unexpected root directory.")
    return extracted


def exports(path):
    output = command(["nm", "--dynamic", "--defined-only", str(path)])
    names = sorted({line.split()[-1] for line in output.splitlines() if len(line.split()) >= 3})
    return len(names), hashlib.sha256(("\n".join(names) + "\n").encode()).hexdigest()


def verify_library(path, specification):
    dynamic = command(["readelf", "--dynamic", str(path)])
    match = re.search(r"\(SONAME\).*\[([^\]]+)\]", dynamic)
    if not match or match.group(1) != specification["soname"]:
        raise ValueError(f"Unexpected native library SONAME: {path.name}")
    count, checksum = exports(path)
    if count != specification["exportsCount"] or checksum != specification["exportsSha256"]:
        raise ValueError(f"Native library exported symbols do not match the pinned ABI: {path.name}")
    if "(RPATH)" in dynamic or "(RUNPATH)" in dynamic:
        raise ValueError(f"The private native runtime must not contain a loader search path: {path.name}")
    return count, checksum


def verify_module(path, specification):
    count, checksum = exports(path)
    if count != specification["exportsCount"] or checksum != specification["exportsSha256"]:
        raise ValueError(f"Native GTK module exports do not match the pinned interface: {path.name}")
    dynamic = command(["readelf", "--dynamic", str(path)])
    if "(RPATH)" in dynamic or "(RUNPATH)" in dynamic:
        raise ValueError(f"The private GTK module must not contain a loader search path: {path.name}")
    return count, checksum


def module_path(directory, filename):
    relative = Path(filename)
    if relative.is_absolute() or not relative.parts or ".." in relative.parts:
        raise ValueError("A private GTK module must have a contained relative path.")
    path = Path(directory) / relative
    if not path.resolve().is_relative_to(Path(directory).resolve()):
        raise ValueError("A private GTK module escapes its manifest directory.")
    return path


def system_library(directory, soname, version):
    path = Path(directory) / soname
    if not path.is_file():
        return {"soname": soname, "version": version, "packageVersion": "", "sha256": ""}
    owner = subprocess.run(["dpkg-query", "-S", str(path)], text=True, capture_output=True)
    package_version = ""
    if owner.returncode == 0:
        package = owner.stdout.split(": ", 1)[0]
        package_version = command(["dpkg-query", "-W", "-f=${Version}", package])
    return {"soname": soname, "version": version, "packageVersion": package_version, "sha256": digest(path)}


def patch_sources(source, specification):
    for patch in specification["patches"]:
        path = ROOT / patch["file"]
        if path.is_absolute() and not path.resolve().is_relative_to(ROOT):
            raise ValueError("A native patch path escapes the source tree.")
        if digest(path) != patch["sha256"]:
            raise ValueError(f"Native source patch checksum mismatch: {path.name}")
        subprocess.run(["patch", "--batch", "--fuzz=0", "-p1", "-i", str(path)], cwd=source, check=True)


def refresh_manifest(directory):
    directory = Path(directory)
    path = directory / "manifest.json"
    manifest = json.loads(path.read_text())
    if manifest.get("schemaVersion") != 1:
        raise ValueError("The native runtime manifest schema is invalid.")
    for specification in manifest["libraries"].values():
        filename = specification["file"]
        if Path(filename).name != filename:
            raise ValueError("A private native runtime library must have a relative plain filename.")
        library = directory / filename
        if not library.resolve().is_relative_to(directory.resolve()):
            raise ValueError("A private native runtime library escapes its manifest directory.")
        verify_library(library, specification)
        specification["sha256"] = digest(library)
    for specification in manifest.get("gtkModules", {}).values():
        module = module_path(directory, specification["file"])
        verify_module(module, specification)
        specification["sha256"] = digest(module)
    with tempfile.NamedTemporaryFile(mode="w", dir=directory, prefix=".manifest-", delete=False) as stream:
        temporary = Path(stream.name)
        json.dump(manifest, stream, indent=2, sort_keys=True)
        stream.write("\n")
    temporary.chmod(0o644)
    temporary.replace(path)


def publish_runtime(staged, output, manifest):
    staged = Path(staged)
    output = Path(output)
    output.mkdir(parents=True, exist_ok=True)
    # Renames preserve old mapped inodes while a running GUI uses its libraries.
    for specification in [*manifest["libraries"].values(), *manifest.get("gtkModules", {}).values()]:
        destination = output / specification["file"]
        destination.parent.mkdir(parents=True, exist_ok=True)
        (staged / specification["file"]).replace(destination)
    (staged / "manifest.json").replace(output / "manifest.json")
    shutil.rmtree(staged)


def cached_runtime_valid(existing, fingerprint, output, lock):
    if existing.get("fingerprint") != fingerprint or set(existing.get("libraries", {})) != set(lock["libraries"]):
        return False
    for category in ("libraries", "gtkModules"):
        if set(existing.get(category, {})) != set(lock.get(category, {})):
            return False
        for name, expected in lock.get(category, {}).items():
            component = existing[category][name]
            filename = expected["soname"] if category == "libraries" else expected["file"]
            if component.get("file") != filename or component.get("exportsSha256") != expected["exportsSha256"]:
                return False
            path = output / component["file"]
            if not path.is_file() or digest(path) != component.get("sha256"):
                return False
    source_bundle = ROOT / "build/native-runtime-source"
    if not (source_bundle / "tools/prepare-native-runtime.py").is_file():
        return False
    if digest(source_bundle / "tools/prepare-native-runtime.py") != digest(__file__):
        return False
    for native_file in (ROOT / "native").rglob("*"):
        if native_file.is_file():
            copied = source_bundle / native_file.relative_to(ROOT)
            if not copied.is_file() or digest(copied) != digest(native_file):
                return False
    for component in source_components(lock).values():
        copied = source_bundle / "native-inputs" / component["source"]["archive"]
        if not copied.is_file() or digest(copied) != component["source"]["sha256"]:
            return False
    return True


def build_runtime(lock, inputs, output, jobs):
    gtk = lock["libraries"]["gtk"]
    gtk_version = command(["pkg-config", "--modversion", "gtk+-3.0"])
    if gtk_version != gtk["version"]:
        raise ValueError(f"The private GDK build requires system GTK {gtk['version']}; found {gtk_version}.")
    multiarch = command(["dpkg-architecture", "-qDEB_HOST_MULTIARCH"])
    architecture = command(["dpkg-architecture", "-qDEB_HOST_ARCH"])
    if architecture != command(["dpkg-architecture", "-qDEB_BUILD_ARCH"]):
        raise ValueError("Cross compilation of the private native runtime is not configured.")
    library_directory = command(["pkg-config", "--variable=libdir", "gtk+-3.0"])
    compatibility = {
        "gtk": system_library(library_directory, "libgtk-3.so.0", gtk_version),
        "fcitx": system_library(library_directory, "libFcitx5GClient.so.2", lock["libraries"]["fcitx"]["version"]),
    }
    if not compatibility["gtk"]["sha256"]:
        raise ValueError("The matching system GTK library was not found.")
    for name in ("gtk", "gdk"):
        specification = lock["libraries"][name]
        verify_library(Path(library_directory) / specification["soname"], specification)
    environment = os.environ.copy()
    environment.update(CC=os.environ.get("NATIVE_CC", "cc"), CXX=os.environ.get("NATIVE_CXX", "c++"))
    environment.setdefault("SOURCE_DATE_EPOCH", "0")
    for name in ("CFLAGS", "CXXFLAGS"):
        environment[name] = environment.get(name, "-O2 -g -fno-omit-frame-pointer") + " " + " ".join(
            shlex.quote(f"-{option}={ROOT}=.") for option in ("ffile-prefix-map", "fdebug-prefix-map")
        )
    fingerprint_data = {
        "lock": digest(LOCK), "helper": digest(__file__), "architecture": architecture,
        "multiarch": multiarch, "compatibility": compatibility,
        "compiler": command([environment["CC"], "--version"]),
        "cxxCompiler": command([environment["CXX"], "--version"]),
        "meson": command(["meson", "--version"]), "cmake": command(["cmake", "--version"]),
        "patchelf": command(["patchelf", "--version"]),
        "flags": {name: environment.get(name, "") for name in ("CFLAGS", "CXXFLAGS", "CPPFLAGS", "LDFLAGS")},
        "sourceDateEpoch": environment["SOURCE_DATE_EPOCH"],
        "dependencies": command(["pkg-config", "--modversion", "glib-2.0", "cairo", "pango", "epoxy", "wayland-client", "wayland-protocols", "xkbcommon"]),
        "nativeFiles": {str(path.relative_to(ROOT)): digest(path) for path in sorted((ROOT / "native").rglob("*")) if path.is_file()},
    }
    fingerprint = hashlib.sha256(json.dumps(fingerprint_data, sort_keys=True).encode()).hexdigest()
    manifest_path = output / "manifest.json"
    if manifest_path.is_file():
        existing = json.loads(manifest_path.read_text())
        if cached_runtime_valid(existing, fingerprint, output, lock):
            return

    work = ROOT / "build/native-runtime-build"
    if work.exists():
        shutil.rmtree(work)
    work.mkdir(parents=True)
    output.mkdir(parents=True, exist_ok=True)
    staged_output = Path(tempfile.mkdtemp(prefix="native-runtime-stage-", dir=ROOT / "build"))
    source_bundle = ROOT / "build/native-runtime-source"
    if source_bundle.exists():
        shutil.rmtree(source_bundle)
    (source_bundle / "native-inputs").mkdir(parents=True)
    (source_bundle / "tools").mkdir()
    shutil.copyfile(__file__, source_bundle / "tools/prepare-native-runtime.py")
    shutil.copytree(ROOT / "native", source_bundle / "native")
    manifest = {
        "schemaVersion": 1, "architecture": architecture, "multiarch": multiarch,
        "fingerprint": fingerprint, "systemCompatibility": compatibility,
        "libraries": {}, "sources": [],
    }
    if lock.get("gtkModules"):
        manifest["gtkModules"] = {}
    for name, specification in source_components(lock).items():
        artifacts = {
            artifact: description for artifact, description in lock["libraries"].items()
            if description.get("buildOwner", artifact) == name
        }
        artifacts.update({
            artifact: description for artifact, description in lock.get("gtkModules", {}).items()
            if description["buildOwner"] == name
        })
        archive = source_archive(specification["source"], inputs, offline=True)
        shutil.copyfile(archive, source_bundle / "native-inputs" / archive.name)
        source = extract_source(archive, work / name, specification["source"]["directory"])
        patch_sources(source, specification)
        build = work / (name + "-build")
        if name == "kdeGtkModules":
            build.mkdir()
            module_flags = shlex.split(command(["pkg-config", "--cflags", "--libs", "gtk+-3.0", "gmodule-2.0"]))
            commands = [
                [environment["CC"], *shlex.split(environment.get("CPPFLAGS", "")),
                 *shlex.split(environment["CFLAGS"]), "-shared", "-fPIC", "-fvisibility=hidden",
                 str(source / description["sourceFile"]), "-o", str(build / description["builtLibrary"]),
                 *shlex.split(environment.get("LDFLAGS", "")), *module_flags]
                for description in artifacts.values()
            ]
        elif name == "gdk":
            options = [f"-D{key}={value}" for key, value in specification["mesonOptions"].items()]
            arguments = ["meson", "setup", str(build), str(source), "--wrap-mode=nodownload", "--prefix=/usr", f"--libdir=lib/{multiarch}", *options]
            targets = [description["buildTarget"] for description in artifacts.values()]
            commands = [arguments, ["ninja", "-C", str(build), f"-j{jobs}", *targets]]
        else:
            options = [f"-D{key}={value}" for key, value in specification["cmakeOptions"].items()]
            commands = [["cmake", "-S", str(source), "-B", str(build), "-G", "Ninja", "-DCMAKE_BUILD_TYPE=RelWithDebInfo", "-DCMAKE_SKIP_RPATH=ON", *options], ["cmake", "--build", str(build), "--parallel", str(jobs), "--target", "Fcitx5GClient"]]
        with (work / (name + "-build.log")).open("w") as log:
            for arguments in commands:
                subprocess.run(arguments, env=environment, stdout=log, stderr=subprocess.STDOUT, check=True)
        for artifact, description in artifacts.items():
            built = build / description["builtLibrary"]
            is_module = artifact in lock.get("gtkModules", {})
            filename = description["file"] if is_module else description["soname"]
            destination = staged_output / filename
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(built.resolve(), destination)
            if artifact == "gtk":
                # Meson uses the build-tree GDK path before its normal install step.
                subprocess.run(["patchelf", "--remove-rpath", str(destination)], check=True)
            (verify_module if is_module else verify_library)(destination, description)
            destination.chmod(0o644)
            record = {
                "file": filename, "version": description["version"],
                "sha256": digest(destination), "exportsCount": description["exportsCount"], "exportsSha256": description["exportsSha256"],
            }
            if not is_module:
                record["soname"] = description["soname"]
            manifest["gtkModules" if is_module else "libraries"][artifact] = record
        manifest["sources"].append({
            "name": name, "version": specification["version"], "archive": archive.name,
            "sha256": specification["source"]["sha256"],
            "patches": [{"file": item["file"], "sha256": item["sha256"]} for item in specification["patches"]],
        })
    staged_manifest = staged_output / "manifest.json"
    staged_manifest.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    staged_manifest.chmod(0o644)
    (source_bundle / "README").write_text(
        "This directory contains the exact native library sources, local patches, full copyright and license notices, and build helper.\n"
        "Install the build dependencies listed in the accompanying MyGOFrame native-runtime-build documentation.\n"
        "Run python3 tools/prepare-native-runtime.py --offline to rebuild against the matching system GTK version.\n"
        "The runtime manifest can be regenerated for modified compatible libraries; the application supports an explicit private runtime directory.\n"
    )
    publish_runtime(staged_output, output, manifest)
    print(f"Prepared private GTK, GDK, and Fcitx runtimes in {output}", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inputs-directory", type=Path, default=ROOT / "build/native-inputs")
    parser.add_argument("--output", type=Path, default=ROOT / "build/native-runtime")
    parser.add_argument("--offline", action="store_true")
    parser.add_argument("--download-only", action="store_true")
    parser.add_argument("--refresh-manifest", type=Path)
    parser.add_argument("--jobs", type=int, default=2)
    arguments = parser.parse_args()
    if arguments.refresh_manifest:
        refresh_manifest(arguments.refresh_manifest)
        return
    if arguments.jobs < 1:
        raise ValueError("The native build job count must be positive.")
    lock = read_lock()
    (ROOT / "build").mkdir(exist_ok=True)
    with (ROOT / "build/native-runtime-prepare.lock").open("a") as stream:
        fcntl.flock(stream, fcntl.LOCK_EX)
        for library in source_components(lock).values():
            source_archive(library["source"], arguments.inputs_directory, arguments.offline)
        if not arguments.download_only:
            build_runtime(lock, arguments.inputs_directory.resolve(), arguments.output.resolve(), arguments.jobs)


if __name__ == "__main__":
    main()
