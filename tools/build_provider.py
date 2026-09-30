#!/usr/bin/env python3
"""Build a standalone, signed macOS arm64 provider release without running Docker."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[1]
VERSION = "1.0.0"
PROTOCOL = "aijiasu-stdio-v1"


def build(output: Path) -> Path:
    if platform.system() != "Darwin":
        raise RuntimeError("此发布脚本目前需要 macOS 构建和签名环境。")
    if output.exists() or output.is_symlink():
        raise RuntimeError("发布目录已存在；请选择新的 --output，避免覆盖已有产物。")
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".provider-build-", dir=output.parent) as temporary:
        staging = Path(temporary) / "release"
        staging.mkdir()
        executable = staging / "aijiasu"
        environment = {**os.environ, "GOOS": "darwin", "GOARCH": "arm64", "CGO_ENABLED": "0"}
        subprocess.run(["go", "build", "-trimpath", "-ldflags=-s -w", "-o", str(executable), "./cmd/aijiasu"],
                       cwd=ROOT, env=environment, check=True, timeout=300)
        subprocess.run(["/usr/bin/lipo", str(executable), "-verify_arch", "arm64"], check=True, timeout=30)
        subprocess.run(["/usr/bin/codesign", "--force", "--sign", "-", str(executable)], check=True, timeout=60)
        subprocess.run(["/usr/bin/codesign", "--verify", "--strict", str(executable)], check=True, timeout=30)
        manifest = {
            "schemaVersion": 1, "id": "aijiasu", "version": VERSION, "platform": "darwin", "arch": "arm64",
            "protocol": PROTOCOL, "executable": "aijiasu", "sha256": hashlib.sha256(executable.read_bytes()).hexdigest(),
        }
        (staging / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        shutil.copytree(staging, output)
    return output


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=ROOT / "dist/provider/darwin-arm64")
    args = parser.parse_args()
    try:
        result = build(args.output.expanduser().absolute())
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        parser.exit(1, f"Provider build failed: {error}\n")
    print(f"Provider release: {result}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
