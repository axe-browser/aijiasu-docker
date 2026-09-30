#!/usr/bin/env python3
"""Build six standalone CLI binaries and SHA256SUMS for a GitHub release."""

import argparse
import hashlib
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import tempfile
import uuid


ROOT = Path(__file__).resolve().parents[1]
PLATFORMS = (
    ("linux", "amd64", "linux"),
    ("linux", "arm64", "linux"),
    ("darwin", "amd64", "macos"),
    ("darwin", "arm64", "macos"),
    ("windows", "amd64", "windows"),
    ("windows", "arm64", "windows"),
)


def build(version: str, output: Path | None = None) -> Path:
    if not re.fullmatch(r"v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?", version):
        raise ValueError("版本号应为 1.0.0 或 v1.0.0 格式。")
    version = version.removeprefix("v")
    if platform.system() != "Darwin":
        raise RuntimeError("构建完整发布产物需要 macOS 代码签名环境。")
    if output is None:
        output = Path(tempfile.gettempdir()) / f"aijiasu-release-{version}-{uuid.uuid4().hex[:8]}"
    output = output.expanduser().absolute()
    if output.exists() or output.is_symlink():
        raise FileExistsError(f"发布目录已存在，拒绝覆盖: {output}")
    with tempfile.TemporaryDirectory(prefix="aijiasu-release-build-") as temporary:
        staging = Path(temporary)
        assets = []
        for goos, goarch, asset_os in PLATFORMS:
            executable = staging / f"aijiasu-{asset_os}-{goarch}{'.exe' if goos == 'windows' else ''}"
            environment = {**os.environ, "CGO_ENABLED": "0", "GOOS": goos, "GOARCH": goarch}
            subprocess.run(
                ["go", "build", "-buildvcs=false", "-trimpath", "-ldflags=-s -w",
                 "-o", str(executable), "./cmd/aijiasu"],
                cwd=ROOT, env=environment, check=True, timeout=300,
            )
            if goos == "darwin":
                subprocess.run(["/usr/bin/codesign", "--force", "--sign", "-", str(executable)],
                               check=True, timeout=30)
                subprocess.run(["/usr/bin/codesign", "--verify", "--strict", str(executable)],
                               check=True, timeout=30)
            assets.append(executable)

        checksums = staging / "SHA256SUMS"
        checksums.write_text(
            "".join(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n"
                    for path in sorted(assets)),
            encoding="ascii",
        )
        output.parent.mkdir(parents=True, exist_ok=True)
        output.mkdir()
        for path in (*assets, checksums):
            shutil.copy2(path, output / path.name)
    return output


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, help="发布版本，如 1.0.0")
    parser.add_argument("--output", type=Path, help="新发布目录，默认创建在系统临时目录")
    args = parser.parse_args()
    try:
        result = build(args.version, args.output)
    except (OSError, RuntimeError, ValueError, subprocess.SubprocessError) as error:
        parser.exit(1, f"Release build failed: {error}\n")
    print(f"Release assets: {result}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
