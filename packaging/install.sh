#!/bin/bash
# The release assembler pins these two values before publishing install.sh.
set -euo pipefail
release_version='@TEAMX_VERSION@'
release_repository='@TEAMX_REPOSITORY@'
[[ $release_version != @* ]] || release_version=''
[[ $release_repository != @* ]] || release_repository='go-aie/teamx'

fail() { printf 'TeamX: %s\n' "$*" >&2; exit 1; }
usage() {
  cat <<'EOF'
TeamX 安装：下载并校验 standard 包，安装和启动常驻服务。
  --version vX.Y.Z     指定正式版本（发行脚本默认安装其所属版本）
  --archive FILE       使用已下载的本机 standard 归档，无需联网
  --sha256 HEX         归档摘要；离线默认读取 FILE.sha256
  --bin-dir DIR        teamx 命令目录（macOS: ~/.local/bin；Linux: /usr/local/bin）
  --server-root DIR    程序安装根；与 --home 不重叠的绝对目录
  --home DIR           私有配置和数据目录
  --user NAME          Linux 专用服务账号（默认 teamx，不存在时创建）
  --help               显示帮助
安装后使用 teamx service status/stop/start/restart/uninstall，无需再次 install。
EOF
}
archive='' expected_sha='' bin_dir='' server_root='' data_home='' service_user='teamx'
while [[ $# -gt 0 ]]; do
  case $1 in
    --help|-h) usage; exit 0 ;;
    --version|--archive|--sha256|--bin-dir|--server-root|--home|--user)
      [[ $# -ge 2 && -n $2 ]] || fail "$1 缺少参数"
      case $1 in
        --version) release_version=$2 ;; --archive) archive=$2 ;; --sha256) expected_sha=$2 ;;
        --bin-dir) bin_dir=$2 ;; --server-root) server_root=$2 ;; --home) data_home=$2 ;; --user) service_user=$2 ;;
      esac
      shift 2 ;;
    *) fail "未知参数：$1（使用 --help 查看帮助）" ;;
  esac
done

platform=$(uname -s)
case $(uname -m) in
  arm64|aarch64) architecture=arm64 ;; x86_64|amd64) architecture=amd64 ;;
  *) fail '仅支持 arm64 和 amd64 原生架构' ;;
esac
needs_sudo=false
run_privileged() {
  if [[ $needs_sudo == true ]]; then sudo "$@"; else "$@"; fi
}
case $platform in
  Darwin)
    [[ $(id -u) != 0 ]] || fail 'macOS 请在已登录普通用户的终端运行，不使用 sudo'
    [[ $service_user == teamx ]] || fail '--user 仅用于 Linux'
    target="darwin-$architecture"
    server_root=${server_root:-"$HOME/Library/Application Support/TeamX/server"}
    data_home=${data_home:-"$HOME/Library/Application Support/TeamX/home"}
    bin_dir=${bin_dir:-"$HOME/.local/bin"}
    ;;
  Linux)
    command -v systemctl >/dev/null || fail '常驻安装需要 systemd'
    [[ $service_user =~ ^[a-z_][a-z0-9_-]*$ ]] || fail '无效的 Linux 服务账号名'
    target="linux-$architecture"
    server_root=${server_root:-/opt/teamx}
    data_home=${data_home:-/var/lib/teamx}
    bin_dir=${bin_dir:-/usr/local/bin}
    if [[ $(id -u) != 0 ]]; then
      command -v sudo >/dev/null || fail 'Linux 安装需要 root 权限，请准备 sudo 或由管理员运行'
      needs_sudo=true
    fi
    ;;
  *) fail '仅支持 macOS 和使用 systemd 的 Linux' ;;
esac
for directory in "$server_root" "$data_home" "$bin_dir"; do
  [[ $directory == /* && $directory != *$'\n'* && $directory != *$'\r'* && $directory != *'\'* ]] || fail '安装目录必须为不含换行或反斜杠的绝对路径'
done
command_target="$server_root/current/bin/teamx"
command_path="$bin_dir/teamx"
if [[ -e $command_path || -L $command_path ]]; then
  [[ -L $command_path && $(readlink "$command_path") == "$command_target" ]] || fail "$command_path 已存在且不属于此次安装，请使用其他 --bin-dir"
fi

umask 077
work=$(mktemp -d "${TMPDIR:-/tmp}/teamx-install.XXXXXXXX")
trap 'rm -rf -- "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
download() {
  curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL --retry 2 --connect-timeout 15 "$1" -o "$2"
}
if [[ -z $archive ]]; then
  [[ $release_version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail '源码脚本联网安装需指定 --version vX.Y.Z；也可使用 --archive'
  command -v curl >/dev/null || fail '请先安装 curl'
  archive_name="teamx-$release_version-$target-standard.tar.gz"
  release_url="https://github.com/$release_repository/releases/download/$release_version"
  printf '正在下载 TeamX %s（%s）…\n' "$release_version" "$target"
  download "$release_url/SHA256SUMS" "$work/SHA256SUMS"
  recorded_sha=$(awk -v name="$archive_name" '$2 == name { print $1; count++ } END { if (count != 1) exit 1 }' "$work/SHA256SUMS") || fail '发行摘要缺失或存在重复归档记录'
  [[ -z $expected_sha || $expected_sha == "$recorded_sha" ]] || fail '指定摘要与发行摘要不一致'
  expected_sha=$recorded_sha
  archive="$work/$archive_name"
  download "$release_url/$archive_name" "$archive"
else
  [[ -f $archive ]] || fail "找不到归档：$archive"
  if [[ -z $expected_sha ]]; then
    [[ -f $archive.sha256 ]] || fail '请保留归档旁的 .sha256 文件，或传入 --sha256'
    expected_sha=$(awk 'NR == 1 { print $1 } END { if (NR != 1) exit 1 }' "$archive.sha256") || fail '无效的归档摘要文件'
  fi
fi
[[ $expected_sha =~ ^[a-fA-F0-9]{64}$ ]] || fail 'SHA-256 必须为 64 位十六进制摘要'
expected_sha=$(printf '%s' "$expected_sha" | tr 'A-F' 'a-f')
if [[ $platform == Darwin ]]; then
  actual_sha=$(shasum -a 256 "$archive")
else
  actual_sha=$(sha256sum "$archive")
fi
[[ ${actual_sha%% *} == "$expected_sha" ]] || fail '归档 SHA-256 不一致，未执行包内程序'

# Extract only the verified bootstrap executable to stdout. No archive paths or
# extended attributes are restored; the Go installer validates/extracts the full
# archive into its managed release directory. Never execute an unpacked start.
bootstrap_entry=$(tar -tzf "$archive" | awk -F/ 'NF == 3 && $1 != "" && $1 != "." && $1 != ".." && $2 == "bin" && $3 == "teamx" { print; count++ } END { if (count != 1) exit 1 }') || fail '归档必须包含唯一的 bin/teamx'
tar -xOzf "$archive" "$bootstrap_entry" > "$work/teamx"
[[ -s $work/teamx ]] || fail '归档中的 bin/teamx 不是有效的引导文件'
chmod 0755 "$work/teamx"

if [[ $platform == Linux ]]; then
  if [[ $needs_sudo == true ]]; then sudo -v; fi
  if ! id "$service_user" >/dev/null 2>&1; then
    run_privileged useradd --system --create-home --home-dir "/home/$service_user" --shell /usr/sbin/nologin "$service_user"
  fi
fi
(umask 022; run_privileged mkdir -p "$bin_dir")
[[ -d $bin_dir ]] || fail '无法创建命令目录'
printf '正在安装并启动 TeamX 常驻服务…\n'
install_args=(service install --archive "$archive" --sha256 "$expected_sha" --server-root "$server_root" --home "$data_home")
if [[ $platform == Linux ]]; then install_args+=(--user "$service_user"); fi
# The private download directory stays 0700, while the managed executable tree
# must be traversable by Linux's ordinary service account. Go creates home and
# credential files with their own private modes.
(umask 022; run_privileged "$work/teamx" "${install_args[@]}" </dev/null > "$work/install-result.json")
[[ -x $command_target ]] || fail '部署未产生可执行的 current/bin/teamx，请保留安装报错'

# Publish the stable CLI only after deployment succeeds. Refuse unrelated files,
# including a destination created during installation.
if [[ -e $command_path || -L $command_path ]]; then
  [[ -L $command_path && $(readlink "$command_path") == "$command_target" ]] || fail "$command_path 在安装期间发生变化；服务已安装，请检查命令入口"
else
  run_privileged ln -s "$command_target" "$command_path"
fi

if [[ $platform == Darwin ]]; then
  case :${PATH:-}: in
    *:"$bin_dir":*) ;;
    *)
      # Quote literal paths (including spaces, quotes and dollar signs), without
      # evaluating them as shell code. Update only this user's shell startup file.
      quoted_bin=$(printf '%s' "$bin_dir" | sed "s/'/'\\\\''/g")
      path_line="export PATH='$quoted_bin':\"\$PATH\""
      case ${SHELL:-/bin/zsh} in
        */zsh) profiles=("$HOME/.zshrc") ;;
        */bash)
          login_profile="$HOME/.bash_profile"
          for candidate in "$HOME/.bash_profile" "$HOME/.bash_login" "$HOME/.profile"; do
            if [[ -f $candidate ]]; then login_profile=$candidate; break; fi
          done
          profiles=("$HOME/.bashrc" "$login_profile")
          ;;
        *) profiles=("$HOME/.profile") ;;
      esac
      for profile in "${profiles[@]}"; do
        if ! grep -Fqx "$path_line" "$profile" 2>/dev/null; then
          printf '\n# TeamX command\n%s\n' "$path_line" >> "$profile"
        fi
      done
      printf '已配置 PATH。请新开终端后使用 teamx；当前终端可执行：\n%s\n' "$path_line"
      ;;
  esac
fi
if [[ $platform == Linux ]]; then
  printf '\n查看服务状态：sudo teamx service status\n'
else
  printf '\n查看服务状态：teamx service status\n'
fi
printf '命令位置：%s\n' "$command_path"
