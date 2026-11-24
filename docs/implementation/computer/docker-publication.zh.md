# Computer 固定镜像发布与分发核验

原始日志、截图和 JSON 回执仅在本地保存；本文保留验收结论、复跑入口及已知限制。

日期：2026-11-24。C5 的固定候选已推送 GHCR，远端 manifest 内容摘要及 config digest 与仓库镜像锁一致。包当前为 private，匿名访问返回 HTTP 401；公开分发、匿名冷拉取和 Linux 宿主验收仍待完成，C5 不标记全部完成。

## 已完成

- 镜像：`ghcr.io/go-aie/teamx/computer:runtime-a6ce189578019f83`，平台 `linux/amd64`。
- 固定拉取引用：`ghcr.io/go-aie/teamx/computer@sha256:a6ce189578019f83b11346da28a5c3df9a1f44a8f0e3cb0b38272c63819a12b9`。
- Config digest：`sha256:d4a7e89fc1bca9378583ae263cd55ffd70fadb67948dcf9e9c99ce6945ede7a9`。
- 发布回执、推送日志和远端 manifest 记录成功推送与远端内容核验。镜像共 90 层，压缩层合计 3,268,433,899 字节；发布锁与已编译使用的镜像锁相同，无须更换 digest。
- 候选构建记录保存二进制、构建文件及源码摘要。发布前复核确认 Runtime 源码与候选一致；其后变化仅在宿主 Docker 适配器及测试，不改变 Runtime 构建内容。`go test ./internal/computer/docker -count=1` 再次通过。

初次发布调用的 `docker-credential-desktop get` 未返回，已终止该次等待。随后用临时 Docker 客户端配置将 `ghcr.io` 路由到 `osxkeychain`，复用用户已有登录完成推送；未修改用户的 Docker 全局配置，未记录凭据值。发布没有 Git 提交／推送，也没有更新日常服务或容器。

## 公开访问与冷拉取

包元数据确认镜像关联 `go-aie/teamx`，可见性为 `private`；匿名检查未使用用户登录，返回 HTTP 401。完整状态分别记录发布、匿名访问、冷拉取和 Linux 验收，不以推送成功代替分发成功。

需要包管理员在 [Computer 包设置](https://github.com/orgs/go-aie/packages/container/teamx%2Fcomputer/settings) 将可见性设为 Public，然后重新核验匿名访问。GitHub 对 Container registry 的公开匿名访问约定见[官方说明](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility)。本轮浏览器工具连接超时，未代用户修改该设置。

匿名 Docker 拉取必须使用空认证配置，并显式选择已核验的本机引擎：

```sh
computer_docker_host="$(docker context inspect --format '{{.Endpoints.docker.Host}}')"
computer_client_dir="$(mktemp -d)"
printf '%s\n' '{"auths":{}}' > "$computer_client_dir/config.json"
env -u DOCKER_CONTEXT DOCKER_CONFIG="$computer_client_dir" \
  docker --host "$computer_docker_host" pull --platform linux/amd64 \
  ghcr.io/go-aie/teamx/computer@sha256:a6ce189578019f83b11346da28a5c3df9a1f44a8f0e3cb0b38272c63819a12b9
rm -r "$computer_client_dir"
```

即使该命令在已有缓存的引擎上通过，也只能证明匿名拉取接口可用。冷拉取另需空镜像缓存的独立引擎，记录所有层下载、完整 digest、镜像平台及耗时；不删除日常引擎的已有镜像制造冷启动。本轮 macOS 宿主仅余约 4 GiB 空间，镜像解压约 12 GB，未准备额外镜像副本或 Linux 虚拟机。

## Linux 验收边界

已有 macOS 验收覆盖 TeamX → Docker Desktop → Linux 容器中的 Runtime、双屏、真实模型和浏览器启停／登录恢复。Linux 宿主验收还需让 TeamX 本身运行在 Linux，与本机 Docker Engine 配合，覆盖 socket／服务账号权限、容器资源限制、服务与 daemon 重启恢复及原有全局启停闭环；不能从 macOS 结果推导通过。

独立物理 Linux 机器或完整 Linux 虚拟机均可承担这项验收。在 Mac 上准备 Linux 虚拟机、将 TeamX 和 Docker Engine 都放入该虚拟机也可以；当前 Docker Desktop 中仅运行 Computer 容器不等同于该拓扑。入口沿用[托管启停记录](docker-lifecycle.zh.md)中的显式生命周期及浏览器／双模型组合脚本。Linux 仍为未验，未将其作为当前 Mac 使用的前置条件。
