#!/usr/bin/env python3
"""Compare CPU and DMA-BUF video paths using the application's native UI checks."""

import argparse
from datetime import datetime, timezone
from fractions import Fraction
import hashlib
import json
import math
import os
from pathlib import Path
import signal
import statistics
import subprocess
import sys
import tempfile
import time


class BenchmarkError(RuntimeError):
    """A run failed validation or could not produce comparable measurements."""


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, delete=False) as stream:
        temporary = Path(stream.name)
        json.dump(value, stream, indent=2, allow_nan=False)
        stream.write("\n")
    temporary.replace(path)


def terminate(process):
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        try:
            _, status, _ = os.wait4(process.pid, 0)
            process.returncode = os.waitstatus_to_exitcode(status)
        except ChildProcessError:
            pass
        return
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        identifier, status, _ = os.wait4(process.pid, os.WNOHANG)
        if identifier:
            process.returncode = os.waitstatus_to_exitcode(status)
            return
        time.sleep(0.05)
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    _, status, _ = os.wait4(process.pid, 0)
    process.returncode = os.waitstatus_to_exitcode(status)


def run_process(command, environment, timeout, stdout_path, stderr_path, resource_path):
    time_program = Path("/usr/bin/time")
    resource_format = '{"userSeconds":%U,"systemSeconds":%S,"elapsedSeconds":%e,"maximumResidentKiB":%M}'
    measured_command = command
    measurement = "wait4"
    if time_program.is_file():
        measured_command = [str(time_program), "-f", resource_format, "-o", str(resource_path), "--", *command]
        measurement = "GNU time"
    started = time.monotonic()
    with stdout_path.open("w") as output, stderr_path.open("w") as errors:
        process = subprocess.Popen(
            measured_command, env=environment, stdout=output, stderr=errors,
            stdin=subprocess.DEVNULL, start_new_session=True,
        )
        try:
            while True:
                identifier, status, usage = os.wait4(process.pid, os.WNOHANG)
                if identifier:
                    process.returncode = os.waitstatus_to_exitcode(status)
                    break
                if time.monotonic() - started > timeout:
                    terminate(process)
                    raise BenchmarkError(f"Video check exceeded its {timeout:g}-second timeout")
                time.sleep(0.05)
        except BaseException:
            if process.returncode is None:
                terminate(process)
            raise
    if measurement == "GNU time":
        records = resource_path.read_text().splitlines()
        try:
            resources = json.loads(records[-1])
        except (IndexError, json.JSONDecodeError) as error:
            raise BenchmarkError("GNU time did not produce a resource record") from error
    else:
        resources = {
            "userSeconds": usage.ru_utime,
            "systemSeconds": usage.ru_stime,
            "elapsedSeconds": time.monotonic() - started,
            "maximumResidentKiB": usage.ru_maxrss,
        }
    elapsed = resources["elapsedSeconds"]
    if elapsed <= 0:
        raise BenchmarkError("The resource measurement reported no elapsed time")
    resources["measurement"] = measurement
    resources["cpuPercentOfOneCore"] = 100 * (resources["userSeconds"] + resources["systemSeconds"]) / elapsed
    write_json(resource_path, resources)
    return process.returncode, resources


def number(value, field):
    if not isinstance(value, (int, float)) or isinstance(value, bool) or not math.isfinite(value):
        raise BenchmarkError(f"Missing or invalid numeric field: {field}")
    return value


def validate_mode(status, width, height, frames_per_second):
    video = status.get("video", {})
    if video.get("format") != "NV12" or video.get("width") != width or video.get("height") != height:
        raise BenchmarkError("The device did not negotiate the requested NV12 dimensions")
    numerator = video.get("intervalNumerator")
    denominator = video.get("intervalDenominator")
    if (not isinstance(numerator, int) or not isinstance(denominator, int)
            or isinstance(numerator, bool) or isinstance(denominator, bool)
            or numerator <= 0 or denominator <= 0
            or Fraction(denominator, numerator) != Fraction(frames_per_second)):
        raise BenchmarkError("The device did not negotiate the requested frame interval")


def validate_status(status, requested_backend, duration, capture_window):
    if not isinstance(status, dict):
        raise BenchmarkError("The application status is not a JSON object")
    video = status.get("video", {})
    actual = status.get("videoBackend", video.get("backend"))
    actual = "dma" if actual == "dmabuf" else actual
    if actual != requested_backend:
        reason = status.get("fallbackReason") or video.get("fallbackReason") or "no fallback reason reported"
        raise BenchmarkError(f"Requested {requested_backend}, but application used {actual}: {reason}")
    if status.get("error") or status.get("videoRunning") is not True:
        raise BenchmarkError(f"Capture did not remain running: {status.get('error', '')}")
    frames = number(status.get("frames"), "frames")
    if frames <= 0 or number(status.get("displayedFrameSequence"), "displayedFrameSequence") <= 0:
        raise BenchmarkError("The application did not display a captured frame")
    if capture_window and (
        status.get("captureWindowDisplayedBitmap") is not True
        or number(status.get("captureWindowRenderCount"), "captureWindowRenderCount") <= 0
    ):
        raise BenchmarkError("The capture window did not render the video")
    contexts = status.get("gpuTimings", {}).get("contexts", [])
    if not contexts:
        raise BenchmarkError("The GPU completion collector returned no samples after warmup")
    timings = []
    roles = status.get("gpuContextRoles", {})
    role_counts = {}
    for ordinal, context in enumerate(sorted(contexts, key=lambda item: item["context"]), start=1):
        role = roles.get(str(context["context"]))
        if role not in ("main", "capture"):
            raise BenchmarkError("The application did not identify a GPU context's window role")
        role_counts[role] = role_counts.get(role, 0) + 1
        samples = number(context.get("samples"), "GPU samples")
        if samples < 2:
            raise BenchmarkError("A rendering context has fewer than two GPU completion samples")
        timings.append({
            "contextOrdinal": ordinal,
            "windowRole": role,
            "roleContextOrdinal": role_counts[role],
            "sourceContextID": context["context"],
            "samples": samples,
            "observedFPSAfterWarmup": number(context.get("observedFps"), "observedFps"),
            "textureReadyP50Ms": number(context.get("captureToTextureReadyP50Ns"), "texture-ready p50") / 1e6,
            "textureReadyP95Ms": number(context.get("captureToTextureReadyP95Ns"), "texture-ready p95") / 1e6,
            "submitP50Ms": number(context.get("captureToSubmitP50Ns"), "submit p50") / 1e6,
            "submitP95Ms": number(context.get("captureToSubmitP95Ns"), "submit p95") / 1e6,
        })
    expected_roles = {"main", "capture"} if capture_window else {"main"}
    if set(role_counts) != expected_roles:
        raise BenchmarkError("GPU timing samples are missing a requested window role")
    return {
        "actualBackend": actual,
        "capturedFrames": frames,
        "softwareDroppedFrames": number(status.get("droppedFrames", 0), "droppedFrames"),
        "captureAverageFPSIncludingStartup": frames / duration,
        "contexts": timings,
    }


def summarize(runs):
    summary = {}
    for backend in ("cpu", "dma"):
        selected = [run for run in runs if run.get("requestedBackend") == backend and run.get("success")]
        if not selected:
            continue
        contexts = {}
        for run in selected:
            for context in run["metrics"]["contexts"]:
                key = (context["windowRole"], context["roleContextOrdinal"])
                contexts.setdefault(key, []).append(context)
        summary[backend] = {
            "runs": len(selected),
            "medianCPUPercentOfOneCore": statistics.median(run["resources"]["cpuPercentOfOneCore"] for run in selected),
            "maximumResidentKiB": max(run["resources"]["maximumResidentKiB"] for run in selected),
            "medianCaptureAverageFPSIncludingStartup": statistics.median(run["metrics"]["captureAverageFPSIncludingStartup"] for run in selected),
            "contexts": [{
                "windowRole": role,
                "roleContextOrdinal": ordinal,
                "runs": len(values),
                "medianObservedFPSAfterWarmup": statistics.median(item["observedFPSAfterWarmup"] for item in values),
                "medianRunTextureReadyP50Ms": statistics.median(item["textureReadyP50Ms"] for item in values),
                "medianRunTextureReadyP95Ms": statistics.median(item["textureReadyP95Ms"] for item in values),
                "minimumRunTextureReadyP95Ms": min(item["textureReadyP95Ms"] for item in values),
                "maximumRunTextureReadyP95Ms": max(item["textureReadyP95Ms"] for item in values),
                "medianRunSubmitP50Ms": statistics.median(item["submitP50Ms"] for item in values),
                "medianRunSubmitP95Ms": statistics.median(item["submitP95Ms"] for item in values),
            } for (role, ordinal), values in sorted(contexts.items())],
        }
    if "cpu" in summary and "dma" in summary:
        cpu, dma = summary["cpu"], summary["dma"]
        cpu_usage = cpu["medianCPUPercentOfOneCore"]
        comparison = {
            "cpuReductionPercent": 100 * (1 - dma["medianCPUPercentOfOneCore"] / cpu_usage) if cpu_usage else None,
            "contextComparisons": [],
        }
        cpu_contexts = {(item["windowRole"], item["roleContextOrdinal"]): item for item in cpu["contexts"]}
        for context in dma["contexts"]:
            previous = cpu_contexts.get((context["windowRole"], context["roleContextOrdinal"]))
            if previous and context["runs"] == dma["runs"] and previous["runs"] == cpu["runs"]:
                comparison["contextComparisons"].append({
                    "windowRole": context["windowRole"],
                    "roleContextOrdinal": context["roleContextOrdinal"],
                    "textureReadyP50DeltaMs": context["medianRunTextureReadyP50Ms"] - previous["medianRunTextureReadyP50Ms"],
                    "textureReadyP95DeltaMs": context["medianRunTextureReadyP95Ms"] - previous["medianRunTextureReadyP95Ms"],
                })
        summary["dmaComparedWithCPU"] = comparison
    return summary


def parse_arguments():
    root = Path(__file__).resolve().parent.parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--device", default="/dev/video0")
    parser.add_argument("--binary", type=Path, default=root / "build/mygoframe")
    parser.add_argument("--seconds", type=float, default=30, help="UI duration per run; must exceed the two-second timing warmup")
    parser.add_argument("--repeats", type=int, default=3, help="Number of CPU/DMA pairs")
    parser.add_argument("--width", type=int, default=3840)
    parser.add_argument("--height", type=int, default=2160)
    parser.add_argument("--fps", default="60")
    parser.add_argument("--color-range", choices=("auto", "limited", "full"), default="auto")
    parser.add_argument("--window-width", type=int, default=1280)
    parser.add_argument("--window-height", type=int, default=800)
    parser.add_argument("--capture-window", action="store_true")
    parser.add_argument("--capture-window-cycles", type=int, default=0)
    parser.add_argument("--first-backend", choices=("cpu", "dma"), default="cpu")
    parser.add_argument("--settle-seconds", type=float, default=1)
    parser.add_argument("--timeout-margin", type=float, default=30)
    parser.add_argument("--output", type=Path, default=root / "build/benchmarks/video.json")
    arguments = parser.parse_args()
    if not math.isfinite(arguments.seconds) or arguments.seconds <= 2 or arguments.repeats <= 0:
        parser.error("--seconds must exceed 2 and --repeats must be positive")
    if min(arguments.width, arguments.height, arguments.window_width, arguments.window_height) <= 0:
        parser.error("video and window dimensions must be positive")
    if arguments.width % 2 or arguments.height % 2:
        parser.error("NV12 dimensions must be even")
    try:
        if Fraction(arguments.fps) <= 0:
            raise ValueError
    except (ValueError, ZeroDivisionError):
        parser.error("--fps must be a positive decimal or fraction")
    if not math.isfinite(arguments.settle_seconds) or arguments.settle_seconds < 0:
        parser.error("--settle-seconds must be finite and nonnegative")
    if not math.isfinite(arguments.timeout_margin) or arguments.timeout_margin <= 0:
        parser.error("--timeout-margin must be finite and positive")
    if arguments.capture_window_cycles < 0 or (arguments.capture_window_cycles and not arguments.capture_window):
        parser.error("--capture-window-cycles requires --capture-window and a nonnegative count")
    arguments.binary = arguments.binary.resolve()
    arguments.output = arguments.output.resolve()
    if not arguments.binary.is_file() or not os.access(arguments.binary, os.X_OK):
        parser.error(f"application executable not found: {arguments.binary}")
    return arguments


def main():
    arguments = parse_arguments()
    environment = os.environ.copy()
    environment["LC_ALL"] = "C"
    logs = arguments.output.parent / (arguments.output.stem + "-runs")
    logs.mkdir(parents=True, exist_ok=True)
    report = {
        "schemaVersion": 2,
        "startedAtUTC": datetime.now(timezone.utc).isoformat(),
        "binary": str(arguments.binary),
        "binarySHA256": hashlib.sha256(arguments.binary.read_bytes()).hexdigest(),
        "configuration": {key: str(value) if isinstance(value, Path) else value for key, value in vars(arguments).items()},
        "measurementNotes": [
            "CPU percentage is process user plus system time divided by elapsed wall time; 100% represents one CPU core.",
            "Capture average FPS includes startup; texture-ready observed FPS excludes the application's timing warmup.",
            "Latency is capture timestamp to GPU texture readiness, not input-to-display or scanout latency.",
            "Aggregates are medians of per-run percentiles, not percentiles of pooled raw samples.",
            "Window roles come from native window/context observations; context creation order does not identify a window.",
        ],
        "runs": [],
    }
    backends = ("cpu", "dma") if arguments.first_backend == "cpu" else ("dma", "cpu")
    try:
        for pair in range(1, arguments.repeats + 1):
            for backend in backends:
                name = f"{pair:02d}-{backend}"
                command = [
                    str(arguments.binary), "--smoke-ui-video", arguments.device,
                    "--format", "NV12", "--width", str(arguments.width), "--height", str(arguments.height),
                    "--fps", arguments.fps, "--color-range", arguments.color_range,
                    "--video-backend", backend, "--measure-latency",
                    "--window-width", str(arguments.window_width), "--window-height", str(arguments.window_height),
                    "--smoke-ui-seconds", str(arguments.seconds),
                ]
                if arguments.capture_window:
                    command.extend(["--capture-window", "--smoke-capture-window-cycles", str(arguments.capture_window_cycles)])
                run = {"pair": pair, "requestedBackend": backend, "command": command, "success": False}
                report["runs"].append(run)
                stdout_path, stderr_path, resource_path = (logs / f"{name}.{suffix}" for suffix in ("stdout.json", "stderr.log", "resources.json"))
                run["artifacts"] = {"stdout": str(stdout_path), "stderr": str(stderr_path), "resources": str(resource_path)}
                print(f"Running {backend} video check {pair}/{arguments.repeats}", file=sys.stderr, flush=True)
                result, resources = run_process(command, environment, arguments.seconds + arguments.timeout_margin, stdout_path, stderr_path, resource_path)
                run["exitCode"], run["resources"] = result, resources
                if result != 0:
                    raise BenchmarkError(f"{backend} video check exited with status {result}; see {stderr_path}")
                try:
                    status = json.loads(stdout_path.read_text())
                except json.JSONDecodeError as error:
                    raise BenchmarkError(f"Application did not return JSON; see {stdout_path}") from error
                run["status"] = status
                run["metrics"] = validate_status(status, backend, arguments.seconds, arguments.capture_window)
                validate_mode(status, arguments.width, arguments.height, arguments.fps)
                run["success"] = True
                report["summary"] = summarize(report["runs"])
                write_json(arguments.output, report)
                time.sleep(arguments.settle_seconds)
    except (BenchmarkError, OSError, KeyboardInterrupt) as error:
        report["error"] = str(error) or "Benchmark interrupted"
        report["summary"] = summarize(report["runs"])
        report["completed"] = False
        write_json(arguments.output, report)
        print(report["error"], file=sys.stderr)
        return 130 if isinstance(error, KeyboardInterrupt) else 1
    report["completed"] = True
    report["finishedAtUTC"] = datetime.now(timezone.utc).isoformat()
    write_json(arguments.output, report)
    print(json.dumps(report["summary"], indent=2, allow_nan=False))
    print(f"Full benchmark report: {arguments.output}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
