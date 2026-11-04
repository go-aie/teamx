Workspace 只提供本地 Git 与限定仓库路径的 askpass；不授予 GitLab API 权限。
平台 API 使用 `"$TEAMX_CLI" platform tools list` 发现当前已启用的房间配置，再调用 `platform tools execute --help`。
项目读取、MR 创建/读取/diff/评审/合并/关闭使用独立配置；没有授权时请用户在房间“工具”配置并为成员启用。不得用 Workspace 凭据或脚本绕过允许操作。
checkout 在自己的 Workspace 执行：先用独立配置读取 MR 的 base/head 与 project.get 的 repository_url；核对本地 origin、干净状态，再 git fetch origin refs/merge-requests/IID/head；确认 FETCH_HEAD 等于期望 head 后 git checkout --detach head，git cat-file -e base^{commit}。之后重新读取 MR 确认版本，变化时停止旧版本评审。保留已有修改，禁止 reset --hard/clean。
