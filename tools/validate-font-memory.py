#!/usr/bin/env python3
"""Validate isolated Fontconfig ownership controls with unsuppressed ASan/LSan.

A passing diagnostic reproduces expected retained-state and lost-owner reports
and verifies clean explicit owner release. It does not mean the GUI is leak-free.
The sole-owner FcFini case runs in a separate Fontconfig-only child process and
does not propose application-wide Fontconfig cleanup.
"""

import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import re
import subprocess
import sys


EXPECTED_EXITS = {
    "pattern-retain": 23,
    "pattern-release": 0,
    "pattern-lost-owner": 23,
    "pattern-checked": 0,
    "config-retain": 23,
    "config-release": 0,
    "global-retain": 23,
    "global-paired": 0,
    "pango-release": 23,
    "pango-release-5": 23,
    "pango-preinit": 23,
}
SUMMARY = re.compile(r"SUMMARY: AddressSanitizer: (\d+) byte\(s\) leaked in (\d+) allocation\(s\)\.")
INVALID_ACCESS = re.compile(
    r"ERROR: AddressSanitizer:|AddressSanitizer:DEADLYSIGNAL|Sanitizer CHECK failed|"
    r"LeakSanitizer has encountered a fatal error|runtime error:"
)


def sanitizer_options(existing, required):
    options = {}
    for entry in existing.split(":"):
        if not entry:
            continue
        key, separator, value = entry.partition("=")
        key = key.strip()
        if key.lower().startswith(("suppress", "ignore")):
            raise ValueError("Sanitizer suppression and ignore options are unsupported.")
        options[key] = value if separator else "1"
    options.update(required)
    return ":".join(f"{key}={value}" for key, value in options.items())


def prepare_environment():
    environment = os.environ.copy()
    if environment.get("LD_PRELOAD", "").strip() or environment.get("LD_AUDIT", "").strip():
        raise ValueError("Remove LD_PRELOAD and LD_AUDIT; the native probe links ASan directly.")
    required = {
        "detect_leaks": "1", "leak_check_at_exit": "1", "halt_on_error": "1",
        "abort_on_error": "0", "exitcode": "23", "log_path": "stderr",
    }
    environment["ASAN_OPTIONS"] = sanitizer_options(environment.get("ASAN_OPTIONS", ""), {
        **required, "start_deactivated": "0", "fast_unwind_on_malloc": "0",
        "malloc_context_size": "40",
    })
    environment["LSAN_OPTIONS"] = sanitizer_options(environment.get("LSAN_OPTIONS", ""), {
        **required, "report_objects": "1", "max_leaks": "0",
    })
    environment["RUSTC_BOOTSTRAP"] = "1"
    return environment


def execute(command, environment, timeout, log):
    try:
        process = subprocess.run(command, env=environment, stdout=subprocess.PIPE,
                                 stderr=subprocess.STDOUT, text=True, timeout=timeout)
        output, exit_code, timed_out = process.stdout, process.returncode, False
    except subprocess.TimeoutExpired as error:
        output = error.stdout or ""
        if isinstance(output, bytes):
            output = output.decode("utf-8", "replace")
        exit_code, timed_out = None, True
    log.write_text(output)
    return {"command": command, "exitCode": exit_code, "timedOut": timed_out,
            "log": str(log)}, output


def classify(mode, run, output):
    summaries = [{"bytes": int(size), "allocations": int(count)}
                 for size, count in SUMMARY.findall(output)]
    expected = EXPECTED_EXITS[mode]
    errors = []
    if run["timedOut"] or run["exitCode"] != expected:
        errors.append(f"Expected exit {expected}; observed {run['exitCode']}.")
    if INVALID_ACCESS.search(output):
        errors.append("The native process reported an invalid access or sanitizer failure.")
    expected_reports = 1 if expected == 23 or mode == "pattern-checked" else 0
    if len(summaries) != expected_reports:
        errors.append(f"Expected {expected_reports} leak report(s); observed {len(summaries)}.")
    observations = [line for line in output.splitlines() if line.startswith((
        "fontconfigVersion=", "pattern=", "ownedConfig=", "cycle=", "baselineThreads=",
        "retainedPatternLeakCheck=", "retainedPatternQuery=", "releasedPatternLeakCheck=",
    ))]
    if mode == "pattern-checked":
        markers = [line for line in observations if line.startswith((
            "retainedPatternLeakCheck=", "retainedPatternQuery=", "releasedPatternLeakCheck="))]
        if markers != ["retainedPatternLeakCheck=1", "retainedPatternQuery=PASS",
                       "releasedPatternLeakCheck=0"]:
            errors.append("The live-owner query and recoverable 1-to-0 checks did not complete in order.")
    if mode.startswith("pango-"):
        threads = re.search(r"baselineThreads=(\d+) finalThreads=(\d+)", output)
        if not threads or threads.group(1) != threads.group(2):
            errors.append("The Pango worker thread count did not return to its baseline.")
        expected_cycles = 5 if mode == "pango-release-5" else 1
        if len(re.findall(r"^cycle=\d+ families=\d+ released=PASS$", output, re.MULTILINE)) != expected_cycles:
            errors.append("The expected Pango map/context release cycles did not complete.")
    run.update({"mode": mode, "expectedExitCode": expected, "leakReports": summaries,
                "observations": observations, "errors": errors, "passed": not errors})
    return run


def compare_controls(runs):
    by_mode = {run["mode"]: run for run in runs}
    errors = []
    if not all(run["passed"] for run in runs):
        return errors
    retained = by_mode["pattern-retain"]["leakReports"][0]
    lost = by_mode["pattern-lost-owner"]["leakReports"][0]
    if lost["allocations"] != retained["allocations"] + 1 or lost["bytes"] <= retained["bytes"]:
        errors.append("The lost-owner control did not add the pattern owner allocation.")
    if by_mode["pattern-checked"]["leakReports"][0] != retained:
        errors.append("The recoverable live-owner report differed from the retained-pattern control.")
    single = by_mode["pango-release"]["leakReports"][0]
    repeated = by_mode["pango-release-5"]["leakReports"][0]
    if single != repeated:
        errors.append("The residual report changed between one and five Pango lifecycles.")
    return errors


def main():
    root = Path(__file__).resolve().parent.parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rustc", default=os.environ.get("RUSTC", "rustc"),
                        help="Use this existing Rust compiler with AddressSanitizer support.")
    parser.add_argument("--build-directory", type=Path, default=root / "build/font-memory",
                        help="Keep the native probe in this ignored build directory.")
    parser.add_argument("--log-directory", type=Path,
                        help="Save complete child logs here; default: BUILD_DIRECTORY/logs.")
    parser.add_argument("--output", type=Path, help="Save the complete JSON diagnostic result.")
    parser.add_argument("--timeout", type=float, default=90,
                        help="Limit each compiler or probe process to this many seconds.")
    arguments = parser.parse_args()
    if not math.isfinite(arguments.timeout) or arguments.timeout <= 0:
        parser.error("The timeout must be finite and positive.")
    result = {
        "passed": False,
        "scope": "Isolated native Fontconfig/Pango owner and detector controls; not a whole-GUI leak check.",
        "expectedLeakExitCode": 23, "captureDeviceOpened": False,
        "graphicsContextCreated": False, "mediaCoreLoaded": False, "runs": [],
        "soleOwnerFinalization": "FcFini is restricted to its Fontconfig-only diagnostic child.",
    }
    try:
        if sys.platform != "linux":
            raise RuntimeError("This SONAME-based diagnostic requires Linux.")
        environment = prepare_environment()
        build_directory = arguments.build_directory.resolve()
        log_directory = (arguments.log_directory or build_directory / "logs").resolve()
        build_directory.mkdir(parents=True, exist_ok=True)
        log_directory.mkdir(parents=True, exist_ok=True)
        result["logDirectory"] = str(log_directory)
        result["environment"] = {key: environment[key] for key in (
            "RUSTC_BOOTSTRAP", "ASAN_OPTIONS", "LSAN_OPTIONS", "FONTCONFIG_FILE",
            "FONTCONFIG_PATH", "FONTCONFIG_SYSROOT", "LD_LIBRARY_PATH") if key in environment}
        compiler, compiler_output = execute([arguments.rustc, "--version", "--verbose"],
                                            environment, arguments.timeout, log_directory / "compiler.log")
        result["compiler"] = {**compiler, "version": compiler_output.strip()}
        if compiler["exitCode"] != 0:
            raise RuntimeError("The Rust compiler version check failed.")
        binary = build_directory / "font-ownership-probe"
        source = root / "tests/font_ownership_probe.rs"
        build, _ = execute([arguments.rustc, "--edition=2024", "-Zsanitizer=address", "-C",
                            "link-arg=-fuse-ld=lld", "-C", "force-frame-pointers=yes", "-g",
                            str(source), "-o", str(binary)], environment, arguments.timeout,
                           log_directory / "build.log")
        result["build"] = build
        if build["exitCode"] != 0:
            raise RuntimeError("The native ASan probe build failed; inspect build.log.")
        result["sourceSha256"] = hashlib.sha256(source.read_bytes()).hexdigest()
        result["binarySha256"] = hashlib.sha256(binary.read_bytes()).hexdigest()
        for mode in EXPECTED_EXITS:
            run, output = execute([str(binary), mode], environment, arguments.timeout,
                                  log_directory / f"{mode}.log")
            result["runs"].append(classify(mode, run, output))
        result["comparisonErrors"] = compare_controls(result["runs"])
        result["passed"] = (all(run["passed"] for run in result["runs"])
                            and not result["comparisonErrors"])
    except (OSError, RuntimeError, ValueError) as error:
        result["error"] = str(error)
    serialized = json.dumps(result, indent=2) + "\n"
    if arguments.output:
        arguments.output.parent.mkdir(parents=True, exist_ok=True)
        arguments.output.write_text(serialized)
    print(serialized, end="")
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
