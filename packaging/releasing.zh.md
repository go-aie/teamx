# 正式发行操作指南

本指南供维护者将同一批版本候选验收并发布到 GitHub Release。构建开发快照见[打包指南](README.zh.md)；下载、安装和维护现有版本见[安装与运行](../docs/installation.zh.md)。

正式发行的包型以统一 lock 的 `release.profiles` 为准，当前是四个平台的 core/standard 共 8 个包，推荐 standard；它们都必须具备发行资格。full 不在默认正式发行范围内，Linux full 的 `git_tls_runtime` 缺项仍保留。已有的单目标开发包通过结果不能代替新候选验收。

## 准备仓库与测试模型

将以下 Workflow 和完整打包代码提交到 GitHub 仓库，手动入口所在的 Workflow 需先进入默认分支；版本 tag 也必须包含相应代码：

| Workflow | 触发方式 | 用途 |
| --- | --- | --- |
| [package.yml](../.github/workflows/package.yml) | Pull request 或手动 | 开发快照 |
| [release.yml](../.github/workflows/release.yml) | 推送 `vX.Y.Z` tag，或手动选择包型 | 构建并下载复验候选 |
| [release-publish.yml](../.github/workflows/release-publish.yml) | 在同一版本 tag 上手动运行 | 真实验收、资格汇总及正式发布 |

在仓库的 **Settings → Secrets and variables → Actions**，或 **Settings → Environments → release-acceptance → Environment secrets** 中配置以下值，入口详见 [GitHub Secrets 设置说明](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-secrets)：

| Secret | 内容 |
| --- | --- |
| `TEAMX_RELEASE_MODEL_CONFIG` | 测试 TOML，包含有效的 `[runtime.model]`；`api_key_env` 使用 `OPENAI_API_KEY`，按模型服务填写协议、地址、模型名及兼容参数 |
| `TEAMX_RELEASE_MODEL_API_KEY` | 测试模型 API Key，地址须能从 GitHub runner 访问，额度须覆盖完整矩阵验收 |

API Key 从模型服务商控制台创建，直接保存到 GitHub Secrets。测试 TOML 只提供模型设置，不复制本机 Room、平台连接或其他凭据。不要把密钥放入源码、Workflow 输入或公开验收 JSON。

候选构建不使用模型或平台凭据。发行验收使用包内真实 DSH 和生产页面，运行会消费模型额度；Go、Node 和固定 Playwright 只作为验证机工具，不进入用户归档。私有回答、Session、日志和截图不上传 Artifact，步骤结束后清理。平台账号的检查由维护者在隔离环境完成，不放进 Actions。

发行使用 GitHub 自带 token，无需另建 PAT。只有最终发布 job 拥有 `contents: write`；模型验收使用 `release-acceptance` Environment，其访问规则由仓库设置决定。

## 1. 构建版本候选

准备干净源码及与 HEAD 一致的 `vX.Y.Z` tag，将 tag 推送到 GitHub。版本必须为三段数字，不接受同名分支代替 tag。

**Release candidates** 会按统一 lock 在原生 runner 构建全部 8 个包，并在独立 job 下载同一归档、核对摘要和运行检查。一次成功构建产出的候选 Artifact 和下载复验 Artifact 各 8 个；记下该次运行的 run ID、完整 commit 与 attempt。

候选构建成功只表示归档组装和传递检查通过。缺少真实模型或人工验收时，资格报告仍可为 `incomplete`；这不会自动发布 Release。候选 Artifact 保留 30 天。

手动运行 Release candidates 可选择单个包型，用于单独检查或 full 实验包。正式发行只接受由 tag push 触发、覆盖默认发行矩阵的候选运行，不能用手动运行替代。

重新构建候选时应完整重跑 Workflow，避免不同 attempt 混用。过期、缺失或其他运行的同名 Artifact 不能补作原候选。程序、运行时、配置或最终归档变化后，需要对新的归档重新绑定验收证据。

## 2. 完成用户环境与平台验收

下载候选 Artifact，先解开外层压缩包，保留其原始五个文件。standard 的常驻安装使用归档内 `bash ./install.sh --archive FILE`，无需先 `start init` 或再次手工执行 `teamx service install`；core 的开发运行仍按开发说明检查。在对应版本源码中为每个候选生成一个待填写模板；以下大写参数替换为实际值，输出文件必须不存在：

```sh
node scripts/package-release.mjs qualification-template \
  CANDIDATE_DIR TARGET PROFILE FULL_COMMIT vX.Y.Z NEW_QUALIFICATION.json
```

模板中的检查默认 `not_run`、观察值为 false，不能直接作为通过证据。使用匹配系统、原生架构和声明基线的隔离环境，完成下列操作：

| 检查 | 必须确认的行为 |
| --- | --- |
| `clean_machine` | 全新解压、独立 home、没有宿主 Go／Node／平台 CLI，系统依赖满足基线；full 还需没有宿主 Git |
| `downloaded_system_security`：macOS | 实际下载并保留 quarantine 至安装／启动前；standard 用系统 Bash 执行 `install.sh --archive FILE`，核对归档校验后部署、命令入口和附带工具运行；开发用 `start` 另核对拒绝不执行、同意后可运行及不重复询问。记录系统是否拦截，必要时按实际提示处理 |
| `downloaded_system_security`：Linux | 在声明系统中检查实际动态库和运行能力均在基线内 |
| `git_tls_clone`：full | 包内 Git 的 helper、证书验证、HTTPS clone 与本地 commit 均可用，未借用宿主 Git |
| `platform_login_read_bind`：standard／full | 用隔离账号完成 GitLab／飞书登录、只读查询和 TeamX 连接绑定，远端写入为 0 |
| `resident_server`：Linux amd64／macOS arm64 standard | 同一归档完成常驻安装、无 export Key 首启／重启、停止／启用、子进程清理、浏览器关闭后采集投递执行、一致备份升级恢复、保留旧 Runtime、已提交模型操作中断后的查询／重试；Linux 另验退出 SSH、机器重启和固定账号发送 |
| `web_delivery`：上述两个 standard 目标 | 同一归档的真实模型后续请求与执行快照、真实浏览器声明、平台读取／通知／授权及获授权动作、至少 24 小时部署观察；见下述记录要求 |

`resident_server.observations.model_delivery_sha256` 必须等于归档 `manifest.server_deployment.model_delivery_sha256`，绑定模型阶段 4 交付记录。`migration` 仅接受经核对的 `not_applicable` 或 `resume_verified`；其余字段须来自真实部署证据。Linux 固定账号发送仍受保护时不能填写 `lark_account_pinned_send=true`，该目标保持发行阻断。macOS 登录／休眠限制也须记录。沿用四目标和 core／standard 矩阵，不因局部部署通过改变发布清单。

`web_delivery` 也绑定该模型交付摘要。`models` 必须完成无 export Key 首启、默认／固定引用、条目更新、换 Key、删除替换、等待／部分失败、刷新原操作、重启后真实调用，并核对请求和执行快照；填写 DSH 身份材料摘要及上游范围。同一上游多条目与不同上游分别声明，其他上游及旧 DSH 可明确为 `not_run`。保存成功或 generation 文件不能单独证明后续调用生效。

`browsers` 固定列出桌面 Chromium、桌面 Safari、iOS Safari、Android Chrome；每项默认 `not_run`。真实设备完成登录、模型首启／维护／成员选择、对话、平台授权返回、草稿刷新、断网恢复和后台返回后才填 `passed`、版本、OS 与证据摘要。手机还须实际软键盘／触控／旋转；桌面检查对应键盘／窗口操作。无真实设备时保持 `not_run`，不能用 Playwright 手机视口或 WebKit 代替 Safari。至少一个桌面浏览器通过；公开评估只将通过项列入 `supported_browsers`，其余列入 `pending_browsers`，不会据此宣称完整手机支持。

`platforms` 包含真实读取、通知采集／投递、授权返回及已获授权测试目标的动作和远端结果核对。动作授权与结果材料分别计算摘要；此项不改变只读 `platform_login_read_bind` 的零远端写入要求。不要为填写记录而自行选择真实写入目标。

部署观察使用只读工具（部署账号运行，使用归档内 `current/runtime/node/bin/node` 或兼容的 Node）：

```sh
node scripts/server-observation.mjs \
  --archive /absolute/candidate-standard.tar.gz \
  --server-root /absolute/server-root \
  --endpoint https://assistant.example.com \
  --token-file /absolute/home/auth/admin-token \
  --output-dir /absolute/new-private-observation
```

默认观察 86400 秒、每 60 秒采样；每次认证读取实例、Room、模型操作和各 Room 已存订阅，记录采集积压、错误／限流及恢复。开始／结束核对已安装文件与归档；采样中校验 current、实例及代际，换归档须另开观察。进程中断、短跑、长时间休眠或无订阅都不能证明后台采集通过；失败样本完整保留。工具的 `completed_pending_review` 仅表示计时与采样连续性，始终 `release_ready=false`。至少 24 小时、最大间隔不超过 180 秒的证据还须人工核对平台积压／限流恢复、失败、同一实例／归档，再将私有报告摘要与时间／样本写入 `observation`。`--duration-seconds` 可供短探针使用，短探针始终不满足 24 小时资格。

保留每项操作的私有证据，计算其 SHA-256 后填写 `evidence_sha256`、实际 UTC 时间 `tested_at`、`status=passed` 和观察结果。`fixture` 必须为 false，`method` 为 `maintainer_manual`。人工记录表示维护者对实际操作的确认，不会被当作自动测试结果。

将本次发行矩阵中各模板的 `records` 合并到同一个公开 JSON，保留共同的版本与完整 commit：

```json
{
  "schema_version": 1,
  "version": "vX.Y.Z",
  "commit": "完整40位commit",
  "records": []
}
```

每个 target/profile 只能出现一次，归档 SHA-256 必须对应实际受测文件。JSON 只放固定观察项和证据摘要，不放账号、主机路径、密钥、模型回答或原始日志。缺项、fixture、错包、错误架构／基线、重复记录和无效证据会阻止发行；不要修改摘要来匹配重建包。

## 3. 运行验收与发布

手动触发 **Publish qualified release**，使用同一个版本 tag 作为 ref，输入成功候选运行的 `candidate_run_id`、`candidate_run_attempt` 和公开 `qualifications` JSON。CLI 可明确指定 tag 并从文件读取 JSON：

```sh
gh workflow run release-publish.yml --repo OWNER/REPO --ref vX.Y.Z \
  -f candidate_run_id=RUN_ID -f candidate_run_attempt=ATTEMPT \
  -F qualifications=@/absolute/public-qualifications.json
```

先把仓库、版本、运行编号及文件路径换成实际值。候选来源必须是当前仓库的 Release candidates、由同一 tag/commit 的 push 触发，且指定的是已成功的最新 attempt，完整 Artifact 尚未过期。

Workflow 下载原候选，在匹配平台重新检查身份和归档，再执行真实 DSH/ACP、工具取消、原 Session 恢复及生产页面浏览器验收，最多并行 2 项。随后将自动结果与人工记录按归档摘要核对。所有 8 个包的必需项通过后，才汇总发行材料。

可以先省略人工 JSON 检查自动验收，但缺项会使发行停止；重跑真实验收会再次消费模型额度。

发布 job 创建 draft，上传原候选，下载远端资产重新计算摘要，完整匹配后公开。发布时不重新构建，也不改动已验收归档。正式 Release 包含各包的归档、摘要和验收报告，以及 `install.sh`、`release.json`、`SHA256SUMS`、`LICENSES.tar.gz`、使用说明和发行说明。

汇总时从同一 tag 的 `packaging/install.sh` 生成独立安装器，固定版本与仓库，纳入 `SHA256SUMS` 并在 `release.json` 记录摘要。发布前重新核对脚本与受验源码，版本下载使用固定 tag，避免安装期间 latest 移动而混用归档和摘要。发布完成后 `https://github.com/OWNER/REPO/releases/latest/download/install.sh` 即为一条命令的安装入口；脚本内部下载本机 standard 并完成常驻安装。文档中的默认仓库为 `go-aie/teamx`。

安装器自动测试包含本机 Bash／tar／摘要和文件操作，Linux 账号与 sudo／管理器用 fixture 模拟；不能代替真实 macOS 下载与 LaunchAgent、原生 Linux systemd 的安装和启停验收。除原部署资格外，还应在真实发布后核对管道安装、PATH 生效和安装后 `teamx service status/stop/start`，再确认公开入口可用。

## 按需生成离线包

需要向无法访问 GitHub 的用户转交一个文件时，可在正式发布完成后，用原始 standard 归档运行 `make package-offline ARCHIVE=/absolute/teamx-standard.tar.gz`（文件名使用实际值）。辅助脚本验证原摘要并生成单独的 `dist/offline/*-offline.zip`，收件人解压后运行 `bash ./install-offline.sh`。可传 `OUTPUT` 指定新路径；不要把补充 ZIP 直接放入已封存的发行资产目录，以免改变资产集合。原归档不变，补充包不扩大验收资格。具体选项见[按需生成单文件离线包](README.zh.md#按需生成单文件离线包)。

## 失败后的处理

- **构建、下载复验或验收失败**：检查对应 job 和资格报告，解决缺项；候选字节变化时重新验收。验收或汇总失败，应完整重跑发行 Workflow。
- **发布 job 失败**：可在同一发行运行中仅重跑失败的 publish job，复用原汇总 Artifact。同一 draft 仅在版本、commit 和摘要完全一致时恢复，已上传资产先下载核验，再补传缺项。
- **新一轮发行遇到旧 draft**：新的验收运行会产生新的上下文，不能覆盖不匹配的 draft。先检查旧 draft，由维护者明确处理后再发起发行。
- **公开结果不确定**：先检查远端 Release。流程不会自动覆盖已公开版本，也不使用 `--clobber` 替换资产。

## 可选的 macOS 签名与公证

当前发行不要求 Apple 证书，默认 `unsigned`；下载后的系统启动行为仍是必需验收项。需要签名时，在构建候选前设置变量 `TEAMX_MACOS_SIGNING=developer-id` 和完整的 `TEAMX_SIGN_IDENTITY`（`Developer ID Application: 名称 (TEAMID)`），并配置：

- `TEAMX_MACOS_P12_BASE64`、`TEAMX_MACOS_P12_PASSWORD`：Developer ID Application 证书及私钥的 PKCS#12 导出和密码。
- `TEAMX_APPLE_API_KEY_BASE64`、`TEAMX_APPLE_API_KEY_ID`、`TEAMX_APPLE_API_ISSUER`：App Store Connect API 私钥及对应标识。

证书在 Apple Developer 账号的 Certificates 中创建，API key 在 App Store Connect 的 Users and Access／Integrations 中创建，凭据保存到 GitHub Secrets。Workflow 使用临时 keychain，签名和公证成功后才生成最终归档；失败不会退回 unsigned。裸 CLI 归档使用在线公证票据，不宣称已 staple。签名会改变程序字节，须重新构建候选并验收。
