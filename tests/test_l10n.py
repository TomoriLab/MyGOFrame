"""Validate translator inputs and reproducible embedding without native dependencies."""

from concurrent.futures import ThreadPoolExecutor
import importlib.util
import json
from pathlib import Path
import shutil
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
SPECIFICATION = importlib.util.spec_from_file_location("localization_build", ROOT / "tools/prepare-l10n.py")
LOCALIZATION = importlib.util.module_from_spec(SPECIFICATION)
SPECIFICATION.loader.exec_module(LOCALIZATION)


class LocalizationTests(unittest.TestCase):
    def temporary_catalogs(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        root = Path(directory.name)
        shutil.copytree(ROOT / "l10n", root / "l10n")
        return root

    def test_shipped_catalogs_have_identical_keys(self):
        catalogs = LOCALIZATION.catalogs()
        self.assertTrue({"en", "ko", "ja"}.issubset(catalogs))
        for tag, catalog in catalogs.items():
            self.assertEqual(catalog["@@locale"], tag)

    def test_missing_and_extra_messages_fail(self):
        for operation in ("missing", "extra"):
            with self.subTest(operation=operation):
                root = self.temporary_catalogs()
                path = root / "l10n/ko.json"
                data = json.loads(path.read_text())
                if operation == "missing":
                    del data["Settings"]
                else:
                    data["orphan message"] = "추가 메시지"
                path.write_text(json.dumps(data))
                with self.assertRaisesRegex(ValueError, "Catalog keys differ"):
                    LOCALIZATION.catalogs(root)

    def test_duplicate_and_empty_messages_fail(self):
        with self.assertRaisesRegex(ValueError, "Duplicate"):
            LOCALIZATION.unique_object([("Settings", "Settings"), ("Settings", "duplicate")])
        root = self.temporary_catalogs()
        path = root / "l10n/ja.json"
        data = json.loads(path.read_text())
        data["Settings"] = " "
        path.write_text(json.dumps(data))
        with self.assertRaisesRegex(ValueError, "Empty"):
            LOCALIZATION.catalogs(root)

    def test_parallel_preparation_and_stale_file_removal(self):
        root = self.temporary_catalogs()
        with ThreadPoolExecutor(max_workers=4) as executor:
            list(executor.map(lambda _: LOCALIZATION.prepare(root), range(8)))
        destination = root / LOCALIZATION.DESTINATION
        for tag, data in LOCALIZATION.catalogs(root).items():
            self.assertEqual(json.loads((destination / (tag + ".json")).read_text()), data)
        (destination / "stale.json").write_text("{}")
        LOCALIZATION.prepare(root)
        self.assertFalse((destination / "stale.json").exists())

    def test_new_language_is_discovered_without_source_changes(self):
        root = self.temporary_catalogs()
        data = json.loads((root / "l10n/en.json").read_text())
        data["@@locale"], data["@@name"] = "fr", "Français"
        (root / "l10n/fr.json").write_text(json.dumps(data))
        LOCALIZATION.prepare(root)
        self.assertTrue((root / LOCALIZATION.DESTINATION / "fr.json").exists())



if __name__ == "__main__":
    unittest.main()
