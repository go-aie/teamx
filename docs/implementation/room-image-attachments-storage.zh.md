# Room 图片附件：存储与消息合同

对应 [Room 图片附件方案](../ROOM-IMAGE-ATTACHMENTS-PLAN.zh.md)阶段 1。本记录覆盖后端附件存储、HTTP 与消息合同；Agent 本地图片读取见[阶段 2 读取适配](room-image-attachments-reading.zh.md)，输入框和完整真实验收仍留在阶段 3、4。

## 已实现

- `POST /v1/attachments/upload` 接收一个 `file` 字段，查询参数携带 ServiceInfo、caller_id、attachment_id。托管入口按查询参数分派上传，绕过普通 128 KiB JSON 解析；单 Room 使用相同 handler。
- `POST /v1/attachments/state` 接收独立的身份信封及 `ids`，返回 available／expired／missing／unavailable；`GET /v1/attachments/content` 区分上传者草稿预览与带 `seq` 的正式引用预览。
- `/v1/info` 的 `attachments` 提供有效格式、数量、单图／总字节、像素、Room 容量及有效期摘要。有效输入限制取首版上限与当前 image_materials 配置的较小值。
- 新上传支持 PNG、JPEG、WebP；真实内容检测及 DecodeConfig 先限制像素，再执行完整解码。接收有字节硬上限，解码全局最多并发两次，不转换或压缩图片。
- Room 私有目录保存不可变 `meta.json` 和 `data`。元数据记录 Room、operator、描述符、创建时间和 SHA-256；发布前同步文件／临时目录，rename 后同步目标父目录。路径只由受限 ID 构造，拒绝符号链接和非普通文件。
- 同 ID、同上传者、相同内容和规范化名称返回原对象；冲突拒绝覆盖。同 ID 并行上传返回 busy。发布同步失败的对象在原 ID 重试确认前不可用，不冻结整个 Room 的消息存储。
- `/v1/send` 只接受 `attachments: [{"id":"img_..."}]`，普通 human 消息可发送纯图片或文字多图。客户端不能提供可信 MIME／尺寸／路径等字段。发送幂等比较包括有序附件 ID，已提交请求在读取文件之前返回原回执。
- 内嵌 Messages 和 InputLog 都保存附件描述符，并覆盖严格解码、克隆、缓存、返回切片及字节预算。缺少 attachments 的旧文字消息无需迁移；旧图片损坏不阻断无关文字写入。

## 引用、回收与关闭

正式引用索引从已验证的物化 Room 消息重建，InputLog 下同样有效。它是可重建缓存，原生 Room root 继续作为唯一发布权威。

Store 原生提交使用独立串行门；文件收发、完整性读取和解码在 Store.mu 之外。首次消息提交与读取持有临时保护，操作实际结束后才释放。提交前在同一串行边界复核元数据、文件身份、实际大小和 SHA-256；读取交付前再次检查消息关联。

回收发生于启动恢复、上传接纳和清空成功后，每批最多删除 64 个过期、无正式引用且无临时保护的对象。每次判断都读取已发布 root，不使用清空失败的候选。消息存储不确定或附件恢复完整性不确定时跳过删除。上传接纳按磁盘现存对象／临时文件重新计算容量，并加入正在接收的预留；重启无法通过丢失进程内计数绕过配额，同 ID 残留接收目录也不会重复计费。

锁顺序为 attachmentLife → roomCommit（仅原生提交）→ Store.mu → writer/path。HTTP 文件操作处于 service.changes 内；关闭等待已接纳操作，随后原有 Room 删除流程可删除整个目录。管理锁不跨越文件 I/O 或 Room Actor 等待。

## 验证

所有新增文件及 HTTP 写入使用临时、隔离 Room，没有修改日常运行中的 Room，也没有迁移实际数据。

```sh
go test -race -tags=integration ./internal/room \
  -run '^TestRoomAttachments|^TestCommitFailureBoundaries$|^TestSerializedCommits$|^TestStoreClose|^TestRoomMembersPostsAndUnread$|^TestRoomInputClear' \
  -count=1 -timeout=3m
go vet ./internal/room
git diff --check
```

检查范围包括：

- 真实 loopback HTTP、单 Room／托管路由及内嵌／InputLog 四种组合；超过 128 KiB 的图片上传、纯图片发送、正式消息取图、后端重启、原 ID 重试和旧 epoch 拒绝。
- 内容与名称冲突、截断／非法格式、大小／像素／消息数量／总字节／Room 配额、重启容量重建、同 ID 并发、符号链接、损坏文件、跨 Room／上传者拒绝及切片隔离。
- 草稿状态与过期、正式引用保留、清空后回收、读取及首次提交临时保护、文件发布故障、消息提交／清空失败和提交不确定、关闭等待接纳中的上传。
- 下调当前限制后，存量预览与已提交请求重试保持有效；新引用继续执行新限制。相关旧文字、消息存储及生命周期回归包含在上述选择中。

全仓普通集成与全仓 race 曾启动，随后按用户要求停止，不记作全仓通过。最终交付仅声明上述定向检查结果。未执行浏览器、真机或真实 DSH／模型验收，未接入客户端图片入口。
