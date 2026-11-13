# GitHub Release 简化实施记录

2026-11-13：完成[方案](../GITHUB-RELEASE-SIMPLIFICATION-PLAN.zh.md)第 1–4 步：删除资格门槛、统一构建与报告语义，接入同次运行的自动发布工作流，补齐发布行为回归测试和当前操作指南。真实 GitHub 发布闭环尚未验证，既有 Go 检查阻塞仍待解决。

## 第 1 步：构建与报告

- 删除 `assessCandidate`、`checkAssessment`、`assess`、`qualification-template`，以及 `package-qualification.mjs`、`server-release-policy.mjs`。汇总不读取真实模型、浏览器、账号、设备或观察报告。
- `acceptanceInputs` 移至 `package-acceptance-lib.mjs`；本地验收仍按实际执行范围报告结果，原有真实测试和工具保留。开发快照及本地报告也移除 `release_ready` 字段。
- 正式构建统一为 `package.sh --channel release --version vX.Y.Z ...`。删除 `--candidate`、`--require-ready` 和历史验收状态门槛，保留干净源码、准确 tag、固定依赖、Git 对应源码和 Linux full Git/TLS 限制。
- 正式矩阵固定为四个平台 × core/standard 共 8 包。full 仍可通过开发快照入口使用，不进入正式矩阵。
- 归档内 `distribution` 使用独立的 schema 2，`kind=release_archive`，保留真实签名结果；CI 构建额外保存 `{run_id, run_attempt}`。基础 manifest 仍为 schema 1，安装器、离线包和服务读取的基础身份结构保持一致。
- 归档 receipt、CI verification 和 Release 索引使用 schema 2。`*-verification.json` 绑定版本、完整 commit、target/profile、依赖锁、归档及报告摘要、校验器指纹和构建批次，记录构建 smoke 与下载后原生 smoke 的实际结果。`release.json` 使用 `kind=github_release`，包含来源工作流、原始批次、完整矩阵与资产摘要。
- 汇总保留原始归档字节，生成固定版本／仓库的安装器、许可归档、安装说明与校验和。发布继续使用草稿、上传后下载核对和相同封存资产恢复机制，不覆盖已发布版本。

## 工作流调用接口

```sh
node scripts/package-release.mjs plan VERSION
node scripts/package-release.mjs record DIR TARGET PROFILE COMMIT VERSION
node scripts/package-release.mjs verify DIR TARGET PROFILE COMMIT VERSION REPORT
node scripts/package-publish.mjs plan CONTEXT
node scripts/package-publish.mjs assemble CANDIDATES VERIFICATIONS CONTEXT OUTPUT
node scripts/package-publish.mjs publish ASSETS CONTEXT CHECKSUMS_SHA256
```

发布上下文从 GitHub 当前 tag push 的环境变量生成，无需填写候选 run ID，也不查询尚未结束的整个运行是否成功。上游 job 的成功状态由第 2 步的 `needs` 负责约束。

同一构建批次的目录分别为 `candidate-VERSION-TARGET-PROFILE-ATTEMPT/` 和 `candidate-verification-VERSION-TARGET-PROFILE-ATTEMPT/`。后者只放 `verification.json`；前者保留归档、同名摘要、构建 smoke、安全报告和 `candidate.json`。缺失、额外或不同批次的包／报告均拒绝汇总。

只重试发布时，复用封存的 `CONTEXT`、资产目录和 checksum seal；发布脚本不使用重试时的新 attempt 改写原批次。

## 第 2 步：同次运行的自动发布

- `release.yml` 只接受 tag push，最早的 plan 步骤核验严格版本、tag 解引用后的完整 commit、干净源码和当前工作流来源，并输出原始批次上下文。移除手动包型选择，`package.yml` 的开发快照入口保持原样。
- 串联 `checks → build → verify → assemble → publish`，保留四个平台 × core/standard 的原生构建、构建 smoke 和独立下载复验。所有必要依赖必须成功后才执行下游，不再放行失败构建后的 verify。
- `assemble` 下载当前运行、当前 attempt 的原始归档与 `verification.json`，保留逐 artifact 目录，由脚本核验完整矩阵和批次后生成发行资产。模型凭据、真实验收、Chromium、人工 JSON 和跨工作流候选查询已从工作流中移除；删除 `release-publish.yml`。
- 上游仅 `contents: read`，只有最终 publish 获得 `contents: write`，使用内置 token。移除两个 Environment 引用，按 tag 串行且不取消正在运行的发布。
- build、verify、assemble 在入口拒绝沿用旧 checks 上下文的部分重跑，提示重跑全部 job，避免拼接新旧批次。完整重跑会生成新的完整产物。
- assemble 输出封存资产的 artifact ID、原始上下文和 checksum seal。publish 按该 ID 下载，并使用原始上下文与摘要；只重试 publish 时不依赖新的 `github.run_attempt`、不重建资产，也不重新 assemble。这利用了 GitHub 对未重跑 job 保留 outputs 的行为，见[官方说明](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs?tool=webui)。
- 可选 Apple 签名继续读取仓库可用的变量与 Secrets，缺省 unsigned。只读核查 `go-aie/teamx`：仓库级 `TEAMX_MACOS_SIGNING` 查询返回 404，仓库 Secrets 列表无 `TEAMX_MACOS_*`／`TEAMX_APPLE_*` 凭据；未读取秘密值或修改远端配置。启用 Developer ID 签名仍需准备工作流要求的仓库可用凭据。

第 2 步验证：

- actionlint v1.7.12 检查 `release.yml` 通过；所有 run 段的 Bash 语法通过。
- 本地解析工作流并核对 tag 入口、权限、依赖、8 个归档／报告名称映射；实际执行三个批次检查的 shell，确认同批次通过、部分重跑拒绝；将 publish 的当前 attempt 从 1 改为 2 后，下载 artifact ID、上下文和 seal 保持一致。
- `node --test scripts/*.test.mjs`：59 项通过，包括模拟发布和中断恢复。未创建远端 Release。
- `git diff --check` 通过；`package.yml` 未修改，发布链路中无旧模型 Secrets、资格入口或 Environment 引用。
- 本步骤未修改 Go 代码，未重复执行全量 Go／Web 检查；第 1 步记录的 `make test` 超时与 `go vet` 诊断仍未解决，工作流保留这些必要检查，失败时会阻塞发布。

## 第 3 步：发布行为回归

本步骤只修改测试及实施记录，未修改生产脚本、工作流或 Go 代码。

- [发布测试](../../scripts/package-publish.test.mjs)的模拟传输保存上传字节副本，下载校验直接计算这些字节的摘要，并核对最终所有资产与本地封存资产完全一致。
- 覆盖 publish 跨 attempt 重试：删除构建和复验目录，从封存副本重新下载，仍使用原上下文与 seal 完成恢复；只补缺失资产。额外覆盖全部上传后响应丢失、最终发布已完成但响应丢失、不同 seal 的草稿，以及已发布版本不可覆盖。
- 覆盖远端字节损坏、错误摘要／长度／状态、重复或额外资产、下载失败、上传少文件及上传期间本地资产变化。失败均不得将草稿转为正式发布；跨仓库／commit／run／attempt 的本地上下文在发起远端请求前拒绝。
- 覆盖远端多层 annotated tag 的解引用、tag 移动、循环及非 commit 目标；覆盖 HTTP 401／403／429／500 不被当作“Release 不存在”，仅显式允许的 404 可表示缺失。
- 逐一移除 8 个包和 8 个对应 CI 报告的目录，确认每个矩阵成员都必需；拒绝额外批次目录。补测实际 plan CLI 对 annotated tag 的输出，包括版本、完整 commit、矩阵、工具版本及原始批次上下文。
- [归档测试](../../scripts/package-release.test.mjs)保留源码、归档、校验器、CI 报告和原生环境绑定检查，补齐非法版本、旧资格命令／绕过参数拒绝、Developer ID 的代码清单与摘要及公证约束、unsigned 不得宣称签名成功。签名报告使用合成 fixture，不代表执行了真实签名。
- [工作流测试](../../scripts/package-workflow.test.mjs)随现有 `node --test scripts/*.test.mjs` 自动运行，无新增依赖。检查 tag 入口、权限、必要 job 依赖、8 个包／报告的 artifact 映射及原始输出绑定；执行工作流中的实际 Bash 批次检查与 publish 片段，确认部分构建重跑被拒绝、publish 重试仍转交原始上下文和摘要，且发布命令失败会保留退出码。发布命令在进程边界使用 stub，隔离 PATH 中没有 GitHub 或网络客户端。
- [离线包测试](../../scripts/package-offline.test.mjs)改用带 schema 2 distribution 的正式归档 fixture，验证仍保留原始归档字节和标准安装入口。

第 3 步验证：

- `node scripts/check-packaging.mjs` 通过；`node --test scripts/*.test.mjs` 共 86 项通过；Web Node 测试 531 项通过。
- actionlint v1.7.12 检查 `release.yml` 通过，工作流 run 段 Bash 语法和 `git diff --check` 通过。测试内的缩进块读取仅用于核对现有工作流结构，完整 YAML 与表达式语法由 actionlint 检查。
- `make test` 复跑：其他 Go 包通过，`internal/room` 再次超过 3 分钟套件总时限（180.507 秒）。超时瞬间的用例刚开始运行，未观察到此前的断言失败；本项仍不能记为通过。
- `go vet ./...` 复跑仍报告第 1 步记录的 7 处既有诊断，未将这些问题改为通过。
- 真实模型、真实浏览器、账号、设备、签名和 GitHub 公开发布均未执行。上述模拟测试不构成多平台发行或 GitHub 自动发布闭环验收。

## 评审补修：草稿查找与恢复

2026-11-13：修复发布重试使用按 tag 查询接口查找草稿的问题。该接口查询已发布 Release；改为使用发布 token 分页列出 Release，精确匹配 tag，再校验草稿正文与资产 seal。全部页面读取成功且没有匹配项时才创建草稿；同一 tag 多条记录、查询失败或列表格式异常均停止发布。已发布版本与不同 seal 的草稿继续拒绝覆盖。

- 修正模拟接口：按 tag 查询不再返回草稿，列表接口按实际分页契约返回草稿及已发布版本。原有中断恢复、跨 attempt 重试和已发布版本保护测试沿用该契约。
- 新增跨页精确查找、同 tag 多条记录、列表 HTTP 错误／格式错误／后续页失败回归；修复前确认测试失败，修复后通过。
- `node scripts/check-packaging.mjs`、`node --test scripts/*.test.mjs`（89 项）及 `git diff --check` 通过。未修改 Go／Web／工作流，本次补修未重复运行其检查；评审已复现的 Go 套件超时与 7 处 vet 诊断仍需解决。
- 未创建或修改远端 Release，本次验证仍为本地模拟。

## 第 4 步：当前操作指南与资产说明

2026-11-13：更新当前文档，保留历史实施报告的原始验收结论。

- 重写 `packaging/releasing.zh.md`：本地检查后推送严格版本 tag，同次运行自动完成 8 包检查与发布；补充完整重跑、单独重试 publish、相同草稿恢复、冲突与已发布版本保护的操作说明。
- 移除当前指南中的手动发布工作流、模型 Secrets 和人工资格 JSON 操作，保留独立本地 DSH／浏览器与长期观察工具，说明验收机依赖、私有材料和实际测试范围。
- 同步 `packaging/README.zh.md`、`docs/installation.zh.md` 和项目 README 的入口及正式资产说明。`packaging/Git-README.zh.md` 是实际随包材料，也清理其旧报告引用；保持 standard 推荐、四平台 × core／standard、full 实验和 Scion 暂缓的边界。
- 发行指南记录 schema 2 的索引与 distribution 格式；安装指南继续使用文档内导航，由现有映射原样导出至归档及 Release，不增加另一份用户说明模板。
- 方案状态更新为第 1–4 步已实施，并在实施索引加入本记录；真实 GitHub 发布闭环和既有 Go 门禁未标为通过。

本步骤验证：打包清单、89 项脚本测试（含发行安装指南导出和离线包回归）、文档本地链接／锚点与 Bash 代码块语法检查、工具帮助入口及 `git diff --check` 通过。本步骤只修改文档，未重复运行 Go／Web／原生构建或真实验收，未修改仓库远端配置或发布 Release。

## 第 1 步验证记录

- 打包清单、构建／本地验收／离线工具帮助入口及旧参数拒绝检查通过；本地验收指纹中的文件全部存在，不再依赖已删除模块。
- `node --test scripts/*.test.mjs`：59 项通过，覆盖无人工验收材料的完整汇总、tag／源码／摘要／批次错误、缺包／缺报告、实际 Git/TLS 限制、签名模式，以及模拟发布和中断恢复；未创建远端 Release。
- Web Node 测试 531 项通过；这些测试不代表真实浏览器验收。
- `make test`：其他 Go 包通过，`internal/room` 超过默认 3 分钟套件总时限，未完成全量验证。超时瞬间运行的 `TestPlatformMemberChatTypeBoundary` 单独复查通过（0.937 秒），不能据此替代全量结果。
- `go vet ./...`：未通过，共 7 处既有诊断，涉及 `access_settings_test.go`、`server_access_test.go` 的未命名字段初始化，以及 `platform_connect_interaction_test.go`、`platform_connect_test.go` 的非测试 goroutine 调用 `Fatal`。这四个文件与 HEAD 一致，本次未修改 Go 文件。

## 后续边界

第 1–4 步的本地改动已完成；历史报告保持原样。全量 Go 检查中的既有问题仍需解决，工作流继续保留这些必要检查。

未修改仓库远端配置，未构建真实多平台发行归档，未消耗真实模型额度，也未验证 GitHub 自动发布闭环。
