# TeamX 开发预览

解压后运行 `./start init`，编辑输出位置的 `config.toml`，填写
`runtime.model.base_url`、`runtime.model.model_id`，并在终端导出
`OPENAI_API_KEY`。运行 `./start doctor` 查看检查结果，运行 `./start` 启动，
用浏览器打开配置的监听地址（默认 http://127.0.0.1:8088）。Ctrl-C 停止服务。

默认使用包内 `.teamx/` 保存配置、模板和私有数据。从任意目录调用 start 均可。
独立用户目录：`/path/to/start init --home "$HOME/.teamx"`，
启动与诊断时使用同一个 `--home`。包本体可只读，home 必须可写。
再次 init 只补充缺失文件，保留配置和团队修改；不会迁移旧数据。

core 包包含三个 TeamX 程序、固定 Node/DSH 与公开团队模板。
普通对话协作无需 Git。使用 Git Workspace 时自行安装 Git；
使用 GitLab/飞书能力时另行安装兼容 CLI 并登录、绑定连接。
Scion 暂缓。模型、平台账号、软件开发任务所需的工具链均由使用者配置。
诊断中的可选能力缺项不会阻止 core 启动。

full 另含固定 glab、lark-cli 和完整 Git（含 helpers、模板、LFS，以及上游为该目标提供的 GCM）。
无需宿主 Git 或另装这些 CLI；平台账号仍需用户登录并绑定。
Git 对应源码与构建脚本位于 `THIRD_PARTY_NOTICES/git-source/dugite/`，
其中 `git/` 是固定 commit 的 Git 源码；上游依赖清单、构建工作流和许可材料一起保留。
macOS 使用系统框架和 curl；Linux 系统库要求见 manifest 的 qualification_baseline。

DSH 为 0.2.2-rc.1 预发布版；版本、来源和摘要见 manifest.json。
macOS 的 DSH 命令工具依赖系统 `/usr/bin/sandbox-exec`；启动环境须允许发现并运行该命令。
checksums.txt 是不可变文件的 JSON 摘要清单（含权限与符号链接目标）。
许可文本及实际部署的第三方材料见 THIRD_PARTY_NOTICES 和 runtime。
运行不会在线安装、更新运行时或自动登录/开启通知。

开发归档仅验证离线 smoke 与 ACP fixture 接线；真实模型、平台、浏览器及系统兼容性
以各阶段验收记录为准。不能据此推断正式支持或发行已完成。

升级前停止服务并备份私有 home；新包先用隔离 home 检查。保留旧包与运行时路径：
已有 Room 冻结了旧运行时，平台连接也可能保存旧 CLI 绝对路径。
不要删除旧包后期待历史 Session 自动恢复，也不要手工修改恢复指纹。
