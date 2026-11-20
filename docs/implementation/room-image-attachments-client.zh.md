# Room 图片附件：客户端交互

对应 [Room 图片附件方案](../ROOM-IMAGE-ATTACHMENTS-PLAN.zh.md)阶段 3，接续[存储与消息合同](room-image-attachments-storage.zh.md)和 [Agent 读取适配](room-image-attachments-reading.zh.md)。本阶段完成客户端交互，完整真实链路、实体手机和真实模型识图仍属于阶段 4。

## 实现

- 输入框「+」成为独立的「添加图片」菜单，支持键盘打开、Esc、焦点返回及浏览器返回；有文字或图片时仍可追加。工作目录保留自己的目录按钮，关闭弹层后返回目录按钮，桌面与窄屏采用相同入口。
- 选择文件与粘贴调用同一附件草稿流程。系统选择器由用户点击同步打开，支持多选和分次追加；图片来源是当前浏览器设备，不访问服务端目录。粘贴合并并去重 ClipboardEvent 的 items／files，普通文字不被接管，图文粘贴保留光标位置的文字；不解析 HTML 远程图片。
- 本地 File 立即生成 blob 缩略图并自动 multipart 上传；以首次生成的附件 ID 重试。按选择顺序保存，即使上传逆序完成也不改变消息顺序。状态区显示上传、就绪、失败／需重新选择，支持重试和删除；存在问题附件时不能发送文字或图片。
- 从当前 Room 的 `/v1/info` 读取有效数量和大小限制，在菜单展示。图片格式、单图和总字节数在客户端提前提示，真实格式、像素及文件完整性仍由服务端校验。没有图片转换或压缩。
- 发送允许纯图片或文字多图；pending 请求冻结有序附件 ID、文字、引用、提及、operator、Room 和原 service epoch。发送回执不确定时继续使用原请求，刷新也不会重新上传或自动换身份。成功清空对应草稿；放弃重试保留草稿并提示可能已经提交。
- 草稿继续按 installation、room_instance、operator 隔离。持久化只包含附件 ID、展示元数据及上传确认标记，不包含 File、Blob、Base64、AbortController 或预览 URL。文件与 object URL 保留在所属草稿的内存控制器中，删除、发送成功或删除会话时释放。
- 同页切换保留未完成上传，迟到结果只更新原草稿。系统选择器返回时，即使页面刚从后台恢复、网络还未完成核对，也会保留已选文件到原草稿。刷新后已上传对象调用批量 state 恢复；只有内存文件的条目提示重新选择。普通恢复使用当前身份，pending 请求保留原身份。
- 到期草稿重新查询服务端状态，已发布引用继续可用，不把旧创建时间直接当作不可用。过期、缺失、状态查询失败均保留可见条目和具体原因。
- 消息图片由结构化附件创建 DOM，预览 URL 使用当前身份及消息 seq，复用既有放大、缩放、旋转与下载。加载失败保留原消息并可重试。纯图片的会话列表、引用和选中复制摘要使用 `[图片]`／`[图片 ×N]`，不写回消息正文。
- CSP 只为 `img-src` 增加 `blob:`；Markdown URL 限制、脚本等策略保留。

用户说明已补充到[会话使用](../conversations.zh.md#发送图片)，包括客户端来源、手机粘贴差异、选图替代路径、格式／大小限制和恢复行为。

## 验证

前端逻辑使用生产入口及受控 DOM／传输，覆盖上传逆序、重试 ID、URL 释放、刷新恢复、过期／缺失、原 epoch pending、跨 epoch 迟到状态、普通文字与图文粘贴、失败图片阻断发送、系统选择器返回时的网络恢复，以及托管 Room、默认助理私信和单 Room 共用合同。

实际 Chromium 149.0.7827.55 使用**模拟 HTTP API**，在 1440、390、320 像素宽度完成浏览器检查：原生文件输入触发与多次追加、实际 CSP 下 blob 预览、文字多图／纯图片发送、共享放大预览、引用／复制摘要、回执丢失后刷新原请求重试、草稿状态恢复、取消事件保留草稿、失败原 ID 重试、切换会话后的迟到上传、逆序完成、菜单键盘操作、工作目录焦点返回及无横向溢出。

浏览器使用实际 File、FormData、ClipboardEvent、DOM、EventSource 和页面刷新；粘贴及取消由事件夹具驱动。模拟服务没有执行 TeamX 附件持久化或模型调用。浏览器证据见[机器记录](room-image-attachments-client-evidence.json)；截图保存在本地 `.local/room-image-ui/`，可运行脚本重新生成。

781 项前端检查、48 项浏览器断言及以下检查均通过：

```sh
node --test internal/web/tests/web*_test.mjs
node scripts/room-image-ui-probe.mjs

go test -race -tags=integration ./internal/web ./internal/room \
  -run '^TestRoomAttachments|^TestRoomAttachmentMaterials|^TestRoomImageMessagePreview$|^TestRoomsCatalog|^TestManagedWeb|^TestWebRoomAssets|^TestWorkspaceAppHTTPBinding$|^TestAssets$|^TestWebAssetReferences$' \
  -count=1 -timeout=3m

go vet ./...
go test -tags=e2e ./... -run '^$'
git diff --check
```

定向 race 中 `internal/room` 用时 25.127 秒，包含附件存储与 Agent 读取回归、会话摘要、真实托管 HTTP 和工作目录／旧文字前端组合检查。e2e 命令仅编译，不代表已运行真实依赖验收；没有全跑全仓 integration／race。

没有修改运行中的 Room 或迁移数据。没有执行真实 TeamX 后端的浏览器上传链路、服务重启后的浏览器预览、真实 iOS Safari／Android Chrome 的系统选图及粘贴，也没有执行真实 DSH／模型识图；窄屏 Chromium 不替代这些验收。阶段 4 保留全部要求，不能将本记录描述为完整功能已验收。
