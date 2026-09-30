# AGENTS.md

## 发布约定

- 普通 CLI 的 GitHub Release 只上传 Linux、macOS、Windows 各 amd64/arm64 的裸二进制文件；不要上传普通 CLI ZIP、Docker 配置、文档或 `SHA256SUMS` 附件。
- 二进制内置 Docker 运行文件模板。发布说明应提示用户运行 `init` 生成所需文件，并自行配置 `.env`；运行仍依赖本机 Docker。
- 桌面插件需要单独的 `manifest.json`，仅在明确要求发布插件时准备其附件。
