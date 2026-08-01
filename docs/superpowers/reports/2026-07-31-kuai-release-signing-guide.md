# kuAI 单文件发布签名与分发指南

> 适用对象：`kuai` 单文件命令行工具
>
> 目标：尽量降低 macOS Gatekeeper 与 Windows SmartScreen 的拦截概率，并给出可落地的发布流程

## 1. 先说结论

`kuai` 作为单文件可执行程序，并不天然豁免开发者签名要求。

如果目标是公开分发、希望用户下载后尽量顺畅启动，那么建议：

- macOS：`codesign` + notarization
- Windows：Authenticode 签名 + 时间戳
- CI：固定同一套发布身份，避免每个版本换证书主体

如果不签名，程序“通常仍然能运行”，但：

- macOS 更容易被 Gatekeeper 拦截或要求用户手动放行；
- Windows 更容易触发 SmartScreen 的未知发布者提示。

结论是：`kuai` 不是桌面软件，也仍然应该签名。

## 2. macOS 方案

### 2.1 推荐做法

macOS 上建议使用 Apple Developer ID 证书对二进制签名，并走 notarization 流程。

推荐链路如下：

1. 构建 `kuai` 可执行文件；
2. 用 `codesign` 对二进制签名；
3. 通过 Apple notarization；
4. 对发布包执行 `staple`；
5. 再对外分发。

### 2.2 为什么不能省略

Apple 官方说明里，Gatekeeper 会检查下载的软件是否：

- 来自已识别开发者；
- 经过 Apple notarization；
- 未被篡改。

也就是说，macOS 的安全判断不是看它是不是“桌面应用”，而是看它是不是来自可验证的发布链路。

### 2.3 对 `kuai` 的具体影响

如果 `kuai` 只以裸二进制形式分发，用户第一次运行时更容易遇到系统提示。

更稳妥的做法是把二进制打进 `zip` 或 `pkg`，然后按 Apple 的标准路径完成签名和 notarization。这样不是为了“绕过安全机制”，而是为了让系统有足够的信任材料来判断这是正常发布的软件。

### 2.4 macOS 发布建议

- 使用固定的 Apple Developer ID；
- 不要在签名后修改二进制内容；
- 每次重新构建都要重新签名；
- 如果是公开下载，优先走 notarized `zip` 或 `pkg`。

## 3. Windows 方案

### 3.1 推荐做法

Windows 上建议对 `kuai.exe` 做 Authenticode 签名，并带时间戳。

推荐链路如下：

1. 构建 `kuai.exe`；
2. 使用代码签名证书签名；
3. 加入 RFC 3161 时间戳；
4. 对外发布。

### 3.2 为什么还会弹提示

Microsoft 的说明里，SmartScreen 不只看“有没有签名”，还看两类信誉：

- 发布者信誉；
- 文件哈希信誉。

这意味着：

- 未签名文件更容易被拦；
- 新签名文件也可能在早期出现提示；
- 即使签了名，信誉还没积累起来时，首次下载仍可能被提示为“未知发布者”。

### 3.3 对 `kuai` 的具体影响

如果 `kuai.exe` 直接下载给用户，不签名时 SmartScreen 的提示概率会很高。

如果签名并保持同一个发布身份，随着版本和下载量积累，提示概率会逐步下降，但不可能承诺“绝对不提示”。

### 3.4 Windows 发布建议

- 使用稳定的代码签名证书；
- 始终启用时间戳；
- 不要每次换发布身份；
- 签名后不要再改动二进制。

## 4. CI / CD 建议流程

建议把签名放进 CI，而不是手工在本机签。

### 4.1 macOS CI

1. 编译 `kuai`；
2. `codesign`；
3. 提交 notarization；
4. `staple`；
5. 打包发布物。

### 4.2 Windows CI

1. 编译 `kuai.exe`；
2. `signtool sign /fd SHA256 /tr <timestamp-url> /td SHA256`；
3. 产出发布包；
4. 上传制品。

### 4.3 共同要求

- 签名密钥不要硬编码进仓库；
- 证书私钥要放在受控密钥管理系统；
- 构建产物签名后不能再被二次修改；
- 同一版本的哈希值应保持稳定可追踪。

## 5. `kuai` 的推荐发布形态

我建议把 `kuai` 的发布物分成两层：

- 主产物：单文件可执行程序；
- 分发载体：macOS 的 `zip` / `pkg`，Windows 的 `zip` / 安装包。

这样可以满足“单文件运行”的产品目标，同时保留足够完整的签名与分发元数据。

## 6. 不能做的事

以下做法不建议：

- 以为“CLI 不是桌面软件，所以不需要签名”；
- 签名后再改字节内容；
- 每个版本换一套发布身份；
- 只做 macOS，不做 Windows；
- 只靠自定义下载说明来替代系统级信任。

## 7. 最小可执行清单

如果现在就要开始，我建议先把下面这几件事做掉：

1. 申请并保管 Apple Developer ID；
2. 申请并保管 Windows 代码签名证书；
3. 在 CI 中加入 macOS `codesign` 和 notarization；
4. 在 CI 中加入 Windows `signtool` 签名与时间戳；
5. 确保发布流程里没有“签名后再修改文件”的步骤；
6. 为用户文档补一句明确说明：下载来源必须可信。

## 8. 简短结论

`kuai` 作为单文件 CLI，仍然应该做开发者签名。

它不能保证完全不触发 macOS 或 Windows 的安全提示，但签名和 notarization 能显著降低拦截概率，是公开分发时最现实的路径。

## 9. 官方参考

- Apple Gatekeeper and runtime protection: https://support.apple.com/guide/security/gatekeeper-and-runtime-protection-sec5599b66df/web
- Apple app code signing process: https://support.apple.com/guide/security/app-code-signing-process-sec3ad8e6e53/web
- Microsoft SmartScreen reputation: https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/smartscreen-reputation
- Microsoft Authenticode time stamping: https://learn.microsoft.com/en-us/windows/win32/seccrypto/time-stamping-authenticode-signatures
- Microsoft Authenticode / SignTool overview: https://learn.microsoft.com/en-us/windows/win32/dxtecharts/authenticode-signing-for-game-developers
