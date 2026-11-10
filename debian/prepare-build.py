#!/usr/bin/env python3
"""Validate the external toolchains and preserve linked dependency notices."""

import json
import os
from pathlib import Path
import re
import shutil
import subprocess

root = Path.cwd()
subprocess.run([
    "python3", "tools/prepare-native-runtime.py", "--download-only", "--offline"
], check=True)
if not (root / "vendor/modules.txt").is_file():
    raise SystemExit("Missing vendored Go sources. Prepare the source package with make deb.")
compiler = os.environ.get("SWIFTC", "")
if not compiler:
    raise SystemExit("Swift compiler not found. Set SWIFTC to the official Swift 6.3.3 compiler.")
version = subprocess.check_output([compiler, "--version"], text=True)
match = re.search(r"Swift version (\d+)\.(\d+)\.(\d+)", version)
if not match or tuple(map(int, match.groups())) != (6, 3, 3):
    raise SystemExit("Swift 6.3.3 is required to match the bundled runtime license notices.")
information = json.loads(subprocess.check_output([compiler, "-print-target-info"], text=True))
static_libraries = Path(information["paths"]["runtimeResourcePath"]).parent / "swift_static/linux"
if not (static_libraries / "libFoundation.a").is_file():
    raise SystemExit("The Swift toolchain must include Linux static Foundation libraries.")

notices = root / "debian/third-party"
notices.mkdir(exist_ok=True)
for source in sorted((root / "debian/runtime-notices").iterdir()):
    shutil.copy2(source, notices / source.name)
go_root = Path(subprocess.check_output(["go", "env", "GOROOT"], text=True).strip())
go_license = go_root / "LICENSE"
if not go_license.is_file():
    go_version = subprocess.check_output(["go", "env", "GOVERSION"], text=True).strip()
    go_series = re.match(r"go(\d+\.\d+)", go_version)
    if go_series:
        go_license = Path(f"/usr/share/doc/golang-{go_series[1]}-src/copyright")
if not go_license.is_file():
    raise SystemExit("The Go toolchain license or Debian copyright file is required.")
shutil.copy2(go_license, notices / "Go-LICENSE")
rust_compiler = os.environ.get("RUSTC", "rustc")
rust_root = Path(subprocess.check_output([rust_compiler, "--print", "sysroot"], text=True).strip())
rust_copyright = rust_root / "share/doc/rust/COPYRIGHT-library.html"
if not rust_copyright.is_file():
    rust_copyright = Path("/usr/share/doc/libstd-rust-dev/copyright")
if not rust_copyright.is_file():
    raise SystemExit("The Rust standard-library copyright and license notices are required.")
shutil.copy2(rust_copyright, notices / ("Rust-COPYRIGHT-library.html" if rust_copyright.suffix == ".html" else "Rust-copyright"))
owner = subprocess.run(["dpkg-query", "-S", str(go_root / "bin/go")], text=True, capture_output=True)
source = ""
if owner.returncode == 0:
    package = owner.stdout.split(": ", 1)[0]
    source = subprocess.check_output([
        "dpkg-query", "-W", "-f=${source:Package} (= ${source:Version})", package
    ], text=True).strip()
(root / "debian/go-static-built-using").write_text(source + "\n")
for source in sorted((root / "vendor").rglob("*")):
    if not source.is_file():
        continue
    name = source.name.upper()
    if name.startswith(("LICENSE", "COPYING", "NOTICE", "COPYRIGHT", "PATENTS")) or name == "AUTHORS":
        destination = notices / "go-modules" / source.relative_to(root / "vendor")
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, destination)
print("Validated Swift static libraries and collected dependency notices.")
