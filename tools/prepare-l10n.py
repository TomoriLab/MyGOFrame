#!/usr/bin/env python3
"""Copy validated authoring catalogs into the native UI's embedded resources."""

import fcntl
import json
from pathlib import Path
import re


ROOT = Path(__file__).resolve().parent.parent
DESTINATION = Path("src/internal/desktop/presentation/localization/catalogs_generated")


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"Duplicate localization key: {key}")
        result[key] = value
    return result


def catalogs(root=ROOT):
    result = {}
    for path in sorted((root / "l10n").glob("*.json")):
        if not re.fullmatch(r"[a-z]{2,3}(?:-[A-Za-z0-9]+)*", path.stem):
            raise ValueError(f"Invalid catalog filename: {path.name}")
        data = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique_object)
        if (not isinstance(data, dict) or data.get("@@locale") != path.stem
                or not isinstance(data.get("@@name"), str) or not data["@@name"].strip()):
            raise ValueError(f"Catalog locale or native language name is missing: {path.name}")
        for key, value in data.items():
            if not key.startswith("@") and (not isinstance(value, str) or not value.strip()):
                raise ValueError(f"Empty or non-string translation: {path.name}: {key}")
        result[path.stem] = data
    if not {"en", "ko", "ja"} <= result.keys():
        raise ValueError("The en, ko, and ja catalogs are required.")
    source = {key for key in result["en"] if not key.startswith("@")}
    for tag, data in result.items():
        keys = {key for key in data if not key.startswith("@")}
        if keys != source:
            raise ValueError(f"Catalog keys differ: {tag}: missing={sorted(source - keys)}, extra={sorted(keys - source)}")
    return result


def prepare(root=ROOT):
    (root / "build").mkdir(exist_ok=True)
    with (root / "build/l10n.lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        copy_catalogs(root)


def copy_catalogs(root):
    data = catalogs(root)
    destination = root / DESTINATION
    destination.mkdir(parents=True, exist_ok=True)
    for tag, catalog in data.items():
        output = destination / (tag + ".json")
        content = json.dumps(catalog, ensure_ascii=False, indent=2) + "\n"
        if not output.exists() or output.read_text(encoding="utf-8") != content:
            temporary = output.with_suffix(".json.new")
            temporary.write_text(content, encoding="utf-8")
            temporary.replace(output)
    for path in destination.glob("*.json"):
        if path.stem not in data:
            path.unlink()


if __name__ == "__main__":
    prepare()
