# TeamX 使用说明

本目录包含 TeamX、固定 Node/DSH 和团队模板，运行无需安装 Go 或 Node。
模型服务、API Key、平台账号及开发任务自己的工具链由使用者配置。

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

## 包含的能力

| 包 | 内容与可选能力 |
| --- | --- |
| core | TeamX、Node/DSH、团队模板；普通对话协作无需 Git。Git Workspace 需要自行安装 Git，GitLab／飞书能力需要另装兼容 CLI |
| standard（推荐） | core 的内容，加 glab、lark-cli 和配套资源；平台账号需登录和绑定，Git Workspace 仍需自行安装 Git |
| full | standard 的内容，加完整 Git 运行时；无需另装 Git |

Scion 当前暂不随包提供运行能力。平台登录、连接绑定和通知订阅由使用者显式配置，启动不会自动启用。
DSH 固定为 `0.2.2-rc.1` 预发布版本，精确版本、来源和运行依赖见 `manifest.json`。

系统需提供 shell、常用命令、证书和进程观察权限。macOS 的 DSH 命令工具需要
`/usr/bin/sandbox-exec`，Linux 需要可用的沙箱后端；系统与动态库基线见
`manifest.json` 中的 `qualification_baseline`。不支持 Windows 或 Alpine/musl。

macOS 包可能未进行 TeamX Developer ID 签名或 Apple 公证。首次启动若被系统拦截，
先核对下载来源与归档 SHA-256，再按系统提示在“系统设置 → 隐私与安全性”中
允许运行相应程序并重试；不要全局关闭 Gatekeeper。具体签名状态见下载页面和随附报告。

## 升级

停止旧服务并等待退出，备份私有 home。新版本解压到独立目录，先用新的 home 检查，再显式指定原 home。
重复初始化不会自动迁移连接、覆盖旧模板或更新已有运行时路径。

保留旧包与旧运行时路径：已有 Room 冻结了原 DSH 路径，平台连接也可能引用旧 CLI 的绝对路径。
需要让新 Room 使用新运行时时，显式更新配置中的路径；切换已有 Room 的运行时应使用 Session 结清／轮换或新建 Room。
不要删除旧包后期待历史 Session 自动恢复。数据格式变化时，回退程序也不能替代数据备份与恢复。

## 校验与许可

归档的适用系统和验收范围以下载页面及随附报告为准。开发快照供体验使用；
正式发行资格由绑定同一归档摘要的 `release.json` 和 `*-assessment.json` 确认。
包内 `manifest.json` 保留构建时的状态，不会为了发行资格重新打包。

`checksums.txt` 是包内不可变文件的 JSON 摘要清单，包含文件权限和符号链接目标。
许可证见 `LICENSE`、`THIRD_PARTY_NOTICES/` 及运行时目录。full 的 Git 对应源码和
构建材料位于 `THIRD_PARTY_NOTICES/git-source/dugite/`，说明见 `THIRD_PARTY_NOTICES/Git-README.zh.md`。
