from pathlib import Path
import unittest
import xml.etree.ElementTree as ET


ROOT = Path(__file__).resolve().parents[1]


class AitiAssetTests(unittest.TestCase):
    def test_poster_is_nonempty_png(self):
        poster = ROOT / "assets" / "poster.png"
        data = poster.read_bytes()
        self.assertGreater(len(data), 8)
        self.assertEqual(data[:8], b"\x89PNG\r\n\x1a\n")

    def test_qr_is_local_svg_with_aiti_branding(self):
        qr = ROOT / "assets" / "aiti-qr.svg"
        data = qr.read_text(encoding="utf-8")
        self.assertIn("<svg", data)
        self.assertIn("AITI", data)
        self.assertNotIn("KwAITI", data)
        self.assertEqual(ET.fromstring(data).tag, "{http://www.w3.org/2000/svg}svg")


if __name__ == "__main__":
    unittest.main()
