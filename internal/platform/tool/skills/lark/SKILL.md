# 飞书平台工具

先运行 `teamx tools list`，使用返回的准确入口。下列命令中的入口由服务端注入，读取与动作都使用该入口的平台账号，不接受任意原生 CLI 命令或登录操作。

```sh
teamx tools run lark --help
teamx tools run lark read --object REF --target REF --scope content
teamx tools run lark read --object REF --target REF --scope context
teamx tools run lark actions resolve --request '{"locator":{"target_ref":"ref_..."},"turn_number":1}'
```

读取只接受已保存的消息引用。上下文与分页有界；结果不完整时使用返回的 cursor。入口与原引用的平台不匹配会拒绝，不能换账号重试来绕过权限。

动作沿用 `teamx actions resolve --help`、`teamx actions execute --help` 的请求结构；用 `teamx tools run lark actions execute --request JSON` 执行。只使用 resolve 返回的可执行能力、准确目标、当前授权与稳定动作 ID。未知结果先查回执，不能换入口或新建 ID 重发。普通消息回复、已验证群消息和本人自聊仍遵守各自的授权边界；工具启用本身不是业务指令。
