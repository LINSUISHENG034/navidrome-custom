# 实施隔离的软件输出准备闭环

Type: task
Label: wayfinder:task
Mode: AFK
Status: claimed
Assignee: LINSUISHENG034
Parent: ../map.md
Blocked by: 01, 02, 03

## Question

在用户已委托技术判断与开发的范围内，能否交付足够可回退、可重复测试的软件闭环，判断 MA + Bridge 最小协调改动值得进入未来硬件验证？

- 以配置化的 Prepare/Release 和 opaque lease 为公开测试 seam；独立 source/clock/HTTP/physical-output 替身验证 ready 前不消费音乐、取消/超时、迟到完成与旧清理、幂等/切歌复用、未就绪失败。
- 扩展 Bridge 现有物理生命周期和 MA 现有播放 session，不复制队列/协议，不更改原生浏览器播放器；准备功能 opt-in，未配置时保留既有行为。
- 软件层同时考虑逻辑注册、短暂停/停止/结束/空闲策略与默认关闭的后台重连，不把 wake success 或缓存 sink 当 ready。
- 优先独立代码与标准库测试；依赖由锁定 runtime image 提供，宿主不安装库。只允许无生产数据、host network、D-Bus/Pulse或音频设备挂载的隔离软件容器。
- canonical 改动位于 `fork/patches/remote-playback/` 的锁定清单及补丁，安全 runner 位于 `fork/scripts/`；生成的 upstream checkout/output 位于已忽略 `tmp/remote-playback/`。不跟踪整份复制源码，不调用会 down/rm/up 的旧重建脚本。
- 各公开 Interface 一次失败测试→最小实现→下一项；记录真实 red/green 输出，不以私有内部断言或模拟响应证明物理效果。
- 报告具体改动、测试、构建和回退范围；软件通过不关闭真实音箱票，不声称首音/10秒/闲置无提示已经达标，不启动/修改生产实例。

## Implementation contract

- 只新增 Bridge 管理面协调，不修改 Sendspin wire。`POST /api/output/prepare` 输入 `output_id`、调用方预先生成的唯一 `lease_id`、有界 `timeout_seconds`；成功返回 `status=ready`、同一 `lease_id`/`output_id` 和不透明 `output_epoch`。只有已配置输出可被选择，不接受任意 MAC/系统命令。
- `POST /api/output/release` 输入同一 `output_id`/`lease_id` 与受限 reason；同时撤销 pending 或 ready lease。必须支持「取消先于 prepare 到达」的有界 tombstone，防止 HTTP 取消/重排后旧请求复活；旧 lease 的释放不影响 successor。
- 两接口要求显式机器访问 token，使用 `X-Output-Preparation-Token` 和常量时间比较；功能默认关闭，未配置 token 不开放控制。不得把 token 写到 URL、日志或仓库。MA 接入 URL 不含用户信息/query/fragment，明文只允许 loopback，其他地址要求 HTTPS。
- Physical output adapter 是外部测试 port；成功至少检查连接/profile/sink、当前 daemon/backend 绑定与 epoch，不能把现有 wake success 或 reroute boolean 当 Ready。真实声学完整性仍独立验证。
- MA 使用现有可取消 session task，在供流前等待；HTTP 中断/超时即用已知 lease_id 请求撤销，不依赖先收到 ready 回包。要审查所有 resume/seek/skip 入口，不能只补 new play；旧 queue/timeline 不复制。
- configured deadline/暂停宽限期/取消记录上限与保留期来自现有配置层；测试用短的明确参数或可控时钟，不在代码中写死真实等待。
- CLI runner 的公开命令与外部 git/docker 执行 port 为额外基础设施测试 seam；它的测试只用临时目录/外部执行替身，不调用会部署生产的旧脚本。

## Comments

- 2026-10-09：这是用户扩大执行范围后的受限 AFK 开发任务；不是从自动通知推导授权。先完成无硬件软件证据，真实窗口仍保留独立确认。
- 2026-10-09 软件阶段 A：审阅并纳入已有安全 runner 与其标准库 CLI 测试，不另建 runner。新增「全部测试 skipped 不得成功」公开回归，真实 red 为 `Ran 1 test / FAILED (failures=1)`、`AssertionError: 0 != 1`；最小修复后单项 green，完整 runner suite 为 `Ran 34 tests / OK`（0.358 秒）。`validate` 返回锁定的 `bridge`、`ma`，`git diff --check` 通过。这里的 Git/Docker 执行 port 使用替身，尚不能证明真实 canonical replay、Bridge/MA 门控或声学效果；本票仍 claimed。
- 2026-10-09 runner 后续回归：嵌套 unittest 输出的 skipped summary 不得与外层执行数量错配。真实 red 为 `AssertionError: 1 != 0`；只解析最后一次 `Ran` 之后的 summary 后，完整 suite `Ran 35 tests / OK`（0.536 秒），`git diff --check` 通过。阶段 A 的 `48182488` 已成功备份到 fork 同名开发分支。

### 2026-10-09 隔离软件检查点：补丁与干净重放

- **Canonical**：[锁定清单](../../../../patches/remote-playback/manifest.json)、[Bridge 补丁](../../../../patches/remote-playback/bridge-output-preparation.patch)、[MA 补丁](../../../../patches/remote-playback/ma-output-preparation.patch)。只跟踪差分，不跟踪整份 upstream checkout；SHA-256 的唯一登记处是清单。Bridge 差分涉及 17 个源码/测试文件，MA 涉及 5 个，不更改 Navidrome 浏览器、Go backend 或 MA queue/controller。
- **Bridge**：opt-in、独立机器 token、配置输出 allowlist、nonce/epoch、取消先到 tombstone、超时/迟到清理及实际 daemon IPC 绑定路径。真实 red→green 补齐 application identity 与 StatusStore 发布；prepare/release/watcher 只处理本播放器的流。独立审阅又发现整链路 disconnect 会干扰同 sink 其他来源，新增公开 release 回归由 `disconnects=[True]`→`[]`，未知/错误的 Pulse 观察保留链路，且观察前后及 worker 内检查 successor epoch。该观察只覆盖本机 Pulse 当前流，不识别手机连接，也不提供与外部程序之间的跨后端原子互斥。
- **MA**：在已有 cancellation-managed session task 中先 Prepare，再应用权威音量、创建流和读取音乐；未配置时保留既有行为。HTTP/JSON 中断撤销已知 nonce；Stop 幂等释放，pause-as-stop 后 resume 用新 nonce；切歌复用 nonce 但重新核验 ready；现有流 EOF 排空提交音频后释放，setup failure 不冒充自然结束。配置位于既有播放器 Config Entries，token 使用 SECURE_STRING，非 loopback URL 要求 HTTPS，deadline 为有界配置。
- **真实软件结果**：runner 35/35；开发源码与干净重放源码均为 Bridge 27/27、MA 17/17。最后重放分别为 18.594 秒、4.412 秒，均 `OK`；这不是首音时间。MA 一个 setup-failure 用例故意触发并捕获 RuntimeError，日志 traceback 不代表测试失败。
- **重放方式**：在新的隔离 roots 调用现有 runner 的公开 Interface，真实 Git clone/checkout/export/apply/write-tree 与固定 digest Docker test；仅 clone 的下载 transport 替换为本地受管 cache，并恢复 manifest 的 HTTPS origin。两份 cache 是浅历史 clone，但本次固定 revision 的对象完整；不宣称全历史或 GitHub 下载 transport 已被这轮 replay 验证。原开发工作区、索引及 Git 非 objects 元数据的前后 fingerprint/status 均未变化；applied provenance 由 runner 生成，未伪造。
- **运行时**：固定镜像实际包版本匹配清单：Bridge Python 3.13.15、aiosendspin 6.1.1、sendspin 7.5.0；MA Python 3.14.7、aiosendspin 9.1.1。容器断网、只读、去 capabilities、禁止提权，只挂载临时源码，没有生产数据、host network、音频设备、D-Bus 或 Pulse socket。
- **检查与未执行项**：普通文件及应用后源码的 `git diff --check` 通过；canonical 目录的 attributes 仅豁免 `.patch` 的差分 context 空白标记（真实 red 为 artifact check 退出 2，配置后退出 0），不豁免源码。完整 pre-commit/hooks/lint 未执行：固定 MA runtime 有 pre_commit、无 Ruff，未创建或下载 hook 环境。未安装宿主库、构建/发布 patched 镜像或进行生产部署。
- **回退范围**：准备功能默认关闭；本轮只提交 canonical 补丁、清单、runner 支持和计划进度。没有生产变更需要回退；保留已有开发源码与旧临时重放证据，不 reset/删除原工作区。

### 2026-10-09 冷启动增量前的独立回归基线

- 当前 canonical replay 的 Bridge 准备 suite 再次运行为 `Ran 27 tests / OK`（18.698 秒、无 skipped）；安全 runner 独立回归为 `Ran 35 tests / OK`（0.325 秒），清单及补丁 checksum 校验通过。这轮未重新运行 MA suite，不将上一检查点的 17 项改写为本轮结果。
- 固定 Bridge runtime 内，用标准库 `unittest.FunctionTestCase` 直接执行 5 个未修改 upstream 模块中无 fixture、无参数的测试函数：`test_bridge_daemon_signature` 1 项、`test_ipc_integration` 12 项、`test_subprocess_ipc_ratelimit` 4 项、`test_subprocess_stderr_tail` 5 项、`test_status_store` 7 项（均位于 `tests/unit/services/ipc/`）。实际结果为 `Ran 29 tests / OK`（2.688 秒、无 skipped）；没有伪造 pytest 或插件。这是指定函数的直接执行，不是完整 pytest/daemon/client suite。
- 容器使用清单锁定 digest、断网、只读源码、去 capabilities、禁止提权及容器内部临时目录；Python 3.13.15、sendspin 7.5.0、aiosendspin 6.1.1 已核对。被测试 replay 源码、索引和 applied provenance 前后不变；没有改写正在开发的 checkout。
- 固定运行时没有 pytest 及其 async/timeout/xdist 插件，也没有 pre_commit/Ruff；依赖这些工具的完整上游测试及 lint 未执行，未安装宿主依赖或下载 hook 环境。这个基线仍不能证明真实 SDK 的冷输出准备；后续增量优先处理音乐供流前的 backend 建立与释放，不接入真实音箱或生产资源。

**本票仍 claimed，以下不是已完成项：**

1. 运行时物理失联需要可靠传回 MA，撤销/冻结权威 source 与 timeline；仅本地 daemon pause/release 不证明该闭环。
2. 真实 SDK 是否能在音乐供流前建立 owned backend 尚未证明；没有当前绑定证据继续 fail closed，不能把替身 ready 当 cold-ready。
3. 完整 MA controller 的 queue/seek/resume、事件竞态及启动 timeout 仍需更广的整应用集成证据；不因公开 session 测试通过宣称全路径等价或多房间支持。
4. 首段完整、10 秒可听、15 秒实机失败截止、占用与闲置提示音仍属于未来逐次确认窗口的 HITL 验收。软件检查点不关闭硬件票。
