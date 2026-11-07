#!/usr/bin/env python3
"""Validate native EGL ownership under ASan without opening a capture device."""

import argparse
import importlib.util
import json
import math
import os
from pathlib import Path
import subprocess
import sys


def sanitizer_options(existing, required):
    options = {}
    for entry in existing.split(":"):
        if not entry:
            continue
        key, separator, value = entry.partition("=")
        if key == "suppressions":
            raise ValueError("Sanitizer suppression files are unsupported by this validation.")
        options[key] = value if separator else "1"
    options.update(required)
    return ":".join(f"{key}={value}" for key, value in options.items())


def prepare_environment(root, baseline):
    environment = os.environ.copy()
    if any(Path(item).name.lower().startswith(("libasan.so", "libclang_rt.asan"))
           for item in environment.get("LD_PRELOAD", "").replace(" ", ":").split(":")):
        raise ValueError("The native probe links ASan directly; remove ASan from LD_PRELOAD.")
    environment["RUSTC_BOOTSTRAP"] = "1"
    environment["ASAN_OPTIONS"] = sanitizer_options(environment.get("ASAN_OPTIONS", ""), {
        "detect_leaks": "1", "leak_check_at_exit": "1", "halt_on_error": "1",
        "abort_on_error": "1", "start_deactivated": "0", "fast_unwind_on_malloc": "0", "malloc_context_size": "40",
    })
    environment["LSAN_OPTIONS"] = sanitizer_options(environment.get("LSAN_OPTIONS", ""), {
        "detect_leaks": "1", "leak_check_at_exit": "1", "exitcode": "23", "report_objects": "1",
    })
    changes = {}
    if not baseline:
        specification = importlib.util.spec_from_file_location(
            "dma_color_validation", root / "tools/validate-dma-colors.py")
        module = importlib.util.module_from_spec(specification)
        specification.loader.exec_module(module)
        changes = module.prepare_graphics_runtime(environment)
    return environment, changes


def execute(command, environment, timeout):
    try:
        process = subprocess.run(command, env=environment, stdout=subprocess.PIPE,
                                 stderr=subprocess.STDOUT, text=True, timeout=timeout)
        return {"command": command, "exitCode": process.returncode,
                "output": process.stdout, "timedOut": False}
    except subprocess.TimeoutExpired as error:
        output = error.stdout or ""
        if isinstance(output, bytes):
            output = output.decode("utf-8", "replace")
        return {"command": command, "exitCode": None, "output": output, "timedOut": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    root = Path(__file__).resolve().parent.parent
    parser.add_argument("--cycles", type=int, default=5,
                        help="Repeat this many complete lifecycles for each thread owner.")
    parser.add_argument("--baseline", action="store_true",
                        help="Skip automatic graphics policy and preserve graphics environment values.")
    parser.add_argument("--rustc", default=os.environ.get("RUSTC", "rustc"),
                        help="Use this existing Rust compiler.")
    parser.add_argument("--build-directory", type=Path, default=root / "build/egl-lifecycle",
                        help="Keep the native probe in this build directory.")
    parser.add_argument("--timeout", type=float, default=60,
                        help="Limit each compiler or probe process to this many seconds.")
    parser.add_argument("--output", type=Path, help="Save the complete JSON validation result.")
    arguments = parser.parse_args()
    if (not 1 <= arguments.cycles <= 1000
            or not math.isfinite(arguments.timeout) or arguments.timeout <= 0):
        parser.error("Cycles must be between 1 and 1000 and the timeout must be positive.")
    result = {"passed": False, "baseline": arguments.baseline, "cyclesPerOwner": arguments.cycles,
              "captureDeviceOpened": False, "mediaCoreLoaded": False, "runs": []}
    try:
        environment, changes = prepare_environment(root, arguments.baseline)
        keys = ("__GL_DisableDBus", "__EGL_EXTERNAL_PLATFORM_CONFIG_FILENAMES",
                "__EGL_EXTERNAL_PLATFORM_CONFIG_DIRS", "ASAN_OPTIONS", "LSAN_OPTIONS",
                "RUSTC_BOOTSTRAP", "GDK_BACKEND", "WAYLAND_DISPLAY", "WAYLAND_SOCKET",
                "GBM_BACKEND", "GBM_BACKENDS_PATH", "MESA_LOADER_DRIVER_OVERRIDE",
                "__GL_MaxFramesAllowed", "VK_DRIVER_FILES", "VK_ICD_FILENAMES")
        result["environment"] = {key: environment[key] for key in keys if key in environment}
        result["graphicsRuntimeChanges"] = changes
        version = execute([arguments.rustc, "--version", "--verbose"], environment, arguments.timeout)
        result["compiler"] = version
        if version["exitCode"] != 0:
            raise RuntimeError("The Rust compiler version check failed.")
        arguments.build_directory.mkdir(parents=True, exist_ok=True)
        binary = str((arguments.build_directory / "egl-lifecycle-probe").resolve())
        command = [arguments.rustc, "--edition=2024", "-Zsanitizer=address", "-C",
                   "link-arg=-fuse-ld=lld", "-C", "force-frame-pointers=yes", "-g",
                   str(root / "tests/egl_lifecycle_probe.rs"), "-o", binary]
        build = execute(command, environment, arguments.timeout)
        result["build"] = build
        if build["exitCode"] != 0:
            raise RuntimeError("The native ASan probe build failed.")
        for owner in ("main", "worker"):
            run = execute([binary, str(arguments.cycles), owner], environment, arguments.timeout)
            run["owner"] = owner
            run["passed"] = run["exitCode"] == 0
            result["runs"].append(run)
        result["passed"] = all(run["passed"] for run in result["runs"])
    except (OSError, RuntimeError, ValueError) as error:
        result["error"] = str(error)
    if arguments.output:
        arguments.output.parent.mkdir(parents=True, exist_ok=True)
        arguments.output.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, indent=2))
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
