#!/usr/bin/env python3
"""Check domain boundaries without loading native libraries or third-party tools."""

import argparse
import json
from pathlib import Path
import re


ROOT = Path(__file__).resolve().parent.parent
MODULE = "github.com/TomoriLab/MyGOFrame/src/internal/"


def go_imports(text):
    blocks = re.findall(r"(?m)^import\s*\((.*?)\)", text, re.DOTALL)
    blocks += re.findall(r'(?m)^import\s+((?:[\w.]+\s+)?"[^"]+")', text)
    return re.findall(r'"([^"]+)"', "\n".join(blocks))


def source_code(text):
    # Dependencies in comments and diagnostic strings do not create source edges.
    return re.sub(r'//[^\n]*|/\*.*?\*/|"(?:\\.|[^"\\])*"', " ", text, flags=re.DOTALL)


def check_file(path, text, swift_adapters=()):
    parts = path.parts
    layer = next((name for name in ("domain", "application", "presentation", "infrastructure") if name in parts), None)
    errors = []
    if path.suffix == ".go":
        for dependency in go_imports(text):
            if dependency.startswith(MODULE):
                destination = dependency[len(MODULE):].split("/")
                forbidden = {"domain": {"application", "infrastructure", "presentation", "bootstrap"},
                             "application": {"infrastructure", "presentation", "bootstrap"},
                             "presentation": {"infrastructure", "bootstrap"},
                             "infrastructure": {"presentation", "bootstrap"}}.get(layer, set())
                if forbidden.intersection(destination):
                    errors.append(f"{layer} imports {dependency}")
            if layer == "domain" and dependency.split("/", 1)[0] in {"os", "syscall", "unsafe", "net", "C"}:
                errors.append(f"domain imports side-effect package {dependency}")
            if layer == "application" and dependency.startswith("github.com/egoist/mygo"):
                errors.append("application imports the native UI framework")
    elif path.suffix == ".swift" and layer in {"domain", "application"}:
        code = source_code(text)
        forbidden = {"NativeCore", "Glibc", "FileManager", "FileHandle", "ProcessInfo", "Composition", "EngineRegistry", *swift_adapters}
        if layer == "domain":
            forbidden.update({"Date", "UUID", "NativeHandle"})
        for name in sorted(forbidden):
            if re.search(r"\b" + re.escape(name) + r"\b", code):
                errors.append(f"{layer} references adapter or runtime {name}")
        if re.search(r"\bmgfr?_\w+|@_cdecl|\bUnsafe\w*Pointer\b", code):
            errors.append(f"{layer} contains an FFI operation or native pointer")
    elif path.suffix == ".rs":
        code = source_code(text)
        if layer == "domain" and re.search(r"\b(ffi|unsafe)\b|::(infrastructure|application|presentation)::|std::(fs|env|net|thread)::", code):
            errors.append("domain depends on native or effectful implementation")
        if ("capture" in parts or "audio" in parts) and "crate::recording::infrastructure" in code:
            errors.append("capture/audio depends on the recording implementation instead of shared media transport")
    return [f"{path}: {message}" for message in errors]


def check(root=ROOT):
    failures = []
    source = root / "src"
    swift_adapters = set()
    for path in (source / "swift").rglob("*.swift"):
        if "infrastructure" in path.parts:
            swift_adapters.update(re.findall(r"(?:struct|class|enum)\s+(\w+)", source_code(path.read_text())))
    count = 0
    for path in sorted(source.rglob("*")):
        if "target" in path.parts or "build" in path.parts or path.suffix not in {".go", ".swift", ".rs"} or path.name.endswith("_test.go") or path.name == "catalog_generated.go":
            continue
        count += 1
        failures.extend(check_file(path.relative_to(root), path.read_text(), swift_adapters))
    for name in ("cmd", "internal", "core"):
        if (root / name).exists():
            failures.append(f"{name}: production source must be under src/")
    return count, failures


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--json", type=Path, help="Write the checked-file count and dependency violations.")
    arguments = parser.parse_args()
    count, failures = check()
    report = {"checkedFiles": count, "violations": failures, "passed": not failures}
    if arguments.json:
        arguments.json.parent.mkdir(parents=True, exist_ok=True)
        arguments.json.write_text(json.dumps(report, indent=2) + "\n")
    print("\n".join(failures) if failures else f"Architecture checks passed for {count} source files.")
    return int(bool(failures))


if __name__ == "__main__":
    raise SystemExit(main())
