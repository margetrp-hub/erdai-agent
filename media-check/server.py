import base64
import hmac
import json
import math
import os
import re
import stat
import subprocess
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

MEDIA_ROOT = Path(os.environ.get("MEDIA_ROOT", "/media"))
TOKEN = os.environ.get("ERDAI_MEDIA_CHECK_TOKEN", "")
MAX_BYTES = 256 * 1024 * 1024
DEADLINE_SECONDS = 35
NAME = re.compile(r"[A-Za-z0-9_-]{1,160}\.mp4\Z")


def run_process(command, fd, deadline, limit):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise TimeoutError("inspection deadline exceeded")
    result = subprocess.run(command, pass_fds=(fd,), stdin=subprocess.DEVNULL,
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                            timeout=remaining, check=True)
    if len(result.stdout) > limit:
        raise ValueError("inspection output exceeds limit")
    return result.stdout


def inspect_video(name):
    if not isinstance(name, str) or not NAME.fullmatch(name):
        raise ValueError("invalid media name")
    # Open once without following links and pass that file descriptor to FFmpeg.
    # Neither submitted paths nor media playlists may reach other files or URLs.
    fd = os.open(MEDIA_ROOT / name, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_size < 12 or info.st_size > MAX_BYTES:
            raise ValueError("invalid media size or type")
        deadline = time.monotonic() + DEADLINE_SECONDS
        source = f"/proc/self/fd/{fd}"
        probe = json.loads(run_process([
            "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-f", "mov", "-enable_drefs", "0", "-use_absolute_path", "0",
            "-select_streams", "v:0", "-show_entries", "stream=width,height:format=duration",
            "-of", "json", source,
        ], fd, deadline, 16384))
        streams = probe.get("streams", [])
        if len(streams) != 1:
            return {"valid": False}
        width, height = int(streams[0]["width"]), int(streams[0]["height"])
        duration = float(probe["format"]["duration"])
        if not math.isfinite(duration) or not 0 < duration <= 120 or not 0 < width <= 4096 or not 0 < height <= 4096:
            return {"valid": False}
        run_process([
            "ffmpeg", "-v", "error", "-xerror", "-nostdin", "-threads", "1",
            "-protocol_whitelist", "file,pipe", "-f", "mov", "-enable_drefs", "0", "-use_absolute_path", "0", "-i", source,
            "-map", "0:v:0", "-an", "-sn", "-dn", "-f", "null", "-",
        ], fd, deadline, 4096)
        frames = []
        for index in range(8):
            timestamp = duration * (index + 0.5) / 8
            frame = run_process([
                "ffmpeg", "-v", "error", "-xerror", "-nostdin", "-threads", "1",
                "-protocol_whitelist", "file,pipe", "-ss", f"{timestamp:.6f}",
                "-f", "mov", "-enable_drefs", "0", "-use_absolute_path", "0", "-i", source, "-map", "0:v:0", "-frames:v", "1",
                "-an", "-sn", "-dn", "-vf", "scale=768:768:force_original_aspect_ratio=decrease",
                "-threads", "1", "-q:v", "5", "-f", "image2pipe", "-vcodec", "mjpeg", "-",
            ], fd, deadline, 700000)
            if not frame.startswith(b"\xff\xd8") or not frame.endswith(b"\xff\xd9"):
                return {"valid": False}
            frames.append("data:image/jpeg;base64," + base64.b64encode(frame).decode("ascii"))
        return {"valid": True, "duration": duration, "width": width, "height": height, "frames": frames}
    except (subprocess.CalledProcessError, KeyError, json.JSONDecodeError, ValueError):
        return {"valid": False}
    finally:
        os.close(fd)


class Handler(BaseHTTPRequestHandler):
    server_version = "media-check"

    def setup(self):
        super().setup()
        self.connection.settimeout(5)

    def log_message(self, *_args):
        pass

    def reply(self, status, value):
        body = json.dumps(value, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self.reply(200 if self.path == "/healthz" else 404, {"ok": self.path == "/healthz"})

    def do_POST(self):
        if self.path != "/inspect":
            return self.reply(404, {"error": "not_found"})
        if not TOKEN or not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + TOKEN):
            return self.reply(401, {"error": "unauthorized"})
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= 4096 or self.headers.get("Transfer-Encoding"):
                return self.reply(400, {"error": "invalid_request"})
            body = json.loads(self.rfile.read(length))
            if not isinstance(body, dict) or set(body) != {"name"}:
                return self.reply(400, {"error": "invalid_request"})
            self.reply(200, inspect_video(body["name"]))
        except (ValueError, json.JSONDecodeError):
            self.reply(400, {"error": "invalid_request"})
        except (TimeoutError, subprocess.TimeoutExpired):
            self.reply(503, {"error": "inspection_timeout"})
        except OSError:
            self.reply(503, {"error": "media_unavailable"})


if __name__ == "__main__":
    if not TOKEN:
        raise SystemExit("ERDAI_MEDIA_CHECK_TOKEN is required")
    # A single request is processed at a time; the socket backlog is bounded.
    HTTPServer.request_queue_size = 4
    HTTPServer(("0.0.0.0", 8091), Handler).serve_forever()
