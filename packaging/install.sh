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
  --upgrade            显式升级已有安装（旧版 CLI 的过渡入口）
  --backup-dir DIR     升级备份目标；省略时在登记 home 旁自动生成
  --repair             使用原版本归档重新登记服务（配合 --archive）
  --sha256 HEX         归档摘要；离线默认读取 FILE.sha256
  --bin-dir DIR        teamx 命令目录（macOS: ~/.local/bin；Linux: /usr/local/bin）
  --server-root DIR    程序安装根；与 --home 不重叠的绝对目录
  --home DIR           私有配置和数据目录
  --user NAME          Linux 专用服务账号（默认 teamx，不存在时创建）
  --help               显示帮助
已有安装会在下载应用包前提示退出；升级到最新稳定版：teamx service upgrade。
指定版本：teamx service upgrade --version vX.Y.Z；离线：teamx service upgrade --archive FILE。
macOS 不加 sudo；Linux 管理命令前加 sudo。
EOF
}
archive='' expected_sha='' bin_dir='' server_root='' data_home='' service_user='teamx'
upgrade=false repair=false backup_dir='' explicit_server_root=false
while [[ $# -gt 0 ]]; do
  case $1 in
    --help|-h) usage; exit 0 ;;
    --upgrade) upgrade=true; shift ;;
    --repair) repair=true; shift ;;
    --version|--archive|--sha256|--bin-dir|--server-root|--home|--user|--backup-dir)
      [[ $# -ge 2 && -n $2 ]] || fail "$1 缺少参数"
      case $1 in
        --version) release_version=$2 ;; --archive) archive=$2 ;; --sha256) expected_sha=$2 ;;
        --bin-dir) bin_dir=$2 ;; --server-root) server_root=$2; explicit_server_root=true ;; --home) data_home=$2 ;; --user) service_user=$2 ;;
        --backup-dir) backup_dir=$2 ;;
      esac
      shift 2 ;;
    *) fail "未知参数：$1（使用 --help 查看帮助）" ;;
  esac
done
[[ $upgrade == false || $repair == false ]] || fail '--upgrade 与 --repair 不能同时使用'
[[ $upgrade == true || -z $backup_dir ]] || fail '--backup-dir 仅用于 --upgrade'
[[ $upgrade == false || ( -z $data_home && $service_user == teamx ) ]] || fail '升级复用登记的 home 和账号，不接受 --home 或 --user'
[[ $repair == false || -n $archive ]] || fail '--repair 需要 --archive 指向原版本归档'

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
    bin_dir=${bin_dir:-"$HOME/.local/bin"}
    ;;
  Linux)
    command -v systemctl >/dev/null || fail '常驻安装需要 systemd'
    [[ $service_user =~ ^[a-z_][a-z0-9_-]*$ ]] || fail '无效的 Linux 服务账号名'
    target="linux-$architecture"
    server_root=${server_root:-/opt/teamx}
    bin_dir=${bin_dir:-/usr/local/bin}
    if [[ $(id -u) != 0 ]]; then
      command -v sudo >/dev/null || fail 'Linux 安装需要 root 权限，请准备 sudo 或由管理员运行'
      needs_sudo=true
    fi
    ;;
  *) fail '仅支持 macOS 和使用 systemd 的 Linux' ;;
esac
# A stable command symlink may belong to a custom installation root. Only
# infer this managed layout; never execute an arbitrary command found on PATH.
if [[ $explicit_server_root == false && -L $bin_dir/teamx ]]; then
  linked_target=$(readlink "$bin_dir/teamx")
  case $linked_target in
    /*/current/bin/teamx) server_root=${linked_target%/current/bin/teamx} ;;
  esac
fi
for directory in "$server_root" "$data_home" "$bin_dir" "$backup_dir"; do
  [[ -n $directory ]] || continue
  [[ $directory == /* && $directory != *$'\n'* && $directory != *$'\r'* && $directory != *'\'* ]] || fail '安装目录必须为不含换行或反斜杠的绝对路径'
done
command_target="$server_root/current/bin/teamx"
command_path="$bin_dir/teamx"
shell_quote() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }

# Check the registered installation before creating temp files or downloading
# an archive. Do not infer an installation from a random teamx on PATH.
installed=false
if [[ -e $server_root/service.json || -L $server_root/service.json ]]; then
  [[ -f $server_root/service.json && ! -L $server_root/service.json && -x $command_target ]] || fail '安装登记或当前程序不完整；请保留安装目录并检查 current 和 service.json'
  run_privileged "$command_target" service inspect --server-root "$server_root" >/dev/null || fail '安装登记验证失败；请核对安装目录和部署账号，尚未下载应用包或更改服务'
  installed=true
  if [[ $upgrade == false && $repair == false ]]; then
    installed_version=$("$command_target" --version) || fail '无法读取当前版本，请检查已有安装'
    printf '检测到已有安装：%s\n安装位置：%s\n' "$installed_version" "$server_root"
    if "$command_target" service upgrade --help 2>&1 | grep -q -- '--version'; then
      printf '\n升级到最新稳定版：\n  '
      [[ $platform != Linux ]] || printf 'sudo '
      shell_quote "$command_target"
      printf ' service upgrade --server-root '
      shell_quote "$server_root"
      printf '\n'
    else
      printf '\n当前 CLI 尚不支持联网升级，请使用此过渡命令：\n  curl -fsSL '
      if [[ -n $release_version ]]; then
        printf 'https://github.com/%s/releases/download/%s/install.sh' "$release_repository" "$release_version"
      else
        printf 'https://github.com/%s/releases/latest/download/install.sh' "$release_repository"
      fi
      printf ' | bash -s -- --upgrade --server-root '
      shell_quote "$server_root"
      printf ' --bin-dir '
      shell_quote "$bin_dir"
      printf '\n'
    fi
    printf '\n查看状态：\n  '
    [[ $platform != Linux ]] || printf 'sudo '
    shell_quote "$command_target"
    printf ' service status --server-root '
    shell_quote "$server_root"
    printf '\n本次未下载应用包或更改服务。\n'
    exit 0
  fi
elif [[ -e $server_root/current || -L $server_root/current || -e $server_root/installation.json ]]; then
  [[ $repair == true ]] || fail '发现未完成的安装登记；请保留目录，使用原版本归档和 --repair 检查修复'
fi
[[ $upgrade == false || $installed == true ]] || fail '未找到可升级的常驻安装；首次安装无需 --upgrade，其他目录请指定 --server-root'
if [[ -e $command_path || -L $command_path ]]; then
  [[ -L $command_path && $(readlink "$command_path") == "$command_target" ]] || fail "$command_path 已存在且不属于此次安装，请使用其他 --bin-dir"
fi

umask 077
work=$(mktemp -d "${TMPDIR:-/tmp}/teamx-install.XXXXXXXX")
trap 'rm -rf -- "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
download() {
  curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL --retry 2 --connect-timeout 15 "$1" -o "$2" || fail "下载失败：$1；尚未停止服务或切换版本，可重新执行原命令"
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

if [[ $platform == Linux && $upgrade == false ]]; then
  if [[ $needs_sudo == true ]]; then sudo -v; fi
  if ! id "$service_user" >/dev/null 2>&1; then
    run_privileged useradd --system --create-home --home-dir "/home/$service_user" --shell /usr/sbin/nologin "$service_user"
  fi
fi
(umask 022; run_privileged mkdir -p "$bin_dir")
[[ -d $bin_dir ]] || fail '无法创建命令目录'
printf '正在%s TeamX 常驻服务…\n' "$(if [[ $upgrade == true ]]; then printf '升级'; else printf '安装并启动'; fi)"
service_action=install
[[ $upgrade == false ]] || service_action=upgrade
install_args=(service "$service_action" --archive "$archive" --sha256 "$expected_sha" --server-root "$server_root")
# Let TeamX resolve defaults and an existing installation's registered home.
if [[ -n $data_home ]]; then install_args+=(--home "$data_home"); fi
if [[ $platform == Linux && $upgrade == false ]]; then install_args+=(--user "$service_user"); fi
if [[ -n $backup_dir ]]; then install_args+=(--backup-dir "$backup_dir"); fi
# The private download directory stays 0700, while the managed executable tree
# must be traversable by Linux's ordinary service account. Go creates home and
# credential files with their own private modes.
if (umask 022; run_privileged "$work/teamx" "${install_args[@]}" </dev/null > "$work/install-result.json") 2> "$work/deployment.log"; then
  cat "$work/deployment.log"
else
  deployment_status=$?
  cat "$work/deployment.log" >&2
  saved_log=$(mktemp "${TMPDIR:-/tmp}/teamx-install-failed.XXXXXXXX")
  cp "$work/deployment.log" "$saved_log"
  printf 'TeamX 部署失败（退出码 %s）。日志已保留：%s\n安装位置：%s\n请按上述状态与恢复提示处理；首次安装失败可核对原因后重试。\n' "$deployment_status" "$saved_log" "$server_root" >&2
  exit "$deployment_status"
fi
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
