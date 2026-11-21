# 工具与通知扩展

TeamX 支持在自定义 Go 入口中显式选择本地工具与通知来源。作者使用 `platform`、`platform/notification`、`extension` 和 `boot`；Agent 后端、Gordis 生命周期和选择构建交付另行实施。

## 本地工具

工具作者只需要公开包 `platform` 与 `extension`：

```go
package localtool

import (
    "github.com/go-aie/teamx/extension"
    "github.com/go-aie/teamx/platform"
)

func Extension() extension.Extension {
    return extension.Extension{
        Name: "local-example",
        Tools: []platform.Tool{{
            Name: "local-echo",
            Description: "Echo arguments and stdin locally",
            Version: "1",
            Executable: "teamx-local-echo",
        }},
    }
}
```

`Extension()` 声明能力，不启动进程、不读取账号或凭据。工具名和扩展名必须为不超过 64 字节的小写字母、数字、`-` 或 `_`；扩展名不可重复，工具名也不能与任何已选内置工具冲突。`Executable` 必须非空，但构造与 Compose 不检查文件是否存在。

入口显式组合后调用 `boot.Run`：

```go
selected, err := extension.Compose(localtool.Extension())
if err != nil {
    return err
}
return boot.Run(selected, os.Args[1:], os.Stdout, os.Stderr)
```

调用者负责进程退出；可以用 `errors.As(err, &exit)` 取得 `*boot.ExitError` 的 `Code`，保留工具退出码。完整外部入口见[独立 module fixture](../testdata/external-tool/cmd/teamx/main.go)。普通 `cmd/teamx` 也使用 Compose 与 boot.Run。

需要保留标准平台时，显式使用 `builtin/platforms.Extensions()`，再追加本地工具；也可从各 Provider 子包选择 `Extension()`。`extension.Compose()` 表示空选择，boot 不会自动补回完整集合。此处是能力选择，当前 Room 的传递依赖仍包含具体 Provider，不能据此声称二进制或资源已经裁剪。

运行 `teamx extensions check` 可检查选择并查看静态工具、通知来源 schema 和可选 input codec。该命令不加载服务配置、不读取凭据、不解析 executable 路径、不打开数据或启动日历／Actor／连接；它也不证明程序已经安装、成员已经绑定或远端权限可用。

启动服务后，工具在现有工具配置目录中可见，成员仍须显式绑定。实际执行继续经过原成员身份与准入、Calls、撤权／取消和进程结清：

```text
teamx tools list
teamx tools run local-echo [args...]
```

这两个命令需要正常的 Agent 执行上下文。仅知道成员 ID，或持有管理 token，都不能冒充 Agent。工具通过 argv、stdin、stdout、stderr 与退出码工作，不需要平台账号或远端 Workspace 绑定；没有可用工作目录时沿用原私有临时目录。可执行程序按原规则在 TeamX 旁或服务器 PATH 查找，也可以显式声明路径。进程环境继续使用原受限集合，不传入模型凭据、管理 token 或 Agent 传输凭据。

请求文件依然由 CLI 进程读取；服务端收到原有受控请求，不读取客户端路径。工具声明不提供凭据缓存、任意宿主访问或进程热替换。

可重复的隔离验收见[fixture 使用说明](../testdata/external-tool/README.md)。它在独立 module 中构建并启动真实服务，用离线 ACP fixture 取得临时执行凭据，验证发现、执行、退出码和关闭。验收不使用真实模型或平台账号，也不代替 DSH 后端验收。fixture 的临时本地模块映射只服务于开发验证；发行应固定包含这些 API 的真实已发布版本。

## 通知来源

在 `Extension.Notifications` 中声明 Provider、来源及本地持久编码：

```go
extension.Extension{
    Name: "local-notification",
    Notifications: []notification.Provider{{
        Provider: "local-feed",
        Sources: []notification.Declaration{{
            ID: notification.SourceID{Provider: "local-feed", Channel: "updates"},
            New: newSource,
            Bind: bindRuntime,
        }},
        Input: inputCapability,
    }},
}
```

`New` 只构造 `Source`，提供版本、严格解码和 `NewRunner`；Compose 校验来源与 Provider／channel 一致、名称无冲突。`Bind` 返回 `Runtime{Source, Admit, CheckLoaded}`，只连接依赖，不做 I/O；静态检查不调用 Bind。实际采集由服务完成加载检查后的 Runner 开始。

`RuntimeHost` 只提供当前 Provider 的账号快照与当前来源的订阅快照，不提供 manager、Store、写入或直接投递入口。`Account` 含账号身份、代际、版本及 CLI 凭据引用，不含 token。这些方法只能在 `Admit`／`CheckLoaded` 内同步调用；二者在宿主原准入锁内执行，不得等待网络或启动任务。Runner 使用 `StepHost.Read` 得到固定查询 scope，离开准入锁后执行查询，再用 `Commit` 检查保存版本和 scope；不要从 Runner 回调 RuntimeHost。

`State` 自己拥有原生进度、pending、克隆、校验与确认转换。每个 Item 固定路由与配置版本，使用稳定 `DeliveryID`；仅在正式 Inbox 收件持久完成后，TeamX 调用 `Acknowledge`。订阅 ID 沿用现有投递合同的 **32 位小写十六进制值**，不能用显示名称代替。取消必须等待查询退出；不要把 context 取消当作底层工作已经完成。

`Input` 是纯本地的 Encode／Decode／Codec 合同，用于现有 JSONL 输入保存、读取和分组；它不开放存储 Provider 或 Repository。注册时可省略，实际持久投递必须提供；已经保存的分区缺 codec 时明确拒绝打开。`Project` 可选，用于同一份保存内容的人类／Agent 投影，读取不能要求远端在线。

完整实现与可重复验收见[外部通知 module](../testdata/external-notification/README.md)。该例在服务停止时写入隔离来源的显式 opt-in 快照，再由正常启动、来源 Runner 与 Inbox owner 完成持久投递；验证重开与缺 codec 的保护。它没有新增通用订阅配置 API；已有飞书 UI／授权／订阅操作继续使用原 owner。新增平台的账号、配置 UI 和授权属于对应产品接入工作。
