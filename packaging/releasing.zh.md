# 正式发行操作指南

维护者在本地确认版本可发布后，推送 `vX.Y.Z` tag；**Release** 工作流自动检查、构建、下载复验并发布 GitHub Release。构建开发快照见[打包指南](README.zh.md)；下载、安装和维护见[安装与运行](../docs/installation.zh.md)。

正式矩阵固定为 macOS／Linux 的 arm64／amd64 四个平台 × core／standard，共 8 包，推荐 standard。full 保留为开发／实验包型，Linux full 的 Git/TLS 运行库缺项仍阻止组包；Scion 暂缓。发布流程简化不扩大真实系统、浏览器或设备的支持声明。

## 发布前准备

将完整发布代码与 [release.yml](../.github/workflows/release.yml) 纳入将要打 tag 的源码。正式工作流只接受 tag push，不提供手动包型选择。手动构建使用 [Package snapshots](../.github/workflows/package.yml)，生成开发快照。

正式工作流使用内置 `GITHUB_TOKEN`，仅最终 publish job 拥有 `contents: write`，其余 job 为只读。CI 使用固定协议 fixture，无需模型或平台凭据、浏览器安装、人工资格 JSON 或 Environment 审批。原有发布专用 Environment 和模型 Secrets 已无工作流引用，仓库管理员可另行清理；可选 Apple 签名凭据需配置在仓库可用的位置，见下文。

在仓库根目录执行发布所需的本地检查：

```sh
node scripts/check-packaging.mjs
node --test scripts/*.test.mjs
make test
go vet ./...
node --test internal/web/tests/web*_test.mjs
```

检查失败先修复，不绕过工作流门禁。真实模型、浏览器、平台账号、干净机器和长期部署测试由维护者在打 tag 前按版本改动安排；工具与范围见[独立本地验收](#独立本地验收)。CI 不读取或要求这些报告。

当前尚未完成真实 GitHub 自动发布闭环验证；已知 Go 检查阻塞及本地验证结果见[实施记录](../docs/implementation/github-release-simplification.zh.md)。完成代码和文档更新不等于已经验证多平台发行。

## 推送版本 tag

确认工作区干净、源码已提交到预期版本，选择尚未发布的新版本。以下 `vX.Y.Z` 和 `GITHUB_REMOTE` 分别替换为实际版本与指向目标 GitHub 仓库的 remote 名称：

```sh
git status --short
git tag -a vX.Y.Z -m 'TeamX vX.Y.Z'
git push GITHUB_REMOTE refs/tags/vX.Y.Z
```

版本必须是无前导零的三段非负整数，例如 `v0.1.0`。预发布／构建后缀不接受，同名分支不能代替 tag；annotated tag 按解引用后的 commit 核验。推送 tag 会触发正式发布，应在本地确认版本后执行。

在 GitHub **Actions → Release** 查看同一次运行：

| Job | 完成内容 |
| --- | --- |
| `checks` | 精确 tag／完整 commit／干净源码、打包清单、脚本及 Go／Web 自动化检查 |
| `build` | 8 个包分别在原生 runner 构建，检查依赖、归档与固定协议 smoke；可选签名／公证在封存前完成 |
| `verify` | 独立 job 下载同一归档，复核身份、摘要和构建批次，再执行原生 smoke |
| `assemble` | 核对完整矩阵及逐包 CI 报告，汇总原始归档、安装器、许可、说明和校验和 |
| `publish` | 创建或恢复相同资产的草稿，上传缺失文件，下载逐一核对摘要后公开 |

任一必要检查或矩阵成员失败都会阻止发布。同一 tag 的运行串行执行；job 之间自动传递上下文和产物，无需记录或填写 run ID、attempt。

如需在本地构建版本归档，在匹配的原生目标上使用统一入口：

```sh
./scripts/package.sh --channel release --version vX.Y.Z --profile standard \
  --target darwin-arm64 --output-dir /absolute/new-output
```

示例目标为 Apple Silicon Mac，换为实际机器的目标；源码必须干净，tag 解引用后的 commit 必须与 HEAD 一致。使用新的输出目录，版本归档不能被覆盖。本地组包不创建 GitHub Release，正式发布仍由 tag push 工作流完成。

## 发行资产与安装入口

公开 Release 包含：

| 资产 | 用途 |
| --- | --- |
| `teamx-vX.Y.Z-<target>-<profile>.tar.gz` 及同名 `.sha256` | 8 个已检查的原始归档及摘要 |
| 同一文件名前缀的 `*-verification.json` | 逐包 CI 归档身份、构建 smoke 与下载后 smoke 结果，绑定源码、依赖锁、摘要及构建批次 |
| `install.sh` | 固定到该版本与仓库，自动下载本机 standard 包并安装常驻服务 |
| `release.json` | schema 2、`kind=github_release`；记录版本、源码、来源工作流、构建批次、完整矩阵、签名模式和资产摘要 |
| `SHA256SUMS` | 除自身外全部发行资产的 SHA-256 |
| `LICENSES.tar.gz` | 各目标许可材料汇总 |
| `README.zh.md`、`release-notes.md` | 安装指南与该版本的 CI 检查范围、下载清单 |

归档的基础 manifest 保持 schema 1；其 `distribution` 使用 schema 2、`kind=release_archive`，保留实际签名结果。报告描述已完成的 CI 检查，不承载人工验收结论；汇总和发布不会重建或修改原始归档。

安装器来自同一 tag 的源码，版本与仓库固定后纳入 `SHA256SUMS`，`release.json` 也记录其摘要。正式发布后，项目默认安装入口为：

```sh
curl -fsSL https://github.com/go-aie/teamx/releases/latest/download/install.sh | bash
```

该脚本内部使用所属版本的固定下载地址，避免 latest 变化导致归档与摘要混用。发布后核对资产清单与安装入口，并在实际目标机器验证安装、PATH 和 `teamx service status/stop/start`；本地模拟不代替 macOS LaunchAgent、Linux systemd 或真实下载验收。

## 失败与重跑

- **checks、构建、复验或汇总失败**：查看对应 job 日志。相同源码上的暂时故障解决后，选择 **Re-run all jobs**，生成完整新批次；只重跑部分 build／verify／assemble 会被拒绝。源码需要修改时，修复后使用新的版本 tag。
- **只有 publish 失败**：可在同一次运行中只重跑 publish，复用原 assemble 输出的 artifact ID、上下文与 checksum seal，不重新组包。已有远端资产先下载核验，只补缺失文件。
- **草稿不匹配或存在重复记录**：发布停止。同一 tag 仅在草稿正文与 seal 一致时恢复；新构建批次不能覆盖旧草稿。维护者先检查远端状态，再明确处理冲突，不直接替换资产。
- **tag 被移动、远端资产损坏或多出文件**：停止发布，检查来源与远端内容；不将新旧批次拼接。
- **发布响应丢失或已公开**：先查看远端 Release。已公开版本不会被重跑覆盖；新内容使用新版本 tag。

Artifact 保留 30 天。恢复依赖原封存资产仍可下载，不能从其他运行或批次补齐。GitHub 的重跑会保留未重跑 job 的 outputs，详情见[官方重跑说明](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs)。

## 独立本地验收

按版本影响选择场景：干净账户安装与系统下载拦截、真实模型首启／重启和会话恢复、生产页面与真实手机交互、平台登录／只读／连接绑定、常驻服务启停与子进程清理、备份升级恢复，以及通知积压／限流／长期运行。只记录实际执行的系统、设备、模型和账号范围；平台写入使用已明确授权的目标。

对本机原生归档执行真实 DSH／ACP 与生产页面验收时，验收机需准备 Go、Node 及固定 Playwright／Chromium。浏览器依赖装在源码与归档之外；以下目录替换为尚不存在的绝对路径：

```sh
mkdir /absolute/new-private-verifier
cp packaging/verifier/package*.json /absolute/new-private-verifier/
npm ci --prefix /absolute/new-private-verifier --ignore-scripts --no-audit --no-fund
/absolute/new-private-verifier/node_modules/.bin/playwright install chromium
```

按模型服务准备含 `[runtime.model]` 的私有 TOML，密钥变量名与模型配置中的 `api_key_env` 一致。凭据可从私有 env 文件载入；无需 env 文件时省略最后一个参数：

```sh
NODE_PATH=/absolute/new-private-verifier/node_modules \
  ./scripts/package-acceptance.sh /absolute/teamx-archive.tar.gz \
  --output-dir /absolute/new-private-acceptance \
  --model-config /absolute/model.toml --env-file /absolute/private-model.env
```

Linux 如提示缺少 Chromium 系统依赖，按 Playwright 提示由管理员在验收机准备。更多真实链路入口见[验证入口](../internal/room/README.zh.md#验证入口)；脚本参数见[本地验收脚本](../scripts/package-acceptance.mjs)。真实调用会消费模型额度；模型配置、凭据、回答、Session、账号、日志和截图保留在受控本地目录，不上传为公开资产。`model-package-probe.mjs` 保留模型首启／维护场景，具体参数见其[脚本入口](../scripts/model-package-probe.mjs)。

常驻部署可另用只读观察工具（部署账号运行，使用归档内 Node 或兼容版本）：

```sh
node scripts/server-observation.mjs \
  --archive /absolute/teamx-standard.tar.gz \
  --server-root /absolute/server-root \
  --endpoint https://assistant.example.com \
  --token-file /absolute/private-home/auth/admin-token \
  --output-dir /absolute/new-private-observation
```

默认观察 24 小时、每 60 秒采样；可用 `--duration-seconds` 和 `--interval-seconds` 调整范围。`completed_pending_review` 只表示计时与采样连续性，平台积压、错误和恢复仍需结合实际结果判断。短探针、模拟手机视口或 fixture 均不能声明真实长期运行或设备测试通过。这些工具的报告独立于 CI 发布决策。

## 按需生成离线包

正式发布后，可用原始 standard 归档及同名摘要执行 `make package-offline ARCHIVE=/absolute/teamx-standard.tar.gz`，生成单独的 `dist/offline/*-offline.zip`。收件人解压后运行 `bash ./install-offline.sh`。可用 `OUTPUT` 指定新路径；补充 ZIP 放在封存资产目录外，保持已发布资产集合不变。具体选项见[按需生成单文件离线包](README.zh.md#按需生成单文件离线包)。

## 可选的 macOS 签名与公证

默认 `unsigned`，无需 Apple 凭据。需要 Developer ID 签名时，在推送 tag 前配置仓库[Actions 变量](https://github.com/go-aie/teamx/settings/variables/actions)：`TEAMX_MACOS_SIGNING=developer-id`、完整 `TEAMX_SIGN_IDENTITY`（`Developer ID Application: 名称 (TEAMID)`）。在仓库[Actions Secrets](https://github.com/go-aie/teamx/settings/secrets/actions) 配置：

- `TEAMX_MACOS_P12_BASE64`、`TEAMX_MACOS_P12_PASSWORD`：Developer ID Application 证书及私钥的 PKCS#12 导出和密码。
- `TEAMX_APPLE_API_KEY_BASE64`、`TEAMX_APPLE_API_KEY_ID`、`TEAMX_APPLE_API_ISSUER`：App Store Connect API 私钥及对应标识。

证书从 [Apple Developer](https://developer.apple.com/account/resources/certificates/list) 创建，API Key 从 [App Store Connect](https://appstoreconnect.apple.com/access/integrations/api) 创建；秘密值直接保存到 Secrets。若使用其他仓库，替换上述仓库设置链接。原先仅在 Environment 中保存的签名凭据须在仓库级准备好，当前工作流不绑定 Environment。

工作流使用临时 keychain，签名与公证成功后才封存归档，失败不会回退为 unsigned。裸 CLI 归档使用在线公证票据，不宣称已 staple。系统实际下载与首次启动行为由维护者另行验证；签名改变程序字节，需要新构建批次，已发布版本不可覆盖。
