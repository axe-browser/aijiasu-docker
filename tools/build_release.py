#!/usr/bin/env python3
"""Build six portable CLI ZIPs and SHA256SUMS for a GitHub release."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import uuid
from zipfile import ZIP_DEFLATED, ZipFile, ZipInfo


ROOT = Path(__file__).resolve().parents[1]
PLATFORMS = (
    ("linux", "amd64", "linux"),
    ("linux", "arm64", "linux"),
    ("darwin", "amd64", "macos"),
    ("darwin", "arm64", "macos"),
    ("windows", "amd64", "windows"),
    ("windows", "arm64", "windows"),
)
RESOURCES = (
    "README.md",
    "CHANGELOG.md",
    ".env.example",
    "Dockerfile",
    "docker-compose.yml",
    "entrypoint.sh",
    "conf/localtime",
)
ZIP_TIMESTAMP = (1980, 1, 1, 0, 0, 0)


def add_file(archive: ZipFile, source: Path, name: str, executable: bool = False) -> None:
    info = ZipInfo(name, ZIP_TIMESTAMP)
    info.create_system = 3
    info.compress_type = ZIP_DEFLATED
    mode = 0o755 if executable else 0o644
    info.external_attr = (stat.S_IFREG | mode) << 16
    archive.writestr(info, source.read_bytes(), compress_type=ZIP_DEFLATED, compresslevel=9)


def build(version: str, output: Path | None = None, with_provider: bool = False) -> Path:
    if not re.fullmatch(r"v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?", version):
        raise ValueError("版本号应为 1.0.0 或 v1.0.0 格式。")
    version = version.removeprefix("v")
    if with_provider and platform.system() != "Darwin":
        raise RuntimeError("--with-provider 仅支持在 macOS 上构建。")
    if output is None:
        output = Path(tempfile.gettempdir()) / f"aijiasu-release-{version}-{uuid.uuid4().hex[:8]}"
    output = output.expanduser().absolute()
    if output.exists() or output.is_symlink():
        raise FileExistsError(f"发布目录已存在，拒绝覆盖: {output}")
    for name in RESOURCES:
        if not (ROOT / name).is_file():
            raise FileNotFoundError(f"缺少发布资源: {name}")

    with tempfile.TemporaryDirectory(prefix="aijiasu-release-build-") as temporary:
        staging = Path(temporary)
        archives = []
        for goos, goarch, asset_os in PLATFORMS:
            binary_name = "aijiasu.exe" if goos == "windows" else "aijiasu"
            executable = staging / f"aijiasu-{goos}-{goarch}{'.exe' if goos == 'windows' else ''}"
            environment = {**os.environ, "CGO_ENABLED": "0", "GOOS": goos, "GOARCH": goarch}
            subprocess.run(
                ["go", "build", "-buildvcs=false", "-trimpath", "-ldflags=-s -w",
                 "-o", str(executable), "./cmd/aijiasu"],
                cwd=ROOT, env=environment, check=True, timeout=300,
            )
            archive_path = staging / f"aijiasu-{asset_os}-{goarch}.zip"
            with ZipFile(archive_path, "w") as archive:
                add_file(archive, executable, binary_name, executable=True)
                for name in RESOURCES:
                    add_file(archive, ROOT / name, name, executable=name == "entrypoint.sh")
            archives.append(archive_path)

        if with_provider:
            provider = staging / "provider-macos-arm64"
            subprocess.run(
                [sys.executable, str(ROOT / "tools/build_provider.py"), "--output", str(provider)],
                cwd=ROOT, check=True, timeout=360,
            )
            manifest = json.loads((provider / "manifest.json").read_text(encoding="utf-8"))
            if manifest.get("version") != version:
                raise ValueError(f"插件清单版本 {manifest.get('version')} 与发布版本 {version} 不一致。")
            provider_binary = provider / "aijiasu"
            if manifest.get("sha256") != hashlib.sha256(provider_binary.read_bytes()).hexdigest():
                raise ValueError("插件清单中的可执行文件 SHA-256 不匹配。")
            archive_path = staging / "aijiasu-provider-macos-arm64.zip"
            with ZipFile(archive_path, "w") as archive:
                add_file(archive, provider_binary, "aijiasu", executable=True)
                add_file(archive, provider / "manifest.json", "manifest.json")
            archives.append(archive_path)

        checksums = staging / "SHA256SUMS"
        checksums.write_text(
            "".join(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n"
                    for path in sorted(archives)),
            encoding="ascii",
        )
        output.parent.mkdir(parents=True, exist_ok=True)
        output.mkdir()
        for path in (*archives, checksums):
            shutil.copy2(path, output / path.name)
    return output


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, help="发布版本，如 1.0.0")
    parser.add_argument("--output", type=Path, help="新发布目录，默认创建在系统临时目录")
    parser.add_argument("--with-provider", action="store_true", help="额外生成 macOS arm64 桌面插件包")
    args = parser.parse_args()
    try:
        result = build(args.version, args.output, args.with_provider)
    except (OSError, RuntimeError, ValueError, subprocess.SubprocessError) as error:
        parser.exit(1, f"Release build failed: {error}\n")
    print(f"Release assets: {result}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
