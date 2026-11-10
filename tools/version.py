#!/usr/bin/env python3
"""Manage the application release from version.json and validate derived metadata."""

import argparse
from datetime import date, datetime
from email.utils import format_datetime
import fcntl
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import textwrap
import xml.etree.ElementTree as ElementTree
from xml.sax.saxutils import escape

ROOT = Path(__file__).resolve().parent.parent
SEMANTIC_VERSION_PATTERN = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?")
GENERATED = Path("src/internal/version/domain/version_generated.go")


def read(root=ROOT):
    data = json.loads((root / "version.json").read_text(encoding="utf-8"))
    if set(data) != {"version", "debianRevision", "releaseDate"}:
        raise ValueError("version.json must contain version, debianRevision, and releaseDate.")
    match = SEMANTIC_VERSION_PATTERN.fullmatch(data["version"]) if isinstance(data["version"], str) else None
    if not match or any(value.isdigit() and len(value) > 1 and value.startswith("0")
                        for value in (match[4] or "").split(".")):
        raise ValueError("Application version must be a canonical Semantic Version.")
    if type(data["debianRevision"]) is not int or data["debianRevision"] < 1:
        raise ValueError("Debian revision must be a positive integer.")
    if date.fromisoformat(data["releaseDate"]).isoformat() != data["releaseDate"]:
        raise ValueError("Release date must use YYYY-MM-DD.")
    return data


def debian_version(data):
    return data["version"].replace("-", "~", 1) + "-" + str(data["debianRevision"])


def changelog_time(release_date, reference=None):
    # Retaining the local offset prevents a release just after midnight from receiving a future UTC date.
    reference = reference or datetime.now().astimezone()
    return datetime.combine(date.fromisoformat(release_date), reference.timetz())


def command(arguments, root):
    result = subprocess.run(arguments, cwd=root, text=True, capture_output=True)
    return result.stdout.strip() if result.returncode == 0 else ""


def metadata(root=ROOT):
    data = read(root)
    revision, modified = "", False
    # A source package keeps the checkout provenance even though it has no .git directory.
    if (root / ".git").exists():
        revision = command(["git", "rev-parse", "--verify", "HEAD"], root)
        modified = bool(command(["git", "status", "--porcelain", "--untracked-files=normal"], root))
    elif (root / "build-source.json").exists():
        source = json.loads((root / "build-source.json").read_text())
        revision, modified = source["revision"], source["modified"]
        if not isinstance(revision, str) or (revision and not re.fullmatch(r"[0-9a-f]{40,64}", revision)) or type(modified) is not bool:
            raise ValueError("Invalid source-package provenance.")
    epoch = os.environ.get("SOURCE_DATE_EPOCH")
    if epoch is not None and (not epoch.isdigit() or int(epoch) > 253402300799):
        raise ValueError("SOURCE_DATE_EPOCH must be a nonnegative supported Unix timestamp.")
    return {"version": data["version"], "debianVersion": debian_version(data),
            "revision": revision, "modified": modified,
            "sourceDateEpoch": int(epoch) if epoch is not None else 0}


def write_changed(path, content):
    if not path.exists() or path.read_text(encoding="utf-8") != content:
        temporary = path.with_suffix(path.suffix + ".new")
        temporary.write_text(content, encoding="utf-8")
        temporary.replace(path)


def check(root=ROOT):
    data = read(root)
    expected = data["version"]
    failures = []
    for relative, pattern in [
        ("src/rust/Cargo.toml", r'(?m)^version = "([^"]+)"$'),
        ("src/rust/Cargo.lock", r'(?m)^name = "mygoframe_media"\nversion = "([^"]+)"$'),
        ("debian/mygoframe.1", r'"MyGOFrame ([^"]+)"'),
    ]:
        match = re.search(pattern, (root / relative).read_text())
        if not match or match[1] != expected:
            failures.append(f"{relative}: expected application version {expected}")
    changelog = (root / "debian/changelog").read_text()
    if not changelog.startswith(f"mygoframe ({debian_version(data)}) "):
        failures.append(f"debian/changelog: expected Debian version {debian_version(data)}")
    release = ElementTree.parse(root / "debian/mygoframe.metainfo.xml").find("releases/release")
    if release is None or release.get("version") != expected or release.get("date") != data["releaseDate"]:
        failures.append("debian/mygoframe.metainfo.xml: latest release differs from version.json")
    if failures:
        raise ValueError("\n".join(failures) + "\nRun tools/version.py sync or set/bump with --note.")
    return data


def synchronize(root=ROOT, notes=()):
    data = read(root)
    version = data["version"]
    changelog_path = root / "debian/changelog"
    changelog = changelog_path.read_text()
    additions = {}
    if not changelog.startswith(f"mygoframe ({debian_version(data)}) "):
        if not notes:
            raise ValueError("A new Debian changelog entry requires at least one --note.")
        maintainer_match = re.search(r"(?m)^Maintainer: (.+)$", (root / "debian/control").read_text())
        if not maintainer_match:
            raise ValueError("debian/control has no Maintainer field.")
        release_time = changelog_time(data["releaseDate"])
        body = "\n".join(textwrap.fill(note, width=76, initial_indent="  * ", subsequent_indent="    ") for note in notes)
        additions[changelog_path] = (f"mygoframe ({debian_version(data)}) unstable; urgency=medium\n\n{body}\n\n"
                                    f" -- {maintainer_match[1]}  {format_datetime(release_time)}\n\n{changelog}")
    for relative, pattern, replacement in [
        ("src/rust/Cargo.toml", r'(?m)^version = "[^"]+"$', f'version = "{version}"'),
        ("src/rust/Cargo.lock", r'(?m)^(name = "mygoframe_media"\n)version = "[^"]+"$', r'\g<1>version = "'+version+'"'),
        ("debian/mygoframe.1", r'^\.TH MYGOFRAME[^\n]*', f'.TH MYGOFRAME 1 "{data["releaseDate"]}" "MyGOFrame {version}" "User Commands"'),
    ]:
        path = root / relative
        content, count = re.subn(pattern, replacement, path.read_text(), count=1)
        if count != 1:
            raise ValueError(f"Cannot locate version in {relative}.")
        additions[path] = content
    path = root / "debian/mygoframe.metainfo.xml"
    content = path.read_text()
    first = ElementTree.fromstring(content).find("releases/release")
    if first is None:
        raise ValueError("AppStream metadata has no release history.")
    if first.get("version") != version:
        if not notes:
            raise ValueError("A new AppStream release requires at least one --note.")
        paragraphs = "\n".join("        <p>"+escape(note)+"</p>" for note in notes)
        entry = (f'\n    <release version="{version}" date="{data["releaseDate"]}" type="development">\n'
                 f'      <description>\n{paragraphs}\n      </description>\n    </release>')
        content = content.replace("<releases>", "<releases>"+entry, 1)
    else:
        content = re.sub(r'(<release\s+version="'+re.escape(version)+r'"\s+date=")[^"]+',
                         r'\g<1>'+data["releaseDate"], content, count=1)
    additions[path] = content
    originals = {path: path.read_text() for path in additions}
    try:
        for path, content in additions.items():
            write_changed(path, content)
        return check(root)
    except BaseException:
        for path, content in originals.items():
            write_changed(path, content)
        raise


def generate(root=ROOT):
    check(root)
    data = metadata(root)
    directory = root / "build"
    directory.mkdir(exist_ok=True)
    with (directory / "version.lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        path = root / GENERATED
        path.parent.mkdir(parents=True, exist_ok=True)
        content = ('// Code generated by tools/version.py; DO NOT EDIT.\npackage domain\n\n'
                   'func Current() Information {\nreturn Information{\n'
                   f'Version: {json.dumps(data["version"])}, DebianVersion: {json.dumps(data["debianVersion"])},\n'
                   f'Revision: {json.dumps(data["revision"])}, Modified: {str(data["modified"]).lower()},\n'
                   f'SourceDateEpoch: {data["sourceDateEpoch"]},\n'+'}\n}\n')
        write_changed(path, content)
    return data


def set_release(version, revision, release_date, notes, root=ROOT):
    path = root / "version.json"
    previous = path.read_text()
    data = {"version": version, "debianRevision": revision, "releaseDate": release_date}
    try:
        write_changed(path, json.dumps(data, indent=2) + "\n")
        read(root)
        synchronize(root, notes)
    except BaseException:
        write_changed(path, previous)
        raise
    return data


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="action", required=True)
    for name in ("show", "debian", "metadata", "check", "generate"):
        commands.add_parser(name)
    sync = commands.add_parser("sync")
    sync.add_argument("--note", action="append", default=[])
    for name in ("set", "bump"):
        command_parser = commands.add_parser(name)
        command_parser.add_argument("version" if name == "set" else "part", choices=None if name == "set" else ["major", "minor", "patch", "packaging"])
        command_parser.add_argument("--debian-revision", type=int)
        command_parser.add_argument("--date", default=date.today().isoformat())
        command_parser.add_argument("--note", action="append", required=True)
    arguments = parser.parse_args()
    if arguments.action == "show":
        print(read()["version"])
    elif arguments.action == "debian":
        print(debian_version(read()))
    elif arguments.action == "metadata":
        print(json.dumps(metadata(), sort_keys=True))
    elif arguments.action == "check":
        check()
        print("Release versions are synchronized.")
    elif arguments.action == "generate":
        generate()
    elif arguments.action == "sync":
        synchronize(notes=arguments.note)
    else:
        data = read()
        if arguments.action == "set":
            version = arguments.version
        else:
            parts = list(map(int, SEMANTIC_VERSION_PATTERN.fullmatch(data["version"]).group(1, 2, 3)))
            if arguments.part == "packaging":
                version = data["version"]
            else:
                index = ["major", "minor", "patch"].index(arguments.part)
                parts[index] += 1
                parts[index+1:] = [0] * (2-index)
                version = ".".join(map(str, parts))
        revision = arguments.debian_revision
        if revision is None:
            revision = data["debianRevision"]+1 if version == data["version"] else 1
        set_release(version, revision, arguments.date, arguments.note)
        print(f"Application {version}; Debian {debian_version(read())}")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError) as error:
        sys.exit(str(error))
