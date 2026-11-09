"""Check diagnostic source bytes independently of device and GPU availability."""

import ctypes
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parent.parent
SPECIFICATION = importlib.util.spec_from_file_location("dma_colors", ROOT / "tools/validate-dma-colors.py")
COLORS = importlib.util.module_from_spec(SPECIFICATION)
SPECIFICATION.loader.exec_module(COLORS)


class DmaPatternTests(unittest.TestCase):
    def test_packed_channels_and_p010_words_preserve_padding(self):
        for pixel_format, stride, offset, length, expected in [
            ("NV12", 4, 8, 12, bytes([64, 47, 0xA5, 0xA5] * 2 + [128, 192, 0xA5, 0xA5])),
            ("P010", 6, 12, 18, bytes([0x40, 0x10, 0, 0x0C, 0xA5, 0xA5] * 2 + [0x40, 0x20, 0x40, 0x30, 0xA5, 0xA5])),
            ("YUYV", 6, 0, 12, bytes([64, 128, 47, 192, 0xA5, 0xA5] * 2)),
            ("UYVY", 6, 0, 12, bytes([128, 64, 192, 47, 0xA5, 0xA5] * 2)),
            ("BGR3", 8, 0, 16, bytes([192, 128, 64] * 2 + [0xA5, 0xA5]) * 2),
            ("RGB3", 8, 0, 16, bytes([64, 128, 192] * 2 + [0xA5, 0xA5]) * 2),
        ]:
            with self.subTest(pixel_format=pixel_format), tempfile.TemporaryFile() as source:
                source.write(bytes([0xA5]) * length)
                source.flush()
                view = COLORS.DmaFrameView(abi_version=1, struct_size=ctypes.sizeof(COLORS.DmaFrameView),
                    descriptor=source.fileno(), width=2, height=2, stride=stride, uv_offset=offset,
                    byte_length=length, fourcc=int.from_bytes(pixel_format.encode("ascii"), "little"))
                values = [65, 129, 193] if pixel_format == "P010" else [64, 128, 192]
                with patch.object(COLORS, "synchronize"):
                    COLORS.write_pattern(view, [{"name": "gray-1", "bounds": [0, 0, 2, 2], "source": values, "alternateLuma": pixel_format not in ("RGB3", "BGR3")}])
                source.seek(0)
                self.assertEqual(source.read(), expected)

    def test_ten_bit_patterns_include_low_bits_and_full_range_endpoints(self):
        view = COLORS.DmaFrameView(width=640, height=480, color_range=2, encoding=2,
                                  fourcc=int.from_bytes(b"P010", "little"))
        patches = COLORS.pattern_patches(view)
        self.assertEqual(patches[0]["source"], [1023, 512, 512])
        self.assertEqual(patches[7]["source"], [0, 512, 512])
        low_bits = {value & 3 for item in patches for value in item["source"]}
        self.assertEqual(low_bits, {0, 1, 2, 3})


if __name__ == "__main__":
    unittest.main()
