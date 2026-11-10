"""Verify synchronized releases, preflight failures, and source-package provenance."""

import importlib.util
from datetime import datetime
import json
import os
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch
import xml.etree.ElementTree as ElementTree

ROOT = Path(__file__).resolve().parent.parent
SPECIFICATION = importlib.util.spec_from_file_location("project_version", ROOT / "tools/version.py")
VERSION = importlib.util.module_from_spec(SPECIFICATION)
SPECIFICATION.loader.exec_module(VERSION)


class VersionTests(unittest.TestCase):
    def test_changelog_timestamp_preserves_local_release_time(self):
        reference = datetime.fromisoformat("2026-11-11T00:11:00+09:00")
        timestamp = VERSION.changelog_time("2026-11-11", reference)
        self.assertEqual(timestamp, reference)
        self.assertEqual(timestamp.utcoffset(), reference.utcoffset())
        self.assertLessEqual(timestamp.timestamp(), reference.timestamp())

    def temporary_source(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        root = Path(directory.name)
        for relative in ("version.json", "debian/changelog", "debian/control", "debian/mygoframe.1",
                         "debian/mygoframe.metainfo.xml", "src/rust/Cargo.toml", "src/rust/Cargo.lock"):
            path = root / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / relative, path)
        return root

    def test_repository_versions_match(self):
        VERSION.check()

    def test_release_bump_preserves_history_and_updates_all_derivatives(self):
        root = self.temporary_source()
        previous = (root / "debian/changelog").read_text()
        VERSION.set_release("1.2.3", 1, "2026-11-10", ["Add localized settings & release metadata."], root)
        self.assertEqual(VERSION.check(root)["version"], "1.2.3")
        self.assertIn(previous, (root / "debian/changelog").read_text())
        releases = ElementTree.parse(root / "debian/mygoframe.metainfo.xml").find("releases")
        self.assertEqual(releases[0].get("version"), "1.2.3")
        self.assertGreater(len(releases), 1)
        self.assertIn("&amp;", (root / "debian/mygoframe.metainfo.xml").read_text())
        self.assertIn('version = "1.2.3"', (root / "src/rust/Cargo.toml").read_text())
        changelog = (root / "debian/changelog").read_bytes()
        VERSION.synchronize(root)
        self.assertEqual((root / "debian/changelog").read_bytes(), changelog)

    def test_packaging_revision_does_not_duplicate_appstream_release(self):
        root = self.temporary_source()
        data = VERSION.read(root)
        count = len(ElementTree.parse(root / "debian/mygoframe.metainfo.xml").find("releases"))
        VERSION.set_release(data["version"], data["debianRevision"]+1, data["releaseDate"], ["Rebuild packaging."], root)
        self.assertEqual(len(ElementTree.parse(root / "debian/mygoframe.metainfo.xml").find("releases")), count)

    def test_prerelease_maps_to_debian_tilde(self):
        root = self.temporary_source()
        VERSION.set_release("1.2.3-rc.1", 2, "2026-11-10", ["Prepare a release candidate."], root)
        self.assertEqual(VERSION.debian_version(VERSION.check(root)), "1.2.3~rc.1-2")

    def test_invalid_versions_and_missing_notes_leave_inputs_unchanged(self):
        for version, revision, notes in [("01.2.3", 1, ["invalid"]), ("1.2.3-01", 1, ["invalid"]),
                                         ("1.2.3", 0, ["invalid"]), ("1.2.3", 1, [])]:
            with self.subTest(version=version, revision=revision, notes=notes):
                root = self.temporary_source()
                originals = {path: path.read_bytes() for path in root.rglob("*") if path.is_file()}
                with self.assertRaises(ValueError):
                    VERSION.set_release(version, revision, "2026-11-10", notes, root)
                self.assertEqual({path: path.read_bytes() for path in originals}, originals)

    def test_write_failure_restores_every_changed_input(self):
        root = self.temporary_source()
        originals = {path: path.read_bytes() for path in root.rglob("*") if path.is_file()}
        write_changed = VERSION.write_changed
        calls = 0

        def fail_once(path, content):
            nonlocal calls
            calls += 1
            if calls == 3:
                raise OSError("Simulated write failure")
            return write_changed(path, content)

        with patch.object(VERSION, "write_changed", side_effect=fail_once):
            with self.assertRaisesRegex(OSError, "Simulated write failure"):
                VERSION.set_release("2.0.0", 1, "2026-11-10", ["Release changes."], root)
        self.assertEqual({path: path.read_bytes() for path in originals}, originals)

    def test_mismatches_fail_build_preflight(self):
        root = self.temporary_source()
        path = root / "src/rust/Cargo.toml"
        path.write_text(path.read_text().replace('version = "'+VERSION.read(root)["version"]+'"', 'version = "9.9.9"'))
        with self.assertRaisesRegex(ValueError, "Cargo.toml"):
            VERSION.generate(root)

    def test_source_snapshot_retains_revision_and_reproducible_epoch(self):
        root = self.temporary_source()
        revision = "a" * 40
        (root / "build-source.json").write_text(json.dumps({"revision": revision, "modified": True}))
        with patch.dict(os.environ, {"SOURCE_DATE_EPOCH": "1794268800"}):
            metadata = VERSION.generate(root)
            initial = (root / VERSION.GENERATED).read_bytes()
            VERSION.generate(root)
        self.assertEqual(initial, (root / VERSION.GENERATED).read_bytes())
        self.assertEqual(metadata["revision"], revision)
        self.assertTrue(metadata["modified"])
        self.assertEqual(metadata["sourceDateEpoch"], 1794268800)
        self.assertIn(b"1794268800", initial)

    def test_build_identity_refreshes_when_git_changes(self):
        root = self.temporary_source()
        (root / ".git").mkdir()
        revision = "a" * 40

        def git_output(arguments, root):
            return revision if "rev-parse" in arguments else ""

        with patch.object(VERSION, "command", side_effect=git_output):
            VERSION.generate(root)
            previous = (root / VERSION.GENERATED).read_bytes()
            revision = "b" * 40
            VERSION.generate(root)
        self.assertNotEqual((root / VERSION.GENERATED).read_bytes(), previous)
        self.assertIn(revision.encode(), (root / VERSION.GENERATED).read_bytes())

    def test_invalid_epoch_is_rejected(self):
        root = self.temporary_source()
        with patch.dict(os.environ, {"SOURCE_DATE_EPOCH": "invalid"}):
            with self.assertRaisesRegex(ValueError, "SOURCE_DATE_EPOCH"):
                VERSION.metadata(root)


if __name__ == "__main__":
    unittest.main()
