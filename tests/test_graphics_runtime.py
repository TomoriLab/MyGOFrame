"""Verify graphics policy behavior before any vendor library is loaded."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent
SPECIFICATION = importlib.util.spec_from_file_location("dma_colors", ROOT / "tools/validate-dma-colors.py")
COLORS = importlib.util.module_from_spec(SPECIFICATION)
SPECIFICATION.loader.exec_module(COLORS)


class GraphicsRuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)

    def manifest(self, name, library):
        path = self.directory / name
        path.write_text(json.dumps({"file_format_version": "1.0.0", "ICD": {"library_path": library}}))
        return str(path)

    def nvidia_manifests(self):
        modern = self.manifest("09-modern.json", "libnvidia-egl-wayland2.so.1")
        legacy = self.manifest("10-legacy.json", "/opt/lib/libnvidia-egl-wayland.so.1")
        other = self.manifest("15-gbm.json", "libnvidia-egl-gbm.so.1")
        return modern, legacy, other

    def prepare(self, environment):
        return COLORS.prepare_graphics_runtime(environment, [self.directory], [], "615.78.08", [])

    def test_default_avoids_only_the_verified_nvidia_allocation_paths(self):
        _, legacy, other = self.nvidia_manifests()
        environment = {"WAYLAND_DISPLAY": "wayland-1"}
        changes = self.prepare(environment)
        self.assertEqual(changes["__GL_DisableDBus"], "1")
        self.assertEqual(changes["__EGL_EXTERNAL_PLATFORM_CONFIG_FILENAMES"], ":".join([legacy, other]))

    def test_explicit_overrides_including_empty_values_are_preserved(self):
        self.nvidia_manifests()
        for name in ("__EGL_EXTERNAL_PLATFORM_CONFIG_FILENAMES", "__EGL_EXTERNAL_PLATFORM_CONFIG_DIRS"):
            for value in ("", "/custom/platforms"):
                environment = {"WAYLAND_DISPLAY": "wayland-1", name: value, "__GL_DisableDBus": "0"}
                original = environment.copy()
                self.assertEqual(self.prepare(environment), {})
                self.assertEqual(environment, original)

    def test_explicit_backend_takes_precedence_over_session(self):
        self.nvidia_manifests()
        for backend, selected in (("wayland,x11", True), ("x11,wayland", False), ("broadway", False), ("*", True)):
            environment = {"GDK_BACKEND": backend, "WAYLAND_DISPLAY": "wayland-1"}
            changes = self.prepare(environment)
            self.assertEqual("__EGL_EXTERNAL_PLATFORM_CONFIG_FILENAMES" in changes, selected)

    def test_non_nvidia_and_absent_manifests_do_not_change_environment(self):
        self.assertEqual(self.prepare({"WAYLAND_DISPLAY": "wayland-1"}), {})
        self.manifest("mesa.json", "libmesa-platform.so.1")
        self.assertEqual(self.prepare({"WAYLAND_DISPLAY": "wayland-1"}), {})

    def test_missing_legacy_keeps_available_backend(self):
        self.manifest("09-modern.json", "libnvidia-egl-wayland2.so.1")
        self.assertEqual(self.prepare({"WAYLAND_DISPLAY": "wayland-1"}), {"__GL_DisableDBus": "1"})

    def test_invalid_manifests_leave_environment_intact(self):
        self.nvidia_manifests()
        for document in ("{", "[]", '{"ICD":{"library_path":null}}'):
            (self.directory / "broken.json").write_text(document)
            environment = {"WAYLAND_DISPLAY": "wayland-1"}
            with self.assertRaises(RuntimeError):
                self.prepare(environment)
            self.assertEqual(environment, {"WAYLAND_DISPLAY": "wayland-1"})

    def test_shared_go_and_python_policy_fixtures(self):
        fixtures = json.loads((ROOT / "src/internal/platform/infrastructure/graphics/testdata/policy-cases.json").read_text())
        for fixture in fixtures:
            with self.subTest(fixture=fixture["name"]), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                for manifest in fixture.get("manifests", []):
                    path = root / manifest["path"]
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_text(json.dumps({"file_format_version": "1.0.0",
                                                "ICD": {"library_path": manifest["library"]}}))
                for relative, data in fixture.get("invalidFiles", {}).items():
                    path = root / relative
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_text(data)
                for relative in fixture.get("runtimeFiles", []):
                    path = root / relative
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.touch()
                environment = fixture.get("environment", {}).copy()
                original = environment.copy()
                locations = [root / "etc/egl/egl_external_platform.d", root / "usr/share/egl/egl_external_platform.d"]
                vendors = [root / "etc/glvnd/egl_vendor.d", root / "usr/share/glvnd/egl_vendor.d"]
                runtime = [root / "usr/lib", root / "usr/lib64", root / "lib", root / "lib64"]
                if fixture.get("errorContains"):
                    with self.assertRaisesRegex(RuntimeError, fixture["errorContains"]):
                        COLORS.prepare_graphics_runtime(environment, locations, vendors,
                                                        fixture.get("driverVersion", "615.78.08"), runtime)
                    self.assertEqual(environment, original)
                else:
                    expected = {key: value.replace("${ROOT}", str(root))
                                for key, value in fixture.get("expectedChanges", {}).items()}
                    self.assertEqual(COLORS.prepare_graphics_runtime(environment, locations, vendors,
                                                                    fixture.get("driverVersion", "615.78.08"), runtime), expected)
                    self.assertEqual(environment, original | expected)


if __name__ == "__main__":
    unittest.main()
