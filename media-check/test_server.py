import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time
import unittest
from unittest.mock import patch
import urllib.error
import urllib.request

import server


class MediaCheckTest(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory(prefix="media-check-test-")
        self.addCleanup(self.folder.cleanup)
        self.root = Path(self.folder.name)
        self.original_root = server.MEDIA_ROOT
        server.MEDIA_ROOT = self.root
        self.addCleanup(setattr, server, "MEDIA_ROOT", self.original_root)

    def test_real_video_eight_uniform_frames_and_integrity(self):
        subprocess.run([
            "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=12",
            "-t", "6", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "1",
            "-pix_fmt", "yuv420p", str(self.root / "sample.mp4"),
        ], check=True, timeout=30)
        started = time.monotonic()
        result = server.inspect_video("sample.mp4")
        self.assertTrue(result["valid"])
        self.assertEqual((result["width"], result["height"]), (1280, 720))
        self.assertAlmostEqual(result["duration"], 6, places=1)
        self.assertEqual(len(result["frames"]), 8)
        self.assertEqual(len(set(result["frames"])), 8)
        self.assertLess(time.monotonic() - started, server.DEADLINE_SECONDS)

    def test_corruption_rejected(self):
        (self.root / "bad.mp4").write_bytes(b"\0\0\0\x18ftypisom" + b"broken" * 10)
        self.assertEqual(server.inspect_video("bad.mp4"), {"valid": False})

    def test_paths_and_symlinks_rejected(self):
        for name in ("../secret.mp4", "/etc/secret.mp4", "a/b.mp4", "https://example.com/a.mp4", "bad\\path.mp4"):
            with self.assertRaises(ValueError):
                server.inspect_video(name)
        (self.root / "link.mp4").symlink_to("/etc/passwd")
        with self.assertRaises(OSError):
            server.inspect_video("link.mp4")

    def test_deadline_does_not_report_corruption(self):
        (self.root / "timeout.mp4").write_bytes(b"\0" * 20)
        with patch.object(server, "run_process", side_effect=TimeoutError()):
            with self.assertRaises(TimeoutError):
                server.inspect_video("timeout.mp4")

    def test_subprocess_protocols_and_file_descriptor(self):
        (self.root / "sample.mp4").write_bytes(b"\0" * 20)
        calls = []

        def fake_process(command, fd, deadline, limit):
            calls.append(command)
            self.assertTrue(os.fstat(fd))
            self.assertIn("-protocol_whitelist", command)
            self.assertEqual(command[command.index("-protocol_whitelist") + 1], "file,pipe")
            if command[0] == "ffprobe":
                return json.dumps({"streams": [{"width": 10, "height": 10}], "format": {"duration": 6}}).encode()
            if "image2pipe" in command:
                return b"\xff\xd8frame\xff\xd9"
            return b""

        with patch.object(server, "run_process", side_effect=fake_process):
            self.assertTrue(server.inspect_video("sample.mp4")["valid"])
        timestamps = [float(c[c.index("-ss") + 1]) for c in calls if "-ss" in c]
        self.assertEqual(timestamps, [6 * (i + 0.5) / 8 for i in range(8)])

    def test_http_authentication_and_request_validation(self):
        with patch.object(server, "TOKEN", "test-only-token"):
            httpd = server.HTTPServer(("127.0.0.1", 0), server.Handler)
            thread = threading.Thread(target=httpd.serve_forever, daemon=True)
            thread.start()
            try:
                origin = f"http://127.0.0.1:{httpd.server_port}"
                with urllib.request.urlopen(origin + "/healthz", timeout=2) as response:
                    self.assertEqual(response.status, 200)
                for token, body, status in (("wrong", {"name": "test.mp4"}, 401),
                                            ("test-only-token", {"name": "../private.mp4"}, 400),
                                            ("test-only-token", {"name": "test.mp4", "url": "https://example.com"}, 400)):
                    request = urllib.request.Request(origin + "/inspect", data=json.dumps(body).encode(),
                                                     headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
                    with self.assertRaises(urllib.error.HTTPError) as result:
                        urllib.request.urlopen(request, timeout=2)
                    self.assertEqual(result.exception.code, status)
            finally:
                httpd.shutdown()
                httpd.server_close()
                thread.join(timeout=2)


if __name__ == "__main__":
    unittest.main()
