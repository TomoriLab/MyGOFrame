"""Verify that timing comparisons identify windows independently of renderer order."""

import importlib.util
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent
SPECIFICATION = importlib.util.spec_from_file_location("video_benchmark", ROOT / "tools/benchmark-video.py")
BENCHMARK = importlib.util.module_from_spec(SPECIFICATION)
SPECIFICATION.loader.exec_module(BENCHMARK)


def status(roles):
    return {
        "videoBackend": "dmabuf", "videoRunning": True, "frames": 600,
        "displayedFrameSequence": 600, "captureWindowDisplayedBitmap": True,
        "captureWindowRenderCount": 600, "gpuContextRoles": roles,
        "gpuTimings": {"contexts": [{
            "context": context, "samples": 500, "observedFps": 60,
            "captureToTextureReadyP50Ns": context * 1_000_000,
            "captureToTextureReadyP95Ns": context * 2_000_000,
            "captureToSubmitP50Ns": context * 1_000_000,
            "captureToSubmitP95Ns": context * 2_000_000,
        } for context in [1, 2]]},
    }


class VideoBenchmarkTests(unittest.TestCase):
    def test_mode_validation_requires_the_requested_raw_format(self):
        for pixel_format in ("NV12", "P010", "YUYV", "UYVY", "BGR3", "RGB3"):
            value = {"video": {"format": pixel_format, "width": 640, "height": 480,
                               "intervalNumerator": 1, "intervalDenominator": 60}}
            BENCHMARK.validate_mode(value, 640, 480, "60", pixel_format)
            with self.assertRaises(BENCHMARK.BenchmarkError):
                BENCHMARK.validate_mode(value, 640, 480, "60", "MJPG")

    def test_window_roles_do_not_follow_context_creation_order(self):
        metrics = BENCHMARK.validate_status(status({"1": "capture", "2": "main"}), "dma", 10, True)
        self.assertEqual([(item["sourceContextID"], item["windowRole"]) for item in metrics["contexts"]],
                         [(1, "capture"), (2, "main")])

    def test_unidentified_or_missing_window_roles_reject_comparison(self):
        for roles in ({}, {"1": "main"}, {"1": "main", "2": "main"}):
            with self.assertRaises(BENCHMARK.BenchmarkError):
                BENCHMARK.validate_status(status(roles), "dma", 10, True)

    def test_summary_keeps_roles_when_context_order_changes_between_runs(self):
        runs = []
        for backend in ("cpu", "dma"):
            for roles in ({"1": "main", "2": "capture"}, {"1": "capture", "2": "main"}):
                value = status(roles)
                value["videoBackend"] = "cpu" if backend == "cpu" else "dmabuf"
                metrics = BENCHMARK.validate_status(value, backend, 10, True)
                for context in metrics["contexts"]:
                    context["observedFPSAfterWarmup"] = 60 if context["windowRole"] == "main" else 30
                runs.append({"success": True, "requestedBackend": backend, "metrics": metrics,
                             "resources": {"cpuPercentOfOneCore": 100, "maximumResidentKiB": 1000}})
        summary = BENCHMARK.summarize(runs)
        for backend in ("cpu", "dma"):
            observed = {item["windowRole"]: item["medianObservedFPSAfterWarmup"]
                        for item in summary[backend]["contexts"]}
            self.assertEqual(observed, {"main": 60, "capture": 30})
        self.assertEqual(len(summary["dmaComparedWithCPU"]["contextComparisons"]), 2)


if __name__ == "__main__":
    unittest.main()
