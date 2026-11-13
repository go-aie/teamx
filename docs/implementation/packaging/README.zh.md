# 打包与发行实施记录

这些记录保留各次实施的范围、归档摘要、验证结果和未完成项，阶段编号用于追溯。文中的“当前”“未实施”和配置要求属于记录时的状态。

日常操作请使用[打包指南](../../../packaging/README.zh.md)、[正式发行操作指南](../../../packaging/releasing.zh.md)和[安装与运行](../../installation.zh.md)。设计与阶段定义见[原实施计划](../../PACKAGING-AND-DISTRIBUTION-PLAN.zh.md)。

| 记录 | 内容 |
| --- | --- |
| [P0](P0.zh.md) | 固定依赖、平台矩阵、资产清单和许可证核查 |
| [P1](P1.zh.md) | 公共启动、初始化、诊断和 core 归档检查 |
| [P2](P2.zh.md) | 多平台组装与 full 依赖缺项 |
| [P3：macOS](P3.zh.md) | macOS arm64 本地真实 DSH 与浏览器验收 |
| [P3：Linux](P3-linux.zh.md) | Linux arm64 本地验收与容器运行条件 |
| [P4.1](P4.1.zh.md) | GitHub 开发快照及下载复验实现 |
| [P4.2](P4.2.zh.md) | 版本候选、可选签名／公证和归档证据绑定 |
| [P4.3](P4.3.zh.md) | 完整矩阵验收、发行汇总和发布恢复实现 |
| [standard 调整](STANDARD.zh.md) | 三档包型、按依赖验收和 core/standard 正式发行范围 |

记录引用的 `.packaging/`、`dist/`、日志和截图是本地证据位置，不随源码文档提交或进入用户包。记录中的单次通过结果只适用于对应归档及范围，不能自动推广到其他平台或后续版本。
