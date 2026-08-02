from pathlib import Path
import struct
import unittest


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

if __name__ == "__main__":
    unittest.main()
