# Room 图片附件：Agent 读取适配

对应 [Room 图片附件方案](../ROOM-IMAGE-ATTACHMENTS-PLAN.zh.md)阶段 2，接续[存储与消息合同](room-image-attachments-storage.zh.md)。本阶段完成既有 Agent 读取适配；客户端交互、真机及真实模型识图分别留在阶段 3、4。

## 实现

- `roomInputNotices` 收集普通人类消息的结构化图片附件，保留平台来源分支。正式读取和 History 使用同一收集器、图片预算及 MCP 图片编码；附件数组下标从 1 开始对应图片位置。
- `projectRoomMessage`、正式输入和 History 输出独立的附件摘要 DTO，包含 ID、格式、名称、字节及尺寸，不含磁盘路径、预览 URL、service epoch 或上传元数据。正式 MCP 图片块标明 Room 用户附件，平台图片继续使用原来源标签。
- `roomMaterialAttachments` 在既有下载／复核边界组合本地 resolver 和平台 resolver。普通 service 初始化即具备本地读取能力，托管装配再组合平台能力；本地证据不进入平台连接／账号检查，平台请求及账号复核保留原实现。
- 正式本地读取绑定服务端选定的具体持久页，核对当前成员、执行代次、页范围及消息可见性。History 使用原 `attachmentRange`、原生保留快照及 `pinHistoryRoom`，另保存精确附件关联证据。存在文件不等于可以读取它；草稿和未选中消息的图片不能借此交付。
- 本地文件沿用阶段 1 的对象临时保护和生命周期锁，在 Store.mu 外读取并核验字节数、SHA-256、格式、像素。读取后再次核对元数据、执行和消息关联；准备结果、交付及确认前沿用原检查边界。缺失／损坏图保留消息正文和不可用原因，执行取消、成员移除、关闭或范围失效则拒绝交付。
- 页内及执行内两种去重均区分 `room_attachment` 和 `platform`。本地身份包含 room_id、room_instance、attachment_id；相同字节但不同 ID 不合并。同图多消息保留各自位置和覆盖说明，复用已供给的图片块／既有正式页证据。History 使用自身缓存及额度，不消费正式流。
- 正式页继续保存实际 MCP 图片字节。短输出、回执丢失及结果仓库重开后重放固定图片，不重新上传或下载；确认仍由原生 Room cursor 和框架结果回执负责。

未新增 Agent 工具、参数、游标或模型启动图片注入。现有 CLI 读取也复用同一个正式页适配。没有修改日常运行中的 Room，没有执行数据迁移。

## 保护边界

文件读取沿用 `service.changes → attachmentLife → Store.mu` 顺序，字节读取不持 Store.mu，不持管理锁调用 Store 或 Room Actor。对象 pin 随真实文件操作结束释放；正式页和 History 原有的消息保护继续防止读图关联被清空。

正式消费确认后，当前执行的固定结果仍受保护，执行结清后才允许清空；History 仅在实际读取／输出期间持有保留 pin。过期无引用对象可在同 Room 的图片准备期间被回收，已发布引用不会因上传超过 24 小时被删除；清空提交成功且保护释放后，过期图片可被回收。History 缓存不能越过已失效的保留范围重放。

## 验证范围

新增测试使用临时隔离 Room、真实 PNG 文件和真实 MCP JSON／图片块编码；服务装配集成覆盖单 Room 与托管路由的真实 loopback HTTP 上传和发送，Agent 读取调用实际 MCP handler 并解析编码结果；执行成员由夹具控制，不启动模型。

覆盖纯图片／文字多图、同图多消息、同内容不同 ID、Room／代次／平台去重隔离、独立 History 预算、跨正式页额度与复用、文本模型、数量／字节／像素限制、缺失／损坏文件、草稿／未选消息拒绝、取消／成员移除／关闭、结果重开与短输出重放，以及清空／回收保护。

已通过：

```sh
go test -race -tags=integration ./internal/room \
  -run '^TestRoomAttachmentMaterials|^TestRoomAttachments|^TestPlatformAttachment|^TestModelAttachment|^TestDSHMaterials|^TestLark.*Image|^TestHistoryMCP|^TestUnifiedRoomFormalImages' \
  -count=1 -timeout=3m
go vet ./...
go test -tags=e2e ./... -run '^$'
git diff --check
```

定向 race 用时 34.077 秒，包含现有 GitLab／飞书平台图片、DSH 材料及 History MCP 回归。平台超时测试在一次并发运行中曾出现通用不可用原因，随后单独及上述完整定向选择均通过，没有放宽断言或修改平台逻辑。

e2e 命令仅编译，不运行浏览器／真实依赖验收。全仓本地集成 race 曾启动，随后按用户关于耗时的指示停止，不记作全仓通过；本阶段交付以以上必要的定向 race、静态检查及编译结果为依据。

未执行前端／浏览器、实体手机或真实 DSH／模型验收；上述组件与集成结果只证明阶段 2 读取适配，不代表完整图片功能已交付。
