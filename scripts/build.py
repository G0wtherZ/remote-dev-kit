#!/usr/bin/env python3
"""Build platform archives from an allowlist; runtime identities are never packaged."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / "dist"
VERSION = "0.1.0-alpha.3"
TAILSCALE = "v1.104.7"
TARGETS = ["darwin-arm64", "darwin-amd64", "windows-amd64", "windows-arm64", "linux-amd64", "linux-arm64"]


def run(args, cwd=ROOT, env=None):
    subprocess.run(args, cwd=cwd, env=env, check=True)


def build_app(target):
    goos, arch = target.split("-")
    dest = OUT / "assets/rdev" / target / ("rdev.exe" if goos == "windows" else "rdev")
    dest.parent.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, "GOOS": goos, "GOARCH": arch, "CGO_ENABLED": "0"}
    run(["go", "build", "-trimpath", "-ldflags=-s -w", "-o", str(dest), "."], env=env)
    return dest


def collect_licenses(module_dir, target_dir):
    result = subprocess.run(["go", "list", "-m", "-json", "all"], cwd=module_dir, text=True, capture_output=True, check=True)
    decoder = json.JSONDecoder(); remaining = result.stdout
    while remaining.strip():
        module, end = decoder.raw_decode(remaining.lstrip()); remaining = remaining.lstrip()[end:]
        directory = module.get("Dir")
        if not directory:
            continue
        label = module["Path"].replace("/", "_") + "@" + module.get("Version", "local")
        for pattern in ["LICENSE*", "COPYING*", "NOTICE*"]:
            for source in Path(directory).glob(pattern):
                if source.is_file():
                    dest = target_dir / label / source.name; dest.parent.mkdir(parents=True, exist_ok=True)
                    if dest.exists(): dest.chmod(0o644)
                    shutil.copyfile(source, dest); dest.chmod(0o644)
    goroot = subprocess.run(["go", "env", "GOROOT"], cwd=module_dir, capture_output=True, text=True, check=True).stdout.strip()
    go_license = target_dir / "go" / "LICENSE"; go_license.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(Path(goroot) / "LICENSE", go_license)


def build_tailscale(target):
    goos, arch = target.split("-")
    result = subprocess.run(["go", "list", "-m", "-json", "tailscale.com@" + TAILSCALE], cwd=ROOT,
                            capture_output=True, text=True, check=True)
    module = Path(json.loads(result.stdout)["Dir"])
    for binary in ["tailscale", "tailscaled"]:
        dest = OUT / "assets/tailscale" / target / (binary + (".exe" if goos == "windows" else ""))
        dest.parent.mkdir(parents=True, exist_ok=True)
        env = {**os.environ, "GOOS": goos, "GOARCH": arch, "CGO_ENABLED": "0", "GOFLAGS": "-mod=readonly"}
        run(["go", "build", "-trimpath", "-ldflags=-s -w", "-o", str(dest), "./cmd/" + binary], cwd=module, env=env)
    collect_licenses(module, OUT / "licenses")


def package(target):
    goos, _ = target.split("-")
    basename = f"remote-dev-kit-{VERSION}-{target}"
    with tempfile.TemporaryDirectory(prefix="rdev-release-") as tmp:
        dest = Path(tmp) / basename; dest.mkdir()
        exe = "rdev.exe" if goos == "windows" else "rdev"
        shutil.copy2(OUT / "assets/rdev" / target / exe, dest / exe)
        for name in ["README.md", "LICENSE", "THIRD_PARTY_NOTICES.md"]:
            shutil.copy2(ROOT / name, dest / name)
        shutil.copytree(ROOT / "docs", dest / "docs")
        shutil.copy2(ROOT / ("setup.ps1" if goos == "windows" else "setup.sh"), dest / ("setup.ps1" if goos == "windows" else "setup.sh"))
        # The remote payload contains binaries only, never local profiles or test fixtures.
        for remote in [x for x in TARGETS if not x.startswith("windows")]:
            source = OUT / "assets/rdev" / remote / "rdev"
            path = dest / "assets/rdev" / remote; path.mkdir(parents=True); shutil.copy2(source, path / "rdev")
        shutil.copytree(OUT / "assets/tailscale" / target, dest / "assets/tailscale" / target)
        shutil.copytree(OUT / "licenses", dest / "licenses", dirs_exist_ok=True)
        sums = []
        forbidden = {"config.json", "tailscaled.state", "identity_ed25519", "known_hosts", "lease.json"}
        for path in sorted(dest.rglob("*")):
            if path.is_file():
                if path.name in forbidden:
                    raise RuntimeError("Refusing to package runtime identity: " + path.name)
                sums.append(hashlib.sha256(path.read_bytes()).hexdigest() + "  " + path.relative_to(dest).as_posix())
        (dest / "SHA256SUMS").write_text("\n".join(sums) + "\n")
        if goos == "windows":
            archive = OUT / (basename + ".zip")
            with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as z:
                for path in dest.rglob("*"):
                    if path.is_file(): z.write(path, str(path.relative_to(dest.parent)))
        else:
            archive = OUT / (basename + ".tar.gz")
            with tarfile.open(archive, "w:gz") as tar: tar.add(dest, arcname=basename)
    print(json.dumps({"archive": str(archive), "sha256": hashlib.sha256(archive.read_bytes()).hexdigest()}), flush=True)


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--target", action="append", choices=TARGETS, help="local release platform; repeat to build several")
parser.add_argument("--app-only", action="store_true", help="cross-compile the app payloads without Tailscale or archives")
args = parser.parse_args()
OUT.mkdir(exist_ok=True)
for target in TARGETS:
    build_app(target)
collect_licenses(ROOT, OUT / "licenses")
if not args.app_only:
    for target in args.target or ["darwin-arm64"]:
        build_tailscale(target)
        package(target)
