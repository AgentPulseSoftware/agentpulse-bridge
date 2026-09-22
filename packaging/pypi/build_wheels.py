#!/usr/bin/env python3
"""Build the four PyPI wheels for the agentpulse bridge (BR-20).

Standard library only. Wraps binaries GoReleaser already built into
`dist/`, one wheel per platform; never builds Go code or an sdist itself
(BR-01, SEC-03).
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import zipfile
from pathlib import Path

PACKAGING_DIR = Path(__file__).resolve().parent

# GoReleaser (goos, goarch) -> the exact wheel tag BR-20 names. linux is
# tagged manylinux_2_17 honestly only because .goreleaser.yaml builds with
# CGO_ENABLED=0: no dynamic libc dependency to violate it.
WHEEL_TAGS = {
    ("darwin", "arm64"): "py3-none-macosx_11_0_arm64",
    ("darwin", "amd64"): "py3-none-macosx_11_0_x86_64",
    ("linux", "amd64"): "py3-none-manylinux_2_17_x86_64",
    ("linux", "arm64"): "py3-none-manylinux_2_17_aarch64",
}

# Mach-O 64-bit LE and ELF magic, as GoReleaser's four targets actually
# produce them (verified against a snapshot build: no fat/32-bit output).
_MACHO_MAGIC = b"\xcf\xfa\xed\xfe"
_ELF_MAGIC = b"\x7fELF"

# SEC-03: an artifact must never carry key material, in any form.
_FORBIDDEN_NAME_RE = re.compile(r"\.p8$|\.pem$|\.key$|(^|/)\.env$|(^|/)credentials", re.IGNORECASE)

# Explicit semver-prerelease -> PEP 440 table. Anything not listed here is
# a hard error, never a guess: a wheel published under a mangled version
# cannot be replaced on PyPI.
_PRERELEASE_RE = re.compile(
    r"^(?:rc\.(?P<rc>\d+)|beta\.(?P<beta>\d+)|alpha\.(?P<alpha>\d+)|snapshot-(?P<snapshot>[0-9A-Za-z]+))$"
)
_SEMVER_RE = re.compile(r"^(?P<core>\d+\.\d+\.\d+)(?:-(?P<pre>[0-9A-Za-z.-]+))?$")


def semver_to_pep440(version: str) -> str:
    """Convert a semver string (no leading "v") to a PEP 440 version."""
    match = _SEMVER_RE.match(version)
    if not match:
        raise ValueError(f"not a recognised semver version: {version!r}")
    core, pre = match.group("core"), match.group("pre")
    if pre is None:
        return core
    pre_match = _PRERELEASE_RE.match(pre)
    if not pre_match:
        raise ValueError(
            f"unrecognised semver prerelease {pre!r} in version {version!r}; "
            "add it to the explicit table in semver_to_pep440, never guess"
        )
    if pre_match.group("rc") is not None:
        return f"{core}rc{pre_match.group('rc')}"
    if pre_match.group("beta") is not None:
        return f"{core}b{pre_match.group('beta')}"
    if pre_match.group("alpha") is not None:
        return f"{core}a{pre_match.group('alpha')}"
    return f"{core}+snapshot.{pre_match.group('snapshot')}"


def discover_binaries(dist_dir: Path) -> dict[tuple[str, str], Path]:
    """Find the four release binaries via dist/artifacts.json, not globs."""
    artifacts_path = dist_dir / "artifacts.json"
    if not artifacts_path.is_file():
        raise SystemExit(f"no artifacts.json in {dist_dir}; run goreleaser first")
    artifacts = json.loads(artifacts_path.read_text())
    base_dir = dist_dir.parent
    found: dict[tuple[str, str], Path] = {}
    for artifact in artifacts:
        if artifact.get("type") != "Binary":
            continue
        key = (artifact.get("goos"), artifact.get("goarch"))
        if key not in WHEEL_TAGS:
            continue
        path = Path(artifact["path"])
        if not path.is_absolute():
            path = base_dir / path
        if not path.is_file() or not os.access(path, os.X_OK):
            raise SystemExit(f"binary for {key[0]}/{key[1]} missing or not executable: {path}")
        found[key] = path
    missing = sorted(WHEEL_TAGS.keys() - found.keys())
    if missing:
        raise SystemExit(f"missing binaries for: {missing}")
    return found


def build_one_wheel(binary: Path, tag: str, version: str, out_dir: Path) -> Path:
    with tempfile.TemporaryDirectory(prefix="agentpulse-pypi-") as tmp:
        stage = Path(tmp) / "src"
        stage.mkdir()
        for name in ("pyproject.toml", "hatch_build.py", "README.md"):
            shutil.copy2(PACKAGING_DIR / name, stage / name)
        (stage / "VERSION").write_text(version + "\n")
        bin_dir = stage / "bin"
        bin_dir.mkdir()
        staged_binary = bin_dir / "agentpulse"
        shutil.copy2(binary, staged_binary)
        staged_binary.chmod(0o755)

        build_out = Path(tmp) / "build-out"
        env = dict(os.environ, AGENTPULSE_WHEEL_TAG=tag)
        cmd = [sys.executable, "-m", "build", "--wheel", "--no-isolation", "--outdir", str(build_out), str(stage)]
        subprocess.run(cmd, check=True, env=env)
        wheels = list(build_out.glob("*.whl"))
        if len(wheels) != 1:
            raise SystemExit(f"expected exactly one wheel for {tag}, got {wheels}")
        out_dir.mkdir(parents=True, exist_ok=True)
        dest = out_dir / wheels[0].name
        shutil.move(str(wheels[0]), dest)
        return dest


def verify_wheel(path: Path, tag: str) -> int:
    """Check the wheel's filename and size invariants; returns the binary size."""
    if tag not in path.name:
        raise SystemExit(f"{path.name}: filename does not carry expected tag {tag}")
    with zipfile.ZipFile(path) as zf:
        names = zf.namelist()
        for name in names:
            if _FORBIDDEN_NAME_RE.search(name):
                raise SystemExit(f"{path.name}: forbidden member {name!r} (SEC-03)")
            if name.endswith(".py"):
                raise SystemExit(f"{path.name}: contains a .py file, metadata only allowed: {name}")
        script_members = [n for n in names if ".data/scripts/" in n]
        if len(script_members) != 1 or not script_members[0].endswith("/agentpulse"):
            raise SystemExit(f"{path.name}: expected exactly one .data/scripts/agentpulse, got {script_members}")
        payload = zf.read(script_members[0])
        if payload[:2] == b"#!":
            raise SystemExit(f"{path.name}: scripts entry is a shebang script, not a binary")
        if payload[:4] not in (_MACHO_MAGIC, _ELF_MAGIC):
            raise SystemExit(f"{path.name}: scripts entry is not a Mach-O or ELF binary")
        return len(payload)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist", default="dist", type=Path)
    parser.add_argument("--out", default=Path("dist/wheels"), type=Path)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()

    version = semver_to_pep440(args.version)
    binaries = discover_binaries(args.dist.resolve())
    for (goos, goarch), tag in WHEEL_TAGS.items():
        wheel = build_one_wheel(binaries[(goos, goarch)], tag, version, args.out)
        size = verify_wheel(wheel, tag)
        print(f"{wheel.name}: {size} bytes (agentpulse {goos}/{goarch})")


if __name__ == "__main__":
    main()
