import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest import mock


specification = importlib.util.spec_from_file_location(
    "native_runtime_build", Path(__file__).resolve().parents[1] / "tools/prepare-native-runtime.py"
)
runtime = importlib.util.module_from_spec(specification)
specification.loader.exec_module(runtime)


class NativeRuntimeBuildTests(unittest.TestCase):
    def test_corrupt_offline_archive_is_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "source.tar.xz").write_bytes(b"corrupt")
            source = {"archive": "source.tar.xz", "sha256": hashlib.sha256(b"expected").hexdigest()}
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                runtime.source_archive(source, root, offline=True)

    def test_failed_download_never_publishes_archive(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = {"archive": "source.tar.xz", "sha256": hashlib.sha256(b"expected").hexdigest(), "url": "https://example.invalid/source"}
            with mock.patch.object(runtime, "ROOT", root), mock.patch.object(runtime.urllib.request, "urlopen", return_value=io.BytesIO(b"corrupt")):
                with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                    runtime.source_archive(source, root)
            self.assertFalse((root / source["archive"]).exists())
            self.assertEqual(list(root.iterdir()), [])

    def test_archive_cannot_escape_extraction_directory(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / "unsafe.tar"
            with tarfile.open(archive, "w") as output:
                member = tarfile.TarInfo("../escaped")
                member.size = 3
                output.addfile(member, io.BytesIO(b"bad"))
            with self.assertRaises(tarfile.FilterError):
                runtime.extract_source(archive, root / "source", "expected")
            self.assertFalse((root / "escaped").exists())

    def test_publication_preserves_open_previous_library(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            output, staged = root / "output", root / "staged"
            output.mkdir()
            staged.mkdir()
            (output / "library.so").write_bytes(b"previous")
            module = "gtk-3.0/modules/module.so"
            for directory in (output, staged):
                (directory / module).parent.mkdir(parents=True)
            (output / module).write_bytes(b"previous module")
            (staged / module).write_bytes(b"replacement module")
            (output / "manifest.json").write_text("previous")
            (staged / "library.so").write_bytes(b"replacement")
            (staged / "manifest.json").write_text("replacement")
            with (output / "library.so").open("rb") as previous, (output / module).open("rb") as previous_module:
                runtime.publish_runtime(staged, output, {
                    "libraries": {"gdk": {"file": "library.so"}},
                    "gtkModules": {"colorreload": {"file": module}},
                })
                self.assertEqual(previous.read(), b"previous")
                self.assertEqual(previous_module.read(), b"previous module")
            self.assertEqual((output / "library.so").read_bytes(), b"replacement")
            self.assertEqual((output / module).read_bytes(), b"replacement module")
            self.assertEqual((output / "manifest.json").read_text(), "replacement")

    def test_manifest_refresh_rejects_escaping_library_path(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "manifest.json").write_text(json.dumps({
                "schemaVersion": 1, "libraries": {"gdk": {"file": "../library.so"}}
            }))
            with self.assertRaisesRegex(ValueError, "relative plain filename"):
                runtime.refresh_manifest(root)

    def test_empty_cached_component_list_cannot_skip_build(self):
        lock = {"libraries": {"gtk": {}, "gdk": {}, "fcitx": {}}}
        self.assertFalse(runtime.cached_runtime_valid(
            {"fingerprint": "matching", "libraries": {}}, "matching", Path("/unused"), lock
        ))

    def test_shared_gtk_source_has_one_build_owner(self):
        lock = runtime.read_lock()
        self.assertEqual(set(runtime.source_components(lock)), {"gdk", "fcitx", "kdeGtkModules"})
        self.assertEqual(lock["libraries"]["gtk"]["buildOwner"], "gdk")
        self.assertEqual(lock["libraries"]["gtk"]["soname"], "libgtk-3.so.0")

    def test_unknown_build_owner_is_rejected(self):
        lock = runtime.read_lock()
        lock["libraries"]["gtk"]["buildOwner"] = "missing"
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "lock.json"
            path.write_text(json.dumps(lock))
            with self.assertRaisesRegex(ValueError, "direct build owner"):
                runtime.read_lock(path)

    def test_module_path_rejects_parent_escape(self):
        with tempfile.TemporaryDirectory() as temporary:
            with self.assertRaisesRegex(ValueError, "contained relative path"):
                runtime.module_path(Path(temporary), "../module.so")

    def test_module_path_rejects_symlink_escape(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            inside = root / "runtime"
            inside.mkdir()
            (root / "module.so").write_bytes(b"outside")
            (inside / "module.so").symlink_to(root / "module.so")
            with self.assertRaisesRegex(ValueError, "escapes its manifest directory"):
                runtime.module_path(inside, "module.so")

    def test_optional_module_source_can_be_omitted_together(self):
        lock = runtime.read_lock()
        lock.pop("gtkModules")
        lock.pop("gtkModuleBuild")
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "lock.json"
            path.write_text(json.dumps(lock))
            self.assertEqual(set(runtime.source_components(runtime.read_lock(path))), {"gdk", "fcitx"})

    def test_partial_module_set_is_rejected(self):
        lock = runtime.read_lock()
        lock["gtkModules"].pop("colorreload")
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "lock.json"
            path.write_text(json.dumps(lock))
            with self.assertRaisesRegex(ValueError, "both approved modules"):
                runtime.read_lock(path)

    def test_circular_build_owners_are_rejected(self):
        lock = runtime.read_lock()
        lock["libraries"]["gdk"]["buildOwner"] = "gtk"
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "lock.json"
            path.write_text(json.dumps(lock))
            with self.assertRaisesRegex(ValueError, "direct build owner"):
                runtime.read_lock(path)


if __name__ == "__main__":
    unittest.main()
