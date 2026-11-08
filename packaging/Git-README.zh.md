# Git 运行时与对应源码

full 保留 desktop/dugite-native v2.56.0-2 的完整目标归档布局。
`runtime/git` 含 Git 2.56.0、Git LFS 3.8.1，以及上游为该架构提供的
Git Credential Manager 2.9.1；Linux arm64 上游未提供 GCM。
运行时原有 NOTICE 一并保留，其他许可文本见本目录。

`git-source/dugite` 是固定构建 commit 的完整源码归档，其 `git/`
补入固定 Git commit 的完整源码。两项下载摘要和实际文件清单摘要见
`git-materials.json`，源代码自身含 COPYING、LGPL-2.1 等许可材料。
上游构建过程、系统包、编译选项和捆绑资产版本/摘要可在
`dugite/.github/workflows/ci.yml`、`dugite/script/build-*.sh` 和
`dugite/dependencies.json` 中核对。

源码归档不含 Git 仓库元数据。重建时按工作流准备对应工具链/系统库，
在 dugite 目录将 SOURCE 设为 git 源码的绝对目录，DESTINATION 设为独立输出目录，
TARGET_ARCH 设为 arm64 或 x64，再调用对应 build 脚本。
归档源码没有 tag 元数据时，应显式提供 `GIT_VERSION=2.56.0`；
使用源码内 Makefile/INSTALL 的版本变量规则，不伪造已有仓库的 commit。
TeamX 直接验证并消费上游固定归档，不声称本地重建结果与上游逐字节一致。

`bin/git` 按自身位置设置 exec-path、模板、system config 和 Linux CA 路径。
macOS 使用系统 curl、expat、zlib、iconv 与系统框架；Linux 动态库依赖
须在目标系统验收，不能把开发机库或 Homebrew 路径复制进包。
TLS/clone、平台登录和真实模型测试另见 P3 记录。
