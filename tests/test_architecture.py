"""Verify architectural violations are rejected, including synthetic regressions."""

import importlib.util
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent
SPECIFICATION = importlib.util.spec_from_file_location("architecture", ROOT / "tools/check-architecture.py")
ARCHITECTURE = importlib.util.module_from_spec(SPECIFICATION)
SPECIFICATION.loader.exec_module(ARCHITECTURE)


class ArchitectureTests(unittest.TestCase):
    def test_current_source_boundaries(self):
        count, errors = ARCHITECTURE.check()
        self.assertGreater(count, 50)
        self.assertEqual(errors, [])

    def test_go_application_cannot_import_its_native_adapter(self):
        text = 'package application\nimport "github.com/TomoriLab/MyGOFrame/src/internal/session/infrastructure/native"'
        self.assertTrue(ARCHITECTURE.check_file(Path("src/internal/session/application/service.go"), text))

    def test_go_presentation_can_use_ports_but_not_adapters(self):
        path = Path("src/internal/desktop/presentation/view.go")
        for layer, rejected in [("application", False), ("domain", False), ("infrastructure/native", True)]:
            text = f'package presentation\nimport "{ARCHITECTURE.MODULE}session/{layer}"'
            self.assertEqual(bool(ARCHITECTURE.check_file(path, text)), rejected)

    def test_swift_domain_cannot_discover_paths_or_use_ffi(self):
        path = Path("src/swift/presets/domain/Preset.swift")
        self.assertTrue(ARCHITECTURE.check_file(path, "let path = FileManager.default.homeDirectoryForCurrentUser"))
        self.assertTrue(ARCHITECTURE.check_file(path, "import NativeCore\nmgfr_video_create()"))
        self.assertEqual(ARCHITECTURE.check_file(path, 'let message = "FileManager" // NativeCore'), [])

    def test_swift_application_cannot_select_a_concrete_adapter(self):
        path = Path("src/swift/session/application/State.swift")
        self.assertTrue(ARCHITECTURE.check_file(path, "let runtime = NativeMediaRuntime()", {"NativeMediaRuntime"}))

    def test_rust_domain_cannot_depend_on_ffi_or_infrastructure(self):
        path = Path("src/rust/src/capture/domain/layout.rs")
        self.assertTrue(ARCHITECTURE.check_file(path, "use crate::ffi;"))
        self.assertTrue(ARCHITECTURE.check_file(path, "use crate::capture::infrastructure::v4l2;"))
        self.assertEqual(ARCHITECTURE.check_file(path, "fn bounded(width:u32)->bool { width <= 16384 }"), [])

    def test_capture_does_not_depend_on_recording_implementation(self):
        path = Path("src/rust/src/audio/infrastructure/pipewire.rs")
        self.assertTrue(ARCHITECTURE.check_file(path, "use crate::recording::infrastructure::input;"))


if __name__ == "__main__":
    unittest.main()
