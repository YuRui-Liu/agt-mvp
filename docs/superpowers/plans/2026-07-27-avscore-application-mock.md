# avscore 本地 Mock 投递闭环实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development 逐任务实现此计划。步骤使用复选框（`- [ ]`）跟踪。

**目标：** 恢复参考画像页的身份生成逻辑，提供海报直接下载和本地职位投递闭环。

**架构：** 浏览器端 `aiti-mock.js` 独立管理验证码、AITI ID、关联和投递状态；画像页和投递页复用该状态。Python 服务只提供受 token 保护的 HTML、JS、PNG 和 SVG 资源，不接收手机号或投递表单。

**技术栈：** 原生 JavaScript、`localStorage`、HTML/CSS、Python 标准库 HTTP 服务、Node 内置测试、Python `unittest`。

---

## 文件结构

- 创建 `aiti-mock.js`：本地身份与投递状态机。
- 创建 `job-application.html.tmpl`：参考投递页样式和本地 Mock 表单。
- 创建 `assets/poster.png`、`assets/aiti-qr.svg`：离线下载与二维码资源。
- 修改 `avscore.html.tmpl`：恢复身份区、海报下载和投递入口。
- 修改 `avscore_server.py`：提供投递页及受控静态资源。
- 修改 `README.md`、`avscore.md`：记录本地 Mock 范围。
- 创建或修改相关 Python、Node、Shell 测试。

### 任务 1：AITI Mock 状态机与资源

- [ ] 先编写 Node 失败测试，覆盖验证码、ID 格式、持久化恢复、损坏数据、关联和重复投递。
- [ ] 创建 `aiti-mock.js`，采用 UMD 形式兼容浏览器与 Node；所有存储异常降级到内存状态。
- [ ] 复制参考海报 PNG 与二维码 SVG 为 AITI 资源，测试文件存在、MIME 和非空内容。
- [ ] 运行 Node 测试并提交 `feat: add local AITI identity mock`。

### 任务 2：画像页身份生成与直接下载

- [ ] 先编写模板和 DOM 失败测试，覆盖身份区、错误验证码、生成成功、最近 ID 恢复、投递 URL。
- [ ] 从参考 `user-profile.html` 恢复身份区 CSS、DOM 和逻辑，品牌统一为 AITI。
- [ ] 海报使用 `<a download="AITI-专属海报.png">`，不绑定 click、预览、放大、弹窗或 `target="_blank"`。
- [ ] 投递按钮跳转受 token 保护的 `/application`。
- [ ] 运行 Python/Node 测试并提交 `feat: restore profile identity actions`。

### 任务 3：投递页与服务路由

- [ ] 先编写 HTTP 和投递 DOM 失败测试，覆盖路由鉴权、自动回填、校验、必填字段、成功与重复投递。
- [ ] 以参考 `job-application.html` 为 CSS/DOM 基线创建模板，并使用 AITI Mock。
- [ ] 服务新增 `/application`、`/assets/aiti-mock.js`、`/assets/poster.png`、`/assets/aiti-qr.svg`；页面 query token，资源使用同源受控 token 路由且拒绝路径穿越。
- [ ] 所有响应设置正确 Content-Type、no-store/nosniff/CSP；投递数据不进入 Python 请求。
- [ ] 运行全量测试并提交 `feat: add local mock application flow`。

### 任务 4：文档与整体验证

- [ ] 更新 README/skill，明确验证码与投递仅为本地 Mock、手机号不离开浏览器。
- [ ] 运行 `python3 -m unittest discover -s tests -v`。
- [ ] 运行全部 Node 测试、`bash tests/test_avscore_sh.sh`、`bash -n avscore.sh` 和 `git diff --check`。
- [ ] 扫描 KwAITI、Mock session、未知模板占位符和海报点击拦截。
- [ ] 用真实 localhost 服务验证画像页、资源和投递页均返回 200。
- [ ] 最终整体审查并提交 `test: verify local application flow`。
