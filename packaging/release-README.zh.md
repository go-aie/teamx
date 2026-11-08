# TeamX 下载、使用与升级

选择与系统、架构匹配的 `.tar.gz` 归档。包内包含 TeamX、固定 Node/DSH 和团队模板，
运行无需安装 Go 或 Node。模型服务、API Key、平台账号及开发任务自己的工具链由使用者配置。

| 包 | 内容与可选能力 |
| --- | --- |
| core | TeamX、Node/DSH、团队模板；普通对话协作无需 Git。Git Workspace 需要自行安装 Git，GitLab／飞书能力需要另装兼容 CLI |
| standard（推荐） | core 的内容，加 glab、lark-cli 和配套资源；平台账号需登录和绑定，Git Workspace 仍需自行安装 Git |
| full | standard 的内容，加完整 Git 运行时；无需另装 Git |

Scion 当前暂不随包提供运行能力。DSH 固定为 `0.2.2-rc.1` 预发布版本，精确依赖见包内 `manifest.json`。
正式发行默认提供 core 和 standard，full 仅在依赖与验收齐全时提供。包的可用平台、系统基线与已完成的验收范围以对应下载页面和报告为准。

## 下载与校验

正式 Release 提供归档和全局 `SHA256SUMS`，开发快照提供归档及同名 `.tar.gz.sha256`。
若从 Actions 下载 Artifact，先解开外层压缩包，再取其中的 `.tar.gz` 与摘要。

对归档计算 SHA-256，与下载材料中相同文件名的一行逐项比较；将下列文件名替换为实际值：

```sh
# macOS
shasum -a 256 teamx-archive.tar.gz
# Linux
sha256sum teamx-archive.tar.gz
```

确认摘要一致后解压，并进入实际生成的目录；文件名和目录名均替换为实际值：

```sh
tar -xzf teamx-archive.tar.gz
cd teamx-archive
```

## 首次启动

在解压目录执行：

```sh
./start init
# 编辑输出位置的 config.toml，设置 runtime.model 的 protocol、base_url 和 model_id。
export OPENAI_API_KEY='自己的模型密钥'
./start doctor
./start
```

浏览器访问配置的监听地址，默认 `http://127.0.0.1:8088`。按 Ctrl-C 停止服务，等待退出后再启动。
默认凭据变量是 `OPENAI_API_KEY`；若修改了 `api_key_env`，请导出对应变量。
`doctor` 会显示缺少的模型配置或运行条件，不会发送模型 prompt。

默认配置、团队模板和私有数据保存在包内 `.teamx/`。`init` 只补充缺失文件，保留已有配置与模板修改。
也可使用独立目录；初始化、诊断和启动时始终传入同一个 `--home`：

```sh
./start init --home "$HOME/.teamx"
# 编辑该目录的 config.toml，并配置模型凭据。
./start doctor --home "$HOME/.teamx"
./start --home "$HOME/.teamx"
```

可以从其他目录通过绝对路径调用 `start`。使用独立 home 时包本体可只读，home 必须可写，同一个 home 同时只运行一个服务实例。
平台登录、连接绑定和通知订阅由使用者显式配置，启动不会自动启用。

## 系统要求与 macOS 下载提示

系统需提供 shell、常用命令、证书和进程观察权限。macOS 的 DSH 命令工具需要
`/usr/bin/sandbox-exec`，Linux 需要可用的沙箱后端；系统与动态库基线见
`manifest.json` 中的 `qualification_baseline`。不支持 Windows 或 Alpine/musl。

macOS 包可能未进行 TeamX Developer ID 签名或 Apple 公证。正式发行的具体状态见下载页面与
`release.json` 中对应包的 `signing` 字段；开发快照不承诺已完成 TeamX 签名／公证。上游或 ad-hoc 签名不代表 TeamX 已签名。
首次启动若被系统拦截，先核对下载来源与归档 SHA-256，再按系统提示在
“系统设置 → 隐私与安全性”中允许运行相应程序并重试；不要全局关闭 Gatekeeper。
遇到无法允许的系统限制时，保留报错并报告问题。

## 升级

停止旧服务并等待退出，备份私有 home。新版本解压到独立目录，先用新的 home 检查，再显式指定原 home。
重复初始化不会自动迁移连接、覆盖旧模板或更新已有运行时路径。

保留旧包与旧运行时路径：已有 Room 冻结了原 DSH 路径，平台连接也可能引用旧 CLI 的绝对路径。
需要让新 Room 使用新运行时时，显式更新配置中的路径；切换已有 Room 的运行时应使用 Session 结清／轮换或新建 Room。
不要删除旧包后期待历史 Session 自动恢复。数据格式变化时，回退程序也不能替代数据备份与恢复。

## 发行材料与许可

`SHA256SUMS` 覆盖发布资产，`release.json` 和每个 `*-assessment.json` 记录绑定到
原归档摘要的发行资格，区分自动检查与维护者人工验收。包内 `manifest.json` 保留构建时状态，
不会为了更新资格重新构建或修改归档。开发快照没有完整正式发行资格，不能沿用其他归档的报告。

`LICENSES.tar.gz` 汇总各包的许可材料，各归档也保留自己的 `LICENSE`、
`THIRD_PARTY_NOTICES/` 和运行时许可。full 的 Git 对应源码与构建材料位于
`THIRD_PARTY_NOTICES/git-source/dugite/`，说明见 `THIRD_PARTY_NOTICES/Git-README.zh.md`。
