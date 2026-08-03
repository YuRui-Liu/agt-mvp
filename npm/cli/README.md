# @yurui-liu/kuai-cli — MVP

Kuai CLI MVP, distributed via GitHub Packages (Node.js only, no binary signing required).

## Install

```sh
# Configure npm to use GitHub Packages for @yurui-liu scope (one-time setup)
echo "@yurui-liu:registry=https://npm.pkg.github.com" >> ~/.npmrc

# Install
npm install -g @yurui-liu/kuai-cli
```

Or pass the registry flag inline:

```sh
npm install -g @yurui-liu/kuai-cli --registry=https://npm.pkg.github.com
```

> **No code signing required.** npm installs a plain JavaScript file (`#!/usr/bin/env node`).
> macOS Gatekeeper and Windows SmartScreen only evaluate native binaries, not `.js` scripts.

## Usage

```sh
kuai help
kuai version
```

## Verify installation security (macOS)

After installing, run the verification script:

```sh
curl -fsSL https://raw.githubusercontent.com/YuRui-Liu/agt-mvp/kuai-node/npm/cli/scripts/verify-install.sh | sh
```

Expected output:
- `PASS  无 com.apple.quarantine（不触发 Gatekeeper）`
- `PASS  kuai 是文本脚本（非二进制，天然绕过签名要求）`
- `PASS  kuai version 输出：0.1.0-mvp.1`

## Requirements

- Node.js >= 18
- npm >= 7

## Why no signing?

npm-installed packages are pure JavaScript executed by your locally-installed `node` binary.
There is no untrusted native binary involved, so:
- macOS: no `com.apple.quarantine` attribute, Gatekeeper never evaluates the script
- Windows: no `Zone.Identifier` alternate data stream, SmartScreen not triggered

This is fundamentally different from distributing a Go binary via curl.
