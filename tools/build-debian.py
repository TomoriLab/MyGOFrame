#!/usr/bin/env python3
"""Prepare a vendored source snapshot and build unsigned local Debian packages."""

import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile


def run(arguments, directory, environment):
    subprocess.run(arguments, cwd=directory, env=environment, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=Path("build/debian"))
    arguments = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    output = arguments.output.resolve()
    if output == root:
        raise SystemExit("The package output directory must differ from the repository root.")
    output.mkdir(parents=True, exist_ok=True)
    version = subprocess.check_output(
        ["dpkg-parsechangelog", "-SVersion"], cwd=root, text=True
    ).strip()
    upstream_version = version.rsplit("-", 1)[0]
    build_directory = Path(tempfile.mkdtemp(prefix="mygoframe-build-"))
    source = build_directory / f"mygoframe-{upstream_version}"
    def ignored(directory, names):
        excluded = {".git", "build", "work", "vendor", "target", "__pycache__",
                    "catalog_generated.go", "catalog_generated.go.new"}
        return [name for name in names if name in excluded or (Path(directory) / name).resolve() == output]
    shutil.copytree(root, source, ignore=ignored)
    environment = os.environ.copy()
    environment.update(GOPROXY="off", GOSUMDB="off", GOTOOLCHAIN="local", GOFLAGS="")
    environment["DEB_BUILD_PROFILES"] = " ".join(filter(None, [
        environment.get("DEB_BUILD_PROFILES", ""), "pkg.mygoframe.external-toolchains"
    ]))
    if not environment.get("SWIFTC"):
        swift_compiler = shutil.which("swiftc")
        swiftly_compiler = Path.home() / ".local/share/swiftly/bin/swiftc"
        if not swift_compiler and swiftly_compiler.is_file():
            swift_compiler = str(swiftly_compiler)
        if not swift_compiler:
            raise SystemExit("Swift compiler not found. Set SWIFTC to an official Swift compiler.")
        environment["SWIFTC"] = swift_compiler
    try:
        run(["python3", str(root / "tools/prepare-native-runtime.py"), "--download-only"], root, environment)
        # Pinned source archives make the generated Debian source package rebuild offline.
        shutil.copytree(root / "build/native-inputs", source / "native-inputs", dirs_exist_ok=True)
        run(["go", "mod", "vendor"], source, environment)
        module = json.loads(subprocess.check_output(
            ["go", "list", "-mod=mod", "-m", "-json", "github.com/egoist/mygo"],
            cwd=source, env=environment, text=True
        ))
        # Go vendoring omits the TypeScript sources for MyGo's embedded JavaScript assets.
        bridge_source = source / "vendor/github.com/egoist/mygo/packages/bridge"
        shutil.copytree(Path(module["Dir"]) / "packages/bridge", bridge_source)
        # Module-cache directories are read-only and must remain removable in the build tree.
        for path in [bridge_source, *bridge_source.rglob("*")]:
            path.chmod(0o755 if path.is_dir() else 0o644)
        epoch = int(subprocess.check_output(
            ["dpkg-parsechangelog", "-STimestamp"], cwd=source, text=True
        ).strip())
        archive_path = build_directory / f"mygoframe_{upstream_version}.orig.tar.xz"
        def normalized(info):
            relative = Path(info.name).parts[1:]
            if relative and relative[0] == "debian":
                return None
            info.uid = info.gid = 0
            info.uname = info.gname = "root"
            info.mtime = epoch
            return info
        with tarfile.open(archive_path, "w:xz") as archive:
            archive.add(source, arcname=source.name, filter=normalized)
        run(["dpkg-buildpackage", "-us", "-uc"], source, environment)
        for artifact in sorted(build_directory.iterdir()):
            if artifact.is_file():
                shutil.copy2(artifact, output / artifact.name)
    except BaseException:
        print(f"Build tree retained at {build_directory}", flush=True)
        raise
    shutil.rmtree(build_directory)
    print(f"Debian artifacts written to {output}")


if __name__ == "__main__":
    main()
