# GitHub Release 自动发布简化方案

状态：第 1–4 步已实施，见[实施记录](implementation/github-release-simplification.zh.md)。仓库远端设置未修改，未触发 GitHub 构建或发布；真实发布闭环与既有 Go 检查阻塞仍待处理。以下保留完整目标方案及实施前的差距。

## 1. 目标与边界

维护者在本地完成必要测试、确认版本可发布后，推送 `vX.Y.Z` tag。CI 自动完成检查、多平台构建、归档检查和 GitHub Release 发布，附带 README 安装命令需要的 `install.sh`。

正式发布链路直接移除真实模型、浏览器和人工验收的强制依赖，不增加兼容开关、可选审批或新的验收材料上传入口。真实环境验收由维护者在打 tag 前安排，CI 不读取、不要求这些报告。

本轮只调整分发与发布设施，不修改模型配置、Room、平台授权或常驻服务的业务行为。保持四个平台、core/standard 共 8 个正式包；standard 仍为推荐包型。full 保留为开发／实验包型，Scion 暂缓，Electron 和原生手机 App 的后续范围不变。

## 2. 实施前基础与差距

| 位置 | 当前行为 | 调整方向 |
| --- | --- | --- |
| [release.yml](../.github/workflows/release.yml) | tag 或手动触发，构建候选、下载复验，记录验收缺项 | 收敛为 tag 触发的完整发布工作流 |
| `release-publish.yml`（已删除） | 手动填写候选 run ID、attempt、qualifications；真实验收后发布 | 删除；资产汇总和发布 job 合并到 release.yml |
| [package-release.mjs](../scripts/package-release.mjs) | 归档身份检查与真实验收资格计算混在一起 | 保留身份与归档检查，删除资格计算 |
| [package-publish.mjs](../scripts/package-publish.mjs) | 查询另一轮候选运行，强制要求验收报告，再汇总上传 | 改为消费当前工作流产物和 CI 检查报告 |
| [package.mjs](../scripts/package.mjs) | 普通 release 构建调用 `check-packaging --require-ready`，候选模式才绕过 | 删除人工资格门槛，统一正式构建入口 |
| [check-packaging.mjs](../scripts/check-packaging.mjs) | 校验固定依赖与资产，也可按历史状态判定发行就绪 | 保留依赖、资产和实际组包约束，删除历史验收资格判定 |

现有测试、原生 runner 构建、依赖锁定、归档 smoke、安装器生成、许可汇总、上传后下载校验与草稿恢复机制均可复用。主要工作是删除资格审批层，并接通已实现的构建和发布步骤。

## 3. 目标工作流

```text
push refs/tags/vX.Y.Z
  → checks：版本与源码检查、自动化测试、静态检查
  → build：四个平台 × core/standard 原生构建，执行归档 smoke
  → verify：独立 job 下载同一归档，复核身份并再次执行 smoke
  → assemble：检查完整矩阵，汇总原始归档、安装器、许可与校验和
  → publish：创建草稿、上传、下载核对摘要、正式发布
```

### 3.1 入口与权限

- `release.yml` 只保留 tag push 入口。触发过滤可以仍为 `v*`，但最早的检查必须严格接受 `vX.Y.Z`，拒绝预发布后缀、同名分支和不一致的源码。保留 annotated tag 支持，以 tag 解引用后的 commit 为准。
- 删除 release 工作流的手动包型选择。需要手动构建时使用现有 [package.yml](../.github/workflows/package.yml)，继续生成开发快照，不创建正式 Release。
- 所有上游 job 只授予读取所需权限，最终 publish job 才拥有 `contents: write`，使用内置 `GITHUB_TOKEN`，无需模型或平台凭据。
- 移除 `release-acceptance` 与 `release` Environment 引用，避免仓库环境审批使 tag 发布仍然需要点击确认。可选 Apple 签名继续从仓库现有变量／Secrets 读取，默认 unsigned，不新增签名要求。
- 同一 tag 的发布串行执行，保留 `cancel-in-progress: false`。任一必要检查或矩阵成员失败，均不能发布。

### 3.2 保留的 CI 检查

- 现有 `check-packaging` 清单校验、脚本测试、`make test`、`go vet ./...` 与 Web 的 Node 测试。这些 Web 测试不等于真实浏览器验收，无需安装 Chromium。
- 每个平台原生构建与既有归档 smoke：文件、摘要、权限、内部链接、依赖版本、DSH 原生模块、服务基本启动／退出，以及使用固定 fixture 的协议检查。
- 下载后的归档身份与 smoke 复验，绑定版本、完整 commit、target、profile、依赖锁和归档摘要。
- 完整矩阵、许可材料、安装器内容及上传后资产摘要检查。

不在本轮扩大 smoke 为真实模型、真实平台账号、手机或长期运行验收，也不为了删除验收门槛而缩小已有构建检查。

## 4. 删除资格审批层，保留本地测试能力

### 4.1 直接删除的发布依赖

- `TEAMX_RELEASE_MODEL_CONFIG`、`TEAMX_RELEASE_MODEL_API_KEY` 的工作流引用、模型配置临时文件和 CI 中的 Playwright／Chromium 安装。
- `candidate_run_id`、`candidate_run_attempt`、`qualifications` 等人工输入，以及跨工作流查询“已成功候选运行”的编排逻辑。
- `assessCandidate`、`checkAssessment`、`qualification-template`、`assess` 等发行资格生成与校验入口；不改成始终通过，不将未执行的检查标为 passed。
- `package-qualification.mjs` 和 `server-release-policy.mjs` 中专用于资格审批的模板、观察值投影和硬性门槛；当前消费者主要是上述发布模块及对应测试，实施时确认无其他使用者后删除文件。
- 正式发布依赖中的真实 DSH／模型、浏览器、干净账户、平台登录绑定、设备与 24 小时观察等验收资格要求。

人工验收中有用的场景可留在本地测试说明中，不再维护一套 CI 必须消费的资格 JSON 协议。

### 4.2 保留并解耦的工具

保留 `package-acceptance.sh/.mjs`、`package-acceptance-lib.mjs`、`package-browser.mjs`、`model-package-probe.mjs`、`server-observation.mjs` 和有关真实测试，供维护者按需在本地执行。报告仍如实记录本次范围和结果，不自动参与发布决策。

特别处理 `package-acceptance.mjs` 对 `package-release.mjs` 中 `acceptanceInputs` 的反向依赖：将该常量移到本地验收模块中，优先放入现有 `package-acceptance-lib.mjs`，同步维护其指纹清单和测试。删除发布审批层后，本地工具仍应能独立加载和运行。

不删除历史实施报告，不把此前未完成的真实验收改写为已完成，也不因本方案自动扩大系统或设备支持声明。

### 4.3 普通构建入口与 lock

统一使用 `package.sh --channel release --version vX.Y.Z ...`，删除 `--candidate` 作为绕过资格门槛的开关及相关分支。工作流内部仍可把待上传的归档称为候选产物，但不再形成另一套发行资格状态。

删除 `check-packaging --require-ready` 以及对 `support=accepted`、历史 `native_runtime_acceptance` 状态的发布阻塞。lock 中固定版本、下载地址、摘要、平台基线和真正的依赖闭包约束继续有效；历史验收字段不作为放行依据，也不批量改为通过。

必须保留实际组包限制：例如 full 包需要的 Git 源码材料和 Linux Git/TLS 运行库问题，仍由对应构建检查拒绝。不能通过“解除验收耦合”抹掉实际缺失的依赖。

## 5. 产物、报告与失败恢复

### 5.1 单次运行内传递产物

发布脚本从 GitHub 自动提供的仓库、tag、源码 commit 和当前 run 信息构造上下文，不再要求维护者传参。job 之间通过 `needs`、outputs 和 Artifacts 传递，发布判断依赖上游 job 成功与逐包报告，不查询“当前整个 workflow 已完成成功”——执行 publish 时整个运行尚未结束。

每个归档及下载复验报告必须绑定同一版本、commit、target/profile、SHA-256 和构建批次。汇总时严格要求 8 个包及其对应的 CI 复验报告齐全，不按模糊匹配拼接历史产物，不重新构建或修改已经检查的归档。

### 5.2 正式资产

保留以下资产：8 个 `.tar.gz` 及同名 `.sha256`、固定版本与仓库的 `install.sh`、`SHA256SUMS`、`release.json`、`LICENSES.tar.gz` 和安装说明。归档命名保持不变，现有安装器仍按 standard 文件名和校验和下载。

用明确的逐包 CI 检查报告（建议 `*-verification.json`）替代 `*-assessment.json`，只记录实际完成的归档与 smoke 检查。`release.json` 记录版本、源码、工作流来源、完整矩阵和资产摘要，不承载人工验收结论。

清理新产物中的 `qualified_release`、`release_ready` 等资格状态：建议正式索引使用 `kind=github_release`，归档 distribution 使用 `kind=release_archive`，保留真实签名信息。按结构变化更新相应 schema 和消费者，不伪造旧格式的“验收通过”，不要求兼容旧资格报告。实施时覆盖安装、离线包和 smoke 的所有实际读取位置。

发布说明列出下载、安装方法与 CI 检查范围，删除“四个平台均已通过真实模型、浏览器、平台账号和用户环境验收”等自动生成声明。

### 5.3 重跑与恢复

- 构建或复验失败后，支持完整重跑工作流，生成一批新的完整产物；不支持跨批次补齐失败矩阵。
- 只重试 publish job 时，继续使用原 assemble 输出的已封存资产、摘要和批次。不能直接用本次 `github.run_attempt` 拼接原产物名，否则仅重试发布会找不到之前的产物。
- 同一批次的部分上传失败，可以恢复同一草稿；已有资产必须摘要一致，只补缺失文件。远端 tag 被移动、资产不一致或多出文件时停止发布。
- 已正式发布的同名版本保持不可覆盖。需要新内容时使用新版本 tag，不以重跑覆盖已发布包。

## 6. 实施顺序与文件范围

建议一次交付完整流程，内部按以下顺序推进；不要在中间状态打发布 tag。

| 顺序 | 工作 | 主要文件 |
| --- | --- | --- |
| 1 | 删除资格门槛，解耦本地验收，统一构建与报告语义 | `scripts/package-release.mjs`、`package-publish.mjs`、`package.mjs`、`check-packaging.mjs`、`package-acceptance.mjs`、`package-acceptance-lib.mjs`；删除两个资格策略模块 |
| 2 | 接入同一次运行的 assemble/publish，处理权限、批次与重试 | `.github/workflows/release.yml`；删除 `release-publish.yml` |
| 3 | 替换旧资格测试，保留并补齐实际发布行为测试 | `package-release.test.mjs`、`package-publish.test.mjs` 及受报告／参数变化影响的测试 |
| 4 | 更新当前操作指南和资产说明 | `packaging/releasing.zh.md`、`packaging/README.zh.md`、`docs/installation.zh.md`；README 按需简化安装前提表述 |

实际文件范围比“只串联两个工作流”稍大，因为普通构建入口和本地工具也有依赖。以删除旧审批代码、复用现有交付能力为主，不新增通用发布框架。历史方案和实施记录保留为历史，不逐篇改写。

旧 Environment 和 Secrets 在工作流移除引用后不再必需；清理远端配置属于实施时的收尾操作，本方案不执行任何远端修改。可选 Apple 签名凭据如原来只配置在 Environment，需在迁移时检查其仓库级可用性。

## 7. 验证与完成条件

### 7.1 本地可验证部分

- 更新后的脚本测试覆盖：无真实验收材料可正常汇总；非法 tag、源码或摘要不一致、缺少任一包／CI 报告必须失败；签名模式和实际依赖限制继续生效。
- 发布模拟测试覆盖：成功发布、上传中断恢复、发布 job 重试复用原封存资产、远端损坏与 tag 移动拒绝、已发布版本不可覆盖。模拟传输沿用现有注入方式，不创建远端 Release。
- 运行 `node scripts/check-packaging.mjs`、`node --test scripts/*.test.mjs`、`make test`、`go vet ./...`、`node --test internal/web/tests/web*_test.mjs`；检查工作流语法及 job 依赖。
- 确认本地验收工具不再依赖已删除的发布资格模块，帮助／参数与报告指纹自洽。无需为了验证此次流程改造消耗真实模型额度。
- 检查本方案涉及的文档链接和安装资产格式，不把测试内部清单放入面向使用者的 README。

### 7.2 GitHub 实际验证

实现与本地检查通过后，在维护者决定正式发布的版本上验证完整闭环：推送 tag 后无需填写 run ID、模型凭据、人工 JSON 或点击环境审批，8 个包检查成功后自动发布；Release 中存在安装器、匹配的包、许可和校验和，资产摘要与构建产物一致。

实际 Release 是公开发布动作，应在后续实施任务明确包含发布、或维护者主动推送版本 tag 时执行。当前方案及未来仅完成本地代码修改，不等于已经验证 GitHub 发布闭环。

完成标准：维护者日常操作仅为“本地确认版本 → 推送版本 tag”，CI 自动完成构建与发布；真实环境测试保留为独立本地能力，发布链路中不存在旧资格门槛或其变形。
