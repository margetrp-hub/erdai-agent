"""Exercise release rollback failure paths without Docker or production data."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPTS = Path(__file__).resolve().parent


class DeploymentRollbackTest(unittest.TestCase):
    def run_rollback(self, renamed=False, core_started=False, browser_started=False,
                     app_moved=False):
        with tempfile.TemporaryDirectory(prefix="erdai-rollback-test-") as folder:
            root = Path(folder)
            mockbin = root / "bin"
            mockbin.mkdir()
            rollback = root / "rollback"
            rollback.mkdir()
            app = root / ("rollback/app" if app_moved else "app")
            (app / "scripts").mkdir(parents=True)
            (app / "compose.production.yml").write_text("old-compose\n")
            (app / "scripts/set-channel-mode.sh").write_text(
                '#!/bin/sh\nprintf "mode=%s\\n" "$1" >> "$MOCK_LOG"\n')
            (app / "scripts/set-channel-mode.sh").chmod(0o755)
            (root / ".env").write_text("old-env\n")
            (rollback / ".env").write_text("old-env\n")
            for name, source in {
                "docker": """#!/bin/sh
printf '%s\\n' "$*" >> "$MOCK_LOG"
case "$*" in
  "container inspect old-core") test "$MOCK_RENAMED" = 1 ;;
  "inspect -f "* ) echo healthy ;;
  "compose "*"config --services") echo erdai-monitor-browser ;;
esac
""",
                "curl": "#!/bin/sh\nexit 0\n",
            }.items():
                (mockbin / name).write_text(source)
                (mockbin / name).chmod(0o755)
            # Use the real cleanup function; fixture variables replace only paths
            # and the exact release phase at which failure was injected.
            source = (SCRIPTS / "deploy-250.sh").read_text()
            prelude = source.split("trap cleanup EXIT", 1)[0]
            script = root / "failure.sh"
            script.write_text(prelude + """
root=$TEST_ROOT
env_file=$root/.env
rollback_dir=$root/rollback
old_container=$TEST_OLD_CONTAINER
old_image_ref=erdai-agent:old
old_image_id=sha256:old
old_browser_image_ref=erdai-monitor-browser:old
embedding_image=embedding:pinned
release_image=erdai-agent:new
browser_image=erdai-monitor-browser:new
old_channel_mode=active
rollback_armed=1
core_install_started=$TEST_CORE_STARTED
browser_install_started=$TEST_BROWSER_STARTED
swapped_app=$TEST_APP_MOVED
trap cleanup EXIT
exit 1
""")
            log = root / "commands.log"
            env = dict(os.environ, PATH=str(mockbin) + os.pathsep + os.environ["PATH"],
                       MOCK_LOG=str(log), TEST_ROOT=str(root),
                       MOCK_RENAMED=str(int(renamed)),
                       TEST_OLD_CONTAINER="old-core" if renamed else "",
                       TEST_CORE_STARTED=str(int(core_started)),
                       TEST_BROWSER_STARTED=str(int(browser_started)),
                       TEST_APP_MOVED=str(int(app_moved)))
            result = subprocess.run(["sh", str(script), str(root)], env=env,
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 1, result.stderr)
            commands = log.read_text()
            self.assertIn("start erdai-agent", commands)
            self.assertIn("mode=active", commands)
            self.assertEqual("rm -f erdai-agent\n" in commands, core_started)
            self.assertEqual("rm -f erdai-monitor-browser\n" in commands, browser_started)
            self.assertEqual((root / "app/compose.production.yml").read_text(), "old-compose\n")
            if renamed:
                self.assertIn("rename old-core erdai-agent", commands)
            return commands

    def test_stop_or_rename_failure_keeps_original_container(self):
        self.run_rollback()

    def test_backup_failure_restarts_renamed_original(self):
        self.run_rollback(renamed=True)

    def test_app_swap_failure_restores_original_app(self):
        self.run_rollback(renamed=True, app_moved=True)

    def test_candidate_failure_removes_only_started_candidates(self):
        self.run_rollback(renamed=True, browser_started=True, app_moved=True)
        self.run_rollback(renamed=True, core_started=True, browser_started=True, app_moved=True)

    def test_shell_syntax(self):
        for name in ("deploy-250.sh", "set-channel-mode.sh", "build-release.sh",
                     "verify-production.sh"):
            subprocess.run(["sh", "-n", str(SCRIPTS / name)], check=True)


if __name__ == "__main__":
    unittest.main()
