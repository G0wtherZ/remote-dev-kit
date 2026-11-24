#!/usr/bin/env python3
"""Exercise the macOS download/setup path against local release fixtures, without SSH."""
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
VERSION = "0.1.0-alpha.5"
ARCH = {"arm64": "arm64", "x86_64": "amd64"}.get(platform.machine())
ARCHIVE = ROOT / "dist" / f"remote-dev-kit-{VERSION}-darwin-{ARCH}.tar.gz"


@unittest.skipUnless(platform.system() == "Darwin" and ARCHIVE.exists(), "requires macOS and a local release archive")
class InstallerTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="rdev-bootstrap-test-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.home = self.root / "home"
        self.home.mkdir()
        tools = self.root / "tools"
        tools.mkdir()
        # Only the transport is stubbed. The real bootstrap, archive checksum,
        # inner setup, installed binary and setup --plan are exercised.
        curl = tools / "curl"
        curl.write_text("#!/usr/bin/env python3\n" + '''import hashlib, os, pathlib, sys
args = sys.argv[1:]
url = next(x for x in args if x.startswith('https://'))
assert url.startswith('https://github.com/G0wtherZ/remote-dev-kit/releases/download/v0.1.0-alpha.5/')
dest = pathlib.Path(args[args.index('-o') + 1])
source = pathlib.Path(os.environ['RDEV_TEST_ARCHIVE'])
mode = os.environ.get('RDEV_TEST_TRANSPORT', '')
if mode == 'download-error': sys.exit(22)
if url.endswith('/SHA256SUMS'):
    name = 'different-archive.zip' if mode == 'missing-checksum' else source.name
    data = (hashlib.sha256(source.read_bytes()).hexdigest() + '  ' + name + '\\n').encode()
else:
    assert url.endswith('/' + source.name)
    data = b'corrupted archive' if mode == 'corrupt' else source.read_bytes()
dest.write_bytes(data)
''')
        curl.chmod(0o755)
        self.env = {**os.environ, "HOME": str(self.home), "SHELL": "/bin/zsh",
                    "TMPDIR": str(self.root), "PATH": str(tools) + ":" + os.environ["PATH"],
                    "RDEV_TEST_ARCHIVE": str(ARCHIVE)}

    def invoke(self, mode=""):
        env = {**self.env, "RDEV_TEST_TRANSPORT": mode}
        return subprocess.run(["/bin/bash", str(ROOT / "install.sh"), "--yes", "--plan",
                               "--ssh", "dev@example.org", "--local-root", str(self.home / "Shared 资料"),
                               "--remote-root", "~/Cloud Lab", "--no-share"],
                              env=env, text=True, capture_output=True, timeout=60)

    def assert_clean_download(self):
        self.assertEqual(list(self.root.glob("rdev-download.*")), [])

    def test_verified_release_installs_and_preserves_plan_arguments(self):
        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((self.home / ".local/bin/rdev").is_symlink())
        self.assertIn("remote-dev-kit PATH", (self.home / ".zprofile").read_text())
        plan = json.loads(result.stdout[result.stdout.index("{"):])
        self.assertTrue(plan["profile"]["local_root"].endswith("Shared 资料"))
        self.assertEqual(plan["profile"]["remote_root"], "~/Cloud Lab")
        self.assertFalse((self.home / ".rdev").exists())
        self.assertFalse((self.home / "Shared 资料").exists())
        self.assert_clean_download()
        repeated = self.invoke()
        self.assertEqual(repeated.returncode, 0, repeated.stdout + repeated.stderr)
        self.assertIn('"changes"', repeated.stdout)
        self.assertEqual((self.home / ".zprofile").read_text().count("# remote-dev-kit PATH"), 1)
        self.assert_clean_download()

    def test_bad_checksum_never_installs(self):
        result = self.invoke("corrupt")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum mismatch", result.stderr)
        self.assertFalse((self.home / ".local").exists())
        self.assert_clean_download()

    def test_missing_checksum_never_installs(self):
        result = self.invoke("missing-checksum")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("invalid release checksum", result.stderr)
        self.assertFalse((self.home / ".local").exists())
        self.assert_clean_download()

    def test_download_error_never_installs(self):
        result = self.invoke("download-error")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.home / ".local").exists())
        self.assert_clean_download()


if __name__ == "__main__":
    unittest.main(verbosity=2)
