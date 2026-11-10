"""Verify full offline notices and safe Go source generation."""

import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent
SPECIFICATION = importlib.util.spec_from_file_location("about_catalog", ROOT / "tools/prepare-about.py")
ABOUT = importlib.util.module_from_spec(SPECIFICATION)
SPECIFICATION.loader.exec_module(ABOUT)


class AboutCatalogTests(unittest.TestCase):
    def test_go_literal_preserves_bom_astral_characters_and_escapes(self):
        original = '\ufeffCopyright \U0001f3b5 "quoted" \\ backtick `\nnext line'
        literal = ABOUT.go_string(original)
        self.assertNotIn("\ufeff", literal)
        self.assertNotIn("\\ud83c", literal)
        self.assertEqual(json.loads(literal), original)

    def test_notice_keeps_exact_text_and_source_hash(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "LICENSE"
            data = b"Copyright holder\nPermission text.\n"
            path.write_bytes(data)
            catalog = ABOUT.Catalog()
            identifier = catalog.document(path, "module/LICENSE")
            document = catalog.documents[identifier]
            self.assertEqual(document["text"], data.decode())
            self.assertEqual(document["sha256"], hashlib.sha256(data).hexdigest())
            self.assertEqual(catalog.document(path, "duplicate/LICENSE"), identifier)
            self.assertEqual(len(catalog.documents), 1)

    def test_html_notices_keep_text_without_executable_markup(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "COPYRIGHT-library.html"
            path.write_text("<h1>Attribution</h1><script>hidden()</script><pre>MIT &amp; Apache\nfull text</pre>")
            catalog = ABOUT.Catalog()
            document = catalog.documents[catalog.document(path, "Rust copyright")]
            self.assertIn("MIT & Apache\nfull text", document["text"])
            self.assertNotIn("hidden()", document["text"])

    def test_debian_references_include_full_common_license(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "copyright"
            path.write_text("License: GPL-2+\n See /usr/share/common-licenses/GPL-2.\n")
            paths = ABOUT.Catalog().common_licenses([(path, "test copyright")])
            self.assertIn(Path("/usr/share/common-licenses/GPL-2"), [path for path, _ in paths])
            self.assertIn("GNU GENERAL PUBLIC LICENSE", paths[1][0].read_text())

    def test_empty_notices_fail_instead_of_creating_a_placeholder(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "LICENSE"
            path.write_text("\n")
            with self.assertRaisesRegex(ValueError, "Empty license"):
                ABOUT.Catalog().document(path, "empty")


if __name__ == "__main__":
    unittest.main()
