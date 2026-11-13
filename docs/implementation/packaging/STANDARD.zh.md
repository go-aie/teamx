# standard 包与正式范围调整

日期：2026-11-08。按用户确认，在 core/full 之间增加 standard，默认构建和推荐下载均使用 standard。本期正式矩阵明确调整为四平台 × core/standard 共 8 项；full 留作开发／候选，不以失败后动态缩减矩阵来绕过缺项。

## 实现

- 统一 lock 新增 standard（Node、DSH、glab、lark-cli），full 在其基础上增加 Git。`release.profiles` 明确声明 core/standard，`recommended_profile` 为 standard。四个平台均保留，Linux full 的 TLS 缺项未消除。
- `package-profiles.mjs` 提供公共包型／依赖判断。平台组装按依赖分别收集 CLI 与 Git，standard 不下载或复制 Git。manifest 必须完整且只声明该包型的依赖，不接受缺少 CLI 的 standard 或多带 Git 的依赖声明。
- Bundle doctor 支持三档，按 manifest 依赖将 glab／lark-cli／Git 标为必需或可选，拒绝缺少声明的 standard。standard 没有宿主 Git 时仍可用于普通协作及平台 API 能力；Git Workspace 和依赖本地 Git 的 glab 命令需要另装 Git。
- smoke 对所有声明的 CLI 执行架构、版本和无凭据帮助检查，只有包含 Git 时检查其 helper/TLS、模板、LFS 和源码材料。资格评估同样拆分：standard/full 必需平台登录／只读／绑定证据，Git/TLS/clone/commit 只对含 Git 的包必需。
- 开发快照支持三档，PR 与手动默认 standard。tag 候选按 `release.profiles` 构建；手动候选可选 release/core/standard/full。正式发布仍只接受同一 tag push 触发、完整覆盖声明矩阵的成功候选运行，单档手动候选不能替代正式矩阵。
- 汇总、源运行检查、校验和与发布都复用上述明确矩阵。macOS 仍允许未签名、未公证，其他真实验收与人工项继续按归档摘要核对。

操作入口见 [packaging 指南](../../../packaging/README.zh.md)与[正式发行指南](../../../packaging/releasing.zh.md)。实施计划已注明此次范围覆盖原来“full 必须同期交付”的要求；旧 P0–P4.3 记录保留其历史证据。

## 验证

- 固定 Node 24.22.2 下 39 项打包脚本测试通过。新增／调整覆盖 standard 归档身份、缺少 glab／多声明 Git 拒绝、三档架构检查、默认推荐包型、正式范围不得缺少 core/standard、standard 必需平台证据但无 Git gate，以及 Linux full TLS 阻塞不影响 core/standard 汇总。合成报告及远端传输 fixture 不是实际模型、平台或 GitHub 验收。
- `go test ./internal/room -run '^TestBundle' -count=1 -timeout=3m` 与 `go vet ./internal/room` 通过，覆盖三档 doctor 必需项和 standard 缺少 glab 声明的拒绝。
- 三个 Workflow 通过 Actionlint，离线 lock／资产清单、JS 语法、文档链接／围栏与空白检查通过。
- 原生 macOS arm64，Go 1.27.1，使用已锁定的离线缓存，通过 `package.sh --channel dev --profile standard --offline` 组装并验证最终归档。包内 Node 24.22.2、DSH 0.2.2-rc.1、glab 1.127.0、lark-cli 1.0.99；没有组装 Git。受限 PATH 下，架构／版本／帮助、doctor、初始化保留、首页及 Room、信号退出和重启、独立 home／只读包体与 ACP fixture 均通过。
- 另读取最终 tar.gz 的 manifest 和文件清单，确认只声明 node/dsh/glab/lark-cli，包含 bin/glab 和 bin/lark-cli，不包含 bin/git 或 runtime/git。

本机开发归档位于 `.packaging/standard-profile-check-01/teamx-dev-f8fe4031063e-dirty-darwin-arm64-standard.tar.gz`，SHA-256：

```text
b210435e34623da6a753644eee55da6a17919e45a4feea5c064104a704dd8463
```

Node 测试、构建日志和额外归档检查分别保存在忽略目录的 `standard-node24-tests.log`、`standard-profile-build.log`、`standard-archive-inspection.json`；原始 smoke 报告随开发归档保存在其输出目录。

本次未调用真实模型、未登录业务平台，也未提交／推送或触发 GitHub Workflow。四平台 standard 真实模型／浏览器、平台账号及用户环境验收、远端传递与正式发行仍待执行；本机离线通过不代表它们已通过。full 继续保留原有依赖和验收缺项。
