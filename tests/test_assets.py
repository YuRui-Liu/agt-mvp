from pathlib import Path
import struct
import unittest
import xml.etree.ElementTree as ET


ROOT = Path(__file__).resolve().parents[1]


class AitiAssetTests(unittest.TestCase):
    def test_poster_is_nonempty_png(self):
        poster = ROOT / "assets" / "poster.png"
        data = poster.read_bytes()
        self.assertGreater(len(data), 8)
        self.assertEqual(data[:8], b"\x89PNG\r\n\x1a\n")
        self.assertEqual(data[12:16], b"IHDR")
        width, height = struct.unpack(">II", data[16:24])
        self.assertGreater(width, 0)
        self.assertGreater(height, 0)
        self.assertEqual(data[-12:-8], b"\x00\x00\x00\x00")
        self.assertEqual(data[-8:-4], b"IEND")

    def test_qr_is_local_svg_with_aiti_branding(self):
        qr = ROOT / "assets" / "aiti-qr.svg"
        data = qr.read_text(encoding="utf-8")
        self.assertIn("<svg", data)
        self.assertIn("AITI", data)
        self.assertNotIn("KwAITI", data)
        self.assertEqual(ET.fromstring(data).tag, "{http://www.w3.org/2000/svg}svg")


if __name__ == "__main__":
    unittest.main()
