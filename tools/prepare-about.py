#!/usr/bin/env python3
"""Embed build-specific dependency notices without adding a runtime dependency."""

import fcntl
import hashlib
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import re
import subprocess
import sys


class PlainText(HTMLParser):
    def __init__(self):
        super().__init__()
        self.parts = []
        self.ignored = 0

    def handle_starttag(self, tag, attributes):
        if tag in {"script", "style"}:
            self.ignored += 1

    def handle_endtag(self, tag):
        if tag in {"script", "style"}:
            self.ignored -= 1
        elif tag in {"p", "pre", "h1", "h2", "h3", "li", "div", "section"}:
            self.parts.append("\n")

    def handle_data(self, data):
        if not self.ignored:
            self.parts.append(data)


def command(arguments, root=None):
    result = subprocess.run(arguments, cwd=root, text=True, capture_output=True)
    return result.stdout.strip() if result.returncode == 0 else ""


def json_stream(text):
    decoder = json.JSONDecoder()
    while text.strip():
        text = text.lstrip()
        value, offset = decoder.raw_decode(text)
        yield value
        text = text[offset:]


def license_label(text):
    labels = list(dict.fromkeys(re.findall(r"^License:\s*(.+)", text, re.MULTILINE)))
    return ", ".join(labels[:4]) + ("; additional terms" if len(labels) > 4 else "") if labels else "Upstream license and notices"


def go_string(text):
    # Go rejects literal byte-order marks even inside an interpreted string.
    return json.dumps(text, ensure_ascii=False).replace("\ufeff", "\\uFEFF")


class Catalog:
    def __init__(self):
        self.documents = {}
        self.components = []

    def document(self, path, source):
        data = path.read_bytes()
        if len(data) > 4 * 1024 * 1024:
            raise ValueError(f"License file exceeds the size limit: {path}")
        text = data.decode("utf-8", errors="replace")
        if path.suffix == ".html":
            parser = PlainText()
            parser.feed(text)
            text = "".join(parser.parts).strip()
        if not text.strip():
            raise ValueError(f"Empty license file: {path}")
        digest = hashlib.sha256(data).hexdigest()
        identifier = digest + ("-text" if path.suffix == ".html" else "")
        self.documents.setdefault(identifier, {"title": path.name, "source": source,
                                               "sha256": digest, "text": text})
        return identifier

    def add(self, identifier, name, version, scope, label, paths, source_url=""):
        documents = list(dict.fromkeys(self.document(path, source) for path, source in paths))
        if not documents:
            raise ValueError(f"No license text found for {name}")
        self.components.append({"id": identifier, "name": name, "version": version,
                                "scope": scope, "license": label, "url": source_url,
                                "documents": documents})

    def common_licenses(self, paths):
        result = list(paths)
        for path, source in paths:
            text = path.read_text(errors="replace")
            names = re.findall(r"/usr/share/common-licenses/([A-Za-z0-9.+-]+)", text)
            for name in dict.fromkeys(names):
                common = Path("/usr/share/common-licenses") / name.rstrip(".")
                if common.is_file():
                    result.append((common, str(common)))
        return result


def collect_go(catalog, root, go, flags):
    result = subprocess.run([go, "list", *flags, "-deps", "-json", "./src/cmd/mygoframe"], cwd=root,
                            text=True, capture_output=True)
    if result.returncode:
        raise ValueError("Cannot inventory Go dependencies: " + result.stderr.strip())
    modules = {}
    for package in json_stream(result.stdout):
        module = package.get("Module", {})
        if module and not module.get("Main"):
            modules[module["Path"]] = module
    for name, module in sorted(modules.items()):
        directory = Path(module.get("Replace", {}).get("Dir") or module.get("Dir") or root / "vendor" / name)
        paths = [(path, f"{name}@{module.get('Version', '')}/{path.relative_to(directory)}")
                 for path in sorted(directory.rglob("*")) if path.is_file()
                 and path.name.upper().startswith(("LICENSE", "COPYING", "NOTICE", "COPYRIGHT", "PATENTS"))]
        label = {"github.com/egoist/mygo": "MIT", "github.com/ebitengine/purego": "Apache-2.0",
                 "github.com/go-text/typesetting": "BSD-3-Clause OR Unlicense",
                 "golang.org/x/image": "BSD-3-Clause", "golang.org/x/text": "BSD-3-Clause"}.get(name, "Upstream license and notices")
        catalog.add("go:" + name, "MyGo" if name == "github.com/egoist/mygo" else name,
                    module.get("Version", ""), "Go module", label, paths,
                    "https://" + name if name.startswith("github.com/") else "https://pkg.go.dev/" + name)
    go_root = Path(command([go, "env", "GOROOT"]))
    version = command([go, "env", "GOVERSION"])
    path = go_root / "LICENSE"
    if not path.is_file():
        series = re.match(r"go(\d+\.\d+)", version)
        path = Path(f"/usr/share/doc/golang-{series[1]}-src/copyright") if series else path
    catalog.add("runtime:go", "Go standard library and runtime", version, "Language runtime",
                "BSD-3-Clause and upstream notices", catalog.common_licenses([(path, "Go toolchain license")]),
                "https://go.dev")


def collect_runtimes(catalog, root):
    groups = [
        ("swift", "Swift runtime", ["swift-LICENSE.txt"], "Apache-2.0 with Swift exception"),
        ("foundation", "Swift Foundation", ["swift-foundation-LICENSE.md", "swift-foundation-NOTICE.txt", "swift-corelibs-foundation-LICENSE"], "Apache-2.0 with Swift exception; upstream notices"),
        ("dispatch", "Swift libdispatch", ["swift-corelibs-libdispatch-LICENSE"], "Apache-2.0; upstream notices"),
        ("icu", "Swift ICU", ["swift-foundation-icu-LICENSE.md", "icu-76.1-LICENSE"], "Unicode and upstream notices"),
    ]
    for identifier, name, files, label in groups:
        catalog.add("runtime:" + identifier, name, "76.1" if identifier == "icu" else "6.3.3 notices",
                    "Language runtime", label,
                    [(root / "debian/runtime-notices" / file, "debian/runtime-notices/" + file) for file in files],
                    "https://www.swift.org" if identifier != "icu" else "https://icu.unicode.org")
    rustc = os.environ.get("RUSTC", "rustc")
    sysroot = Path(command([rustc, "--print", "sysroot"]))
    paths = [(path, "Rust toolchain/" + path.name) for path in
             [sysroot / "share/doc/rust/COPYRIGHT-library.html"] if path.is_file()]
    if not paths:
        paths = [(Path("/usr/share/doc/libstd-rust-dev/copyright"), "Rust standard library copyright")]
    catalog.add("runtime:rust", "Rust standard library and runtime", command([rustc, "--version"]),
                "Language runtime", "MIT OR Apache-2.0; upstream notices", catalog.common_licenses(paths),
                "https://www.rust-lang.org")
    lock = json.loads((root / "native/runtime-lock.json").read_text())
    for identifier, name, prefix, version, label in [
        ("gtk", "GTK and GDK private runtime", "GTK-", lock["libraries"]["gtk"]["version"], "LGPL-2.0-or-later; upstream notices"),
        ("fcitx", "Fcitx GClient private runtime", "Fcitx-", lock["libraries"]["fcitx"]["version"], "LGPL-2.1-or-later"),
        ("kde", "KDE GTK integration modules", "KDE-", lock["gtkModuleBuild"]["version"], "GPL-2.0 OR GPL-3.0 OR KDE-Accepted-GPL; upstream notices"),
    ]:
        paths = [(path, "native/notices/" + path.name) for path in sorted((root / "native/notices").glob(prefix + "*"))
                 if path.is_file() and path.suffix != ".json"]
        catalog.add("native:" + identifier, name, version, "Private native runtime", label,
                    catalog.common_licenses(paths), "https://www.gtk.org" if identifier == "gtk" else
                    "https://fcitx-im.org" if identifier == "fcitx" else "https://kde.org")


def collect_system(catalog, root):
    roots = [root / "build/libmygoframe_core.so", root / "build/libmygoframe_media.so"]
    roots += list((root / "build/native-runtime").glob("*.so.*"))
    roots += list((root / "build/native-runtime/gtk-3.0/modules").glob("*.so"))
    roots += list(Path("/usr/lib").glob("*/dri/zink_dri.so"))
    roots += list(Path("/usr/lib").glob("*/gbm/dri_gbm.so"))
    libraries = set()
    for path in roots:
        if not path.is_file():
            continue
        for line in command(["ldd", str(path)]).splitlines():
            match = re.search(r"(?:=>\s*)?(/\S+)", line)
            if match:
                libraries.add(str(Path(match[1]).resolve()))
    # MyGo opens these established native runtimes dynamically rather than through ELF dependencies.
    loader = command(["/sbin/ldconfig", "-p"])
    for name in ["libepoxy.so.0", "libEGL.so.1", "libGL.so.1", "libGLESv2.so.2", "libvulkan.so.1",
                 "libdbus-1.so.3", "libfontconfig.so.1", "libpangocairo-1.0.so.0", "libEGL_mesa.so.0"]:
        match = re.search(r"^\s*" + re.escape(name) + r"\s+.*=>\s+(\S+)", loader, re.MULTILINE)
        if match:
            for line in command(["ldd", match[1]]).splitlines() + ["=> " + match[1]]:
                path = re.search(r"=>\s*(/\S+)", line)
                if path:
                    libraries.add(str(Path(path[1]).resolve()))
    ownership = subprocess.run(["dpkg-query", "-S", *sorted(libraries)], text=True, capture_output=True) if libraries else None
    # A private or toolchain library can be unowned while other package matches remain valid.
    owners = ownership.stdout.splitlines() if ownership else []
    packages = set()
    for line in owners:
        if ": /" in line:
            packages.update(line.split(": /", 1)[0].split(", "))
    groups = {}
    for package in sorted(packages):
        metadata = command(["dpkg-query", "-W", "-f=${source:Package}\t${Version}\t${Homepage}", package]).split("\t")
        if len(metadata) < 2:
            continue
        name, version = metadata[:2]
        if name in {"gtk+3.0", "fcitx5-gtk", "kde-gtk-config"}:
            continue
        copyright = Path("/usr/share/doc") / package.split(":")[0] / "copyright"
        if not copyright.is_file():
            continue
        value = groups.setdefault(name, {"version": version, "url": metadata[2] if len(metadata) > 2 else "", "paths": []})
        value["paths"].append((copyright, str(copyright)))
    for name, value in sorted(groups.items()):
        text = "\n".join(path.read_text(errors="replace") for path, _ in value["paths"])
        scope = "System-linked library"
        if name.startswith(("cuda", "nvidia")):
            scope = "Optional vendor runtime"
        catalog.add("system:" + name, name, value["version"], scope, license_label(text),
                    catalog.common_licenses(value["paths"]), value["url"])


def _prepare(go="go", flags=()):
    root = Path(__file__).resolve().parent.parent
    catalog = Catalog()
    catalog.add("mygoframe", "MyGOFrame", "", "Application", "MIT", [(root / "LICENSE", "MyGOFrame/LICENSE")],
                "https://github.com/TomoriLab/MyGOFrame")
    collect_go(catalog, root, go, flags)
    collect_runtimes(catalog, root)
    collect_system(catalog, root)
    information = json.loads(subprocess.check_output([sys.executable, str(root / "tools/version.py"), "metadata"], cwd=root, text=True))
    catalog.components[0]["version"] = information["version"]
    document = {"schemaVersion": 1, **information,
                "developer": "zeroday0619", "contact": "zero@tomori.dev", "organization": "Tomori Lab",
                "projectURL": "https://github.com/TomoriLab/MyGOFrame", "components": catalog.components,
                "documents": catalog.documents}
    data = json.dumps(document, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    if len(data.encode()) > 24 * 1024 * 1024:
        raise ValueError("The embedded license catalog exceeds its size limit.")
    output = root / "src/internal/about/infrastructure/notices/catalog_generated.go"
    content = "// Code generated by tools/prepare-about.py; DO NOT EDIT.\npackage notices\n\nconst catalogJSON = " + go_string(data) + "\n"
    if not output.is_file() or output.read_text() != content:
        temp = output.with_suffix(".go.new")
        temp.write_text(content)
        temp.replace(output)
    directory = root / "build/about"
    directory.mkdir(parents=True, exist_ok=True)
    (directory / "catalog.json").write_text(data + "\n")
    return document


def prepare(go="go", flags=()):
    directory = Path(__file__).resolve().parent.parent / "build/about"
    directory.mkdir(parents=True, exist_ok=True)
    with (directory / "generation.lock").open("w") as handle:
        fcntl.flock(handle, fcntl.LOCK_EX)
        return _prepare(go, flags)


if __name__ == "__main__":
    prepare(os.environ.get("GO", "go"))
