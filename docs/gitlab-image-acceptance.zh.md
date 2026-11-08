# GitLab 图片理解验收

本手册对应[图片理解计划 I4](GITLAB-IMAGE-UNDERSTANDING-PLAN.zh.md#8-实施顺序与验收)。每项结果分别记录；下载成功、ACP 声明图片能力、模型读出像素、正常恢复和浏览器预览各有独立证据。当前结果见计划 8.4，不能将通过其中一项理解为一期全部验收通过。

## 配置与运行边界

对实际支持图片的精确模型配置：

```toml
[runtime.model]
protocol = "openai-completions"
base_url = "https://ai.space.opencsg.com/v1"
model_id = "qwen3.8-flash"
api_key_env = "OPENAI_API_KEY"
input_modalities = ["text", "image"]
max_tokens = 8192
```

上例是本次实测路由，不承诺其他部署或网关持续可用。`max_tokens` 是验收使用的输出预算：1536 的首次真实截图测试已能读出错误，但两次以 `max_tokens` 截断；8192 下相同截图的 Room／Inbox 场景正常结束。它不是图片预算，不保证所有任务都够用。配置按实际团队／Room 的生效来源设置；已有 Room 保留冻结配置，新建隔离 Room 验收，不修改旧 `backend.json` 或恢复指纹。

缺省模态保持纯文本，发现图片时告知未读并且不下载；显式图片配置在 ACP 握手能力缺失时停止开启 Session。启用模态不能替代真实网关请求。服务级 `[server.image_materials]` 的默认值和可调项见[配置示例](../.teamx/config.example.toml)，不进入 Room 模型指纹。

只使用固定当前回合范围内已保存的 GitLab 通知文字和完整图片引用。正文实际截断时保留“正文已截断，内容可能不完整”；不重新抓取原评论，不猜测被截断的图片地址，也不保证发现截断之外的图片。Todos 的目标摘要不能当成完整评论。预算、权限、格式和超时导致的未读状态随文字一起提交；不承诺下一轮自动补齐。

材料预览不确认 Room 已读、不消费 Inbox、不创建或完成 matters，不授予 GitLab 操作权限。TeamX 在有界内存中准备图片，页面下载和模型输入复用来源与凭据校验；图片提交后由 DSH 的私有 Session 附件机制持久保存。验收目录也可能保留私有 Session、模型输出和测试图片，应按私有数据管理，不提交到仓库。

## 共同准备

从已有 AgentInbox 中明确选择一条含图通知，传入它的绝对 JSON 文件路径。测试只读该文件；在临时新 Room 中建立真实验证的测试连接与项目订阅，复制选中通知的正文和原来源，并替换为隔离订阅 ID。不会修改原 Room、原通知或远端评论，不启动 Events 轮询器。

```sh
export TEAMX_GITLAB_IMAGE_NOTICE=/absolute/data/rooms/ROOM/inboxes/MEMBERSHIP/00000000000000000015.json
export TEAMX_DSH_ENV_FILE=/absolute/private/.env
export TEAMX_DSH_BINARY_PATH=/absolute/installed/bin/dsh
export MODEL_BASE_URL=https://ai.space.opencsg.com/v1
export MODEL_ID=qwen3.8-flash
```

凭据可直接来自环境，也可由显式 `TEAMX_DSH_ENV_FILE` 通过 Node dotenv 解析；需要 `GITLAB_PAT` 和模型密钥（缺省 `OPENAI_API_KEY`，可用 `TEAMX_DSH_API_KEY_ENV` 指定名称）。生产服务仍不自动读取 `.env`。命令中的输出目录必须尚不存在且为绝对路径，防止覆盖旧失败或恢复证据。不要在日志或命令行填 PAT／密钥。

## 各项入口

1. 独立像素挑战与原 Session 恢复：

   ```sh
   TEAMX_DSH_IMAGE_REAL=1 TEAMX_DSH_IMAGE_RESUME=1 \
   TEAMX_DSH_OUTPUT=/absolute/new-independent-evidence \
   go test -tags=e2e ./internal/agent/dsh -run '^TestRealDSHImage$' -count=1 -v -timeout 7m
   ```

   随机八位数字和红／蓝图形仅在 PNG 像素中，工作区和 prompt 不包含答案。须准确读出数字和布局、无工具调用、正常关闭；恢复检查另要求相同 Session ID 和原图上下文。误读或 429 均如实失败，不重放业务动作。

2. 真实 GitLab 下载与 Room／Inbox 材料准备：

   ```sh
   TEAMX_GITLAB_IMAGE_REAL=1 \
   TEAMX_DSH_OUTPUT=/absolute/new-download-evidence \
   go test -tags e2e ./internal/room -run '^TestRealGitLabImageMaterials$' -count=1 -v -timeout 10m
   ```

   两个通道分别在隔离 Room 中接收同一条原通知。下载不使用浏览器 Cookie；报告同时记录无凭据状态、PAT 请求的状态／耗时、图片字节数和摘要。无凭据对照须返回 401／403／404；公开附件或未知服务器故障不计为私有图片验收。纯文本准备不发起下载。检查前后 Room／Agent／Inbox 状态一致，且没有创建 matters。

3. 真实模型识别：在上条命令增加 `TEAMX_GITLAB_IMAGE_MODEL=1`，并设置 `TEAMX_GITLAB_IMAGE_EXPECT` 为 JSON 字符串数组，例如 `'["只出现在选中截图像素中的完整错误句"]'`。期待值仅供测试核对，绝不放入模型 prompt；应先人工看图，选择正文与来源标题没有泄露的内容。

   实际调用生产 `dshSession.Run`、材料准备及安装的 DSH，不模拟下载响应或模型输出。模型须正常结束、无工具调用，并给出 `channel`（Room／Inbox）、`visible_error`、`error_region`、`marked_region`、`coverage`。程序核对来源与错误文字；空间描述还须人工对照像素，不能仅凭非空字段判为正确。

   增加 `TEAMX_GITLAB_IMAGE_VARIANT=partial` 可对只有一张真实图片的通知构造本地截断／缺图变体：原图片与正文仍在，新增带真实截断提示的测试文字，以及原附件目录下不存在的测试文件名。远端评论不变。模型必须同时承认正文截断和图片未读。

   增加 `TEAMX_GITLAB_IMAGE_RESUME=1` 可在正常回答后关闭、恢复同一 DSH Session，随后仅发送文本追问截图错误和标注颜色。核对 Session ID、错误、标注以及无工具调用，不重新发图；报告中的 `after_resume` 和 `resume_status` 单独判断。这个检查不覆盖整个 TeamX 服务重启或调度恢复。

   这些入口明确设置固定运行回合和通知范围，验证“已保存交付→材料→ACP→模型”；没有验收 Events 自动到达、调度器唤醒或工具业务闭环。临时 Room／凭据存储自动清理，DSH 证据单独保留；测试的组件开启观察点仅调整证据目录，实际调用仍是 `dsh.Open`。

4. 同评论两图、缺图、截断和预算：选择**恰有两张受支持图片**的已有通知，执行：

   ```sh
   TEAMX_GITLAB_IMAGE_COVERAGE=1 \
   TEAMX_DSH_OUTPUT=/absolute/new-coverage-evidence \
   go test -tags e2e ./internal/room -run '^TestRealGitLabImageCoverage$' -count=1 -v -timeout 5m
   ```

   在临时 Room 的原文字后添加本地测试截断段和不存在的图片文件名；分别验证两张成功／一张未读、数量预算只传一张、单图字节预算不传图，且原状态不变。此项调用真实 GitLab，**不调用模型**；不能用它证明模型在超限时不会编造。

5. 浏览器预览：

   ```sh
   TEAMX_GITLAB_IMAGE_BROWSER=1 \
   TEAMX_DSH_OUTPUT=/absolute/new-browser-evidence \
   go test -tags e2e ./internal/room -run '^TestRealGitLabImagePreview$' -count=1 -v -timeout 5m
   ```

   测试将真实页面和附件 handler 暴露在临时 loopback 服务，写出 `browser-ready.json`，其中有页面、Room／Inbox 图片地址。浏览器驱动打开页面和两个图片地址，确认真实图片显示并保留截图后，才在证据目录写入 `browser-done`。三分钟内没有确认即失败；该文件只是驱动确认，仍须检查截图，不能把程序退出成功当成视觉验收。不要在浏览器不可用时创建确认文件。

## 报告判读与待补项

每个目录的 `report.json` 独立判读，保留失败目录，不覆盖、不修改恢复证据。模型报告包括安装版本／摘要、精确路由、ACP 图片能力、提交块的字节／MIME／SHA-256、实际回答、终态及关闭状态；原 `backend.json` 记录 ACP 身份。DSH 附件对象摘要可能因其缩放处理而与 TeamX 提交字节不同，不混用两者。

完整一期通过还需要独立短码、真实 GitLab、Room／Inbox 模型识别、缺失覆盖理解、正常恢复、轮换后重新准备和浏览器预览的证据。组件测试与真实测试分别记录；一位 OCR 误读、`max_tokens`、429、工具阻断和未执行项都不能写成通过。
