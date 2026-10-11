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

- 只新增 Bridge 管理面协调，不修改 Sendspin wire。`POST /api/output/prepare` 输入 `output_id`、调用方预先生成的唯一 `lease_id`、有界 `timeout_seconds`，升级目标另带可选 `pcm_format={sample_rate,bit_depth,channels}`，表示协商的有符号整数 PCM；成功返回 `status=ready`、同一 `lease_id`/`output_id`、不透明 `output_epoch`，显式格式请求还须回显相同格式。只有已配置输出可被选择，不接受任意 MAC/系统命令。同一 nonce 不可改变格式；冷启动的已接受 MA 路径必须显式给出实际协商格式，无格式旧调用只保留已绑定输出的行为，不猜测格式后声称 cold-ready。
- `POST /api/output/release` 输入同一 `output_id`/`lease_id` 与受限 reason；同时撤销 pending 或 ready lease。必须支持「取消先于 prepare 到达」的有界 tombstone，防止 HTTP 取消/重排后旧请求复活；旧 lease 的释放不影响 successor。
- `POST /api/output/verify` 只复验既有 ready 使用权，输入 `output_id`、`lease_id`、`output_epoch` 与配置上限内的 `timeout_seconds`，不创建或重启准备。成功返回相同 ready contract，显式 PCM 返回存储的不可变格式；额外请求 PCM 不改变存储 contract，MA 校验响应格式。未知 output 为 404，旧 nonce/epoch 为 409，pending/not-ready 或 fresh 阴性/截止为 503；fresh 失败只撤销匹配的当前使用权；外层 WSGI 等待截止可返回 504 并取消观察，取消 verify 只取消观察，不释放有效使用权。显式 PCM 通过既有 worker/native backend 的 fresh request-ID ACK，不 Connect、wake、Prepare、重新分配或修复路由；无 PCM 旧路径保留既有 Pulse/BT 检查，不声称 fresh SDK 证据。
- 三接口要求显式机器访问 token，使用 `X-Output-Preparation-Token` 和常量时间比较；功能默认关闭，未配置 token 不开放控制。不得把 token 写到 URL、日志或仓库。MA 接入 URL 不含用户信息/query/fragment，明文只允许 loopback，其他地址要求 HTTPS。
- Physical output adapter 是外部测试 port；成功至少检查连接/profile/sink、当前 daemon/backend 绑定与 epoch，不能把现有 wake success 或 reroute boolean 当 Ready。升级目标的显式格式请求，即便已有 Pulse 绑定也须取得 SDK 对该格式的确认；准备与内容使用同一 worker/backend，不能用另一个 reservation 替代。SDK 保留正常 decoder/clock 路径，未 drain 的不同格式转换失败关闭，不重写调度。真实声学完整性仍独立验证。
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

### 2026-10-09 冷启动 tracer 与 SDK 能力缺口

- 新增真实 daemon stdin 的冷启动回归，保留固定 sendspin 7.5.0/aiosendspin 6.1.1 的 daemon、client、handler 与 worker，仅替换外部 PortAudio/Pulse 端口，不预置 owned sink-input。实际 red 为 `Ran 1 test / FAILED (failures=1)`（2.108 秒）：owned streams 为 0、allocations/starts/closes 均为空、绑定回报 `ready=false`。这不是物理实验，也不是失败已修复的声明。
- 固定 SDK 的音乐 worker 在首个 chunk 后才调用 `AudioPlayer.set_format`；公开 handler/worker 尚无 pre-content prepare-format 能力。独立 `AudioPlayer.set_format` 可创建另一个 daemon-owned 预检输出，但不能证明实际音乐 worker 已准备或无需重开 backend；不将这类 reservation 改名成 gate ready，也不向私有队列注入伪造音频。
- 后续增量选择最小的 SDK 公开 Prepare 生命周期，使准备和首个音乐 chunk 使用同一 worker/backend；SDK 改动将有独立版本锁定与 canonical 差分，隔离 runner 只读加载经 provenance 校验的依赖源码。若所需改动演变为重写 worker/协议，应停止该方向并重估，不用预检替身绕过首段门槛。此项仍在实施，没有 green/真实冷就绪结论。

### 2026-10-10 依赖源码隔离 runner 增量

- 扩展既有 runner 的可选 `runtime_sources` 配置；依赖递归去重后只读挂载于 `/runtime-sources/<project>`，复用同一 provenance/pin/canonical tree/Git 配置与整树安全检查，不自动 fetch/apply/install。依赖只需源码目录，不要求测试目录；未配置时保留原测试行为。
- 依赖额外拒绝普通和被忽略的未跟踪文件、缺失源码目录以及含 `:` 的 `source_path`，防止导入覆盖或拆出额外 `PYTHONPATH`；缺失、错误 revision/origin/provenance、非 canonical 索引/工作区、循环引用、逃逸与特殊文件均在 Docker 前失败。
- `source_path="src:/unowned"` 的公开 CLI 回归实际 red 为 `AssertionError: 0 != 1`、`Ran 1 test / FAILED (failures=1)`（0.018 秒），只加依赖专用的 `:` 检查后 unchanged test green；普通和被忽略未跟踪文件的拒绝也有真实 red→green。协调者独立 suite 为 `Ran 51 tests / OK`（0.660 秒），独立审阅另跑 51 项（1.096 秒），增加 diamond 去重和 transitive cycle 的公开替身探针，未发现已确认缺陷；固定清单及旧补丁 checksum 校验仍通过。新增验证只使用临时目录及 Git/Docker 外部执行替身，不等于真实依赖 clone/apply/Docker replay。canonical 清单暂未登记 SDK 项目，实际 SDK 生命周期增量仍待完成。
- 已拒绝的独立 reservation/handoff 试验及其 35 项 green 留在忽略工作区，不纳入 canonical，也不关闭冷启动缺口。该试验发现的原生身份问题另计：固定 libpulse 17.0 下，`PULSE_PROP_application.name` 会被 ALSA 的显式名称覆盖；`PULSE_PROP_OVERRIDE_application.name` 经隔离原生 Pulse 观察可保留配置身份。严格 PID/身份比较不放宽，后续同一 worker 路径仍须重新验证。

### 2026-10-10 source-only 依赖检查点

- 既有 runner 接受 `test_command: null` 的源码依赖，但要求它从至少一个有测试命令的项目经 `runtime_sources` 可达；直接 `test` 源码项目在 Docker 前拒绝，提示测试依赖它的项目。`status/fetch/apply/export` 保留原语义，依赖仍接受相同的 provenance/pin/canonical tree、安全路径及未跟踪文件检查。
- 独立审阅实际运行完整 runner suite：`Ran 57 tests / OK`（0.839 秒，无 skipped），未发现已确认缺陷；本轮增加 6 个公开 CLI 回归。`validate` 与 `git diff --check` 通过。这里 Git/Docker 仍是外部执行替身，不能证明真实 SDK 导入或冷启动输出。
- 已提交 `ea7bd14d` 并成功推送到 fork 的同名 `dev/remote-playback-output-gate` 分支。SDK 开发 checkout 没有 runner provenance，不补造标记；后续导出及重放使用 runner 新建的干净 checkout，不 reset/覆盖原开发源码。SDK canonical 差分及同一 worker 冷启动证据仍待接入。

### 2026-10-10 锁定源码依赖的真实 runner 基线

- 清单登记 `sendspin` 7.5.0 的精确 revision（以清单为唯一锁定来源），Bridge 经 `runtime_sources` 依赖它；SDK 使用 `test_command: null`，本检查点尚未加入 SDK 生命周期补丁，不把依赖登记当作 cold-ready。
- 在新的 `tmp/remote-playback/provenance-sdk-baseline-20261010/` 隔离 root 使用 runner 的公开 fetch/apply/test；provenance 由真实 runner fetch 生成，不采用缺失标记的原开发 SDK checkout。仅 Git clone transport 替换为受管本地 cache，保留 no-hardlinks 并恢复 HTTPS origin；其他 Git 与容器操作真实执行。
- 旧 canonical 补丁加未修改锁定 SDK 的实际结果：Bridge `Ran 27 tests / OK`（19.663 秒），MA `Ran 17 tests / OK`（5.501 秒）。MA 预期的模拟 transport unavailable traceback 属于通过的故障测试。本轮没有真实音箱或声学指标证据。
- 容器确认 `sendspin.__file__=/runtime-sources/sendspin/sendspin/__init__.py`、daemon 同样来自该只读挂载，而非镜像安装副本；实际包 metadata 为 sendspin 7.5.0/aiosendspin 6.1.1，MA 为 aiosendspin 9.1.1。容器固定 digest、断网、只读、去 capabilities、禁止提权，无宿主音频/D-Bus/Pulse/生产数据挂载。协调者再跑 runner suite 为 `Ran 57 tests / OK`（0.850 秒）。

### 2026-10-10 同一 worker 增量实施中（未导出验收）

- Bridge 开发源码补齐显式 PCM ready 回显、manager→client→typed IPC 的格式转发、standby 等待 SDK 释放，以及返回 JSON 不能修改缓存 lease 格式。四个公开回归实际 red 分别为缺少格式回显、`output_unavailable`、standby 后 backend 未重建、回显被改为 44100 而期望 48000；各自最小修复后 green。
- 开发完整 suite 曾为 Bridge `Ran 55 tests / OK`（42.865 秒）、MA `Ran 26 tests / OK`（2.822 秒），但随后共享 Bridge 源码出现另一旧任务的 request-ID/ACK 并发改动，因此 **55 项通过不验证当前源码**。协调者明确 Bridge/MA 与 SDK 的唯一写入者；合并当前工作、冻结及重新完整测试前禁止 canonical 导出。未 reset/覆盖既有工作。
- SDK 新保留回归 `test_prepared_pcm_cannot_be_replaced_by_mismatched_music_without_prepare` 的真实 red：准备 16-bit PCM 后送入合法 24-bit 音乐，再以公开 Prepare 作 FIFO barrier，`Ran 1 test / FAILED (errors=1)`（1.495 秒），报 `Cannot reconfigure an undrained output`。在原 drain 前拒绝不匹配内容的最小保护后，SDK class `Ran 14 tests / OK`（1.519 秒）；纠正开发者此前手工误报的 15 项计数。未重写 decoder、clock 或 wire。
- 保留的 native-close/retry 回归目前真实 red：`Ran 1 test / FAILED (failures=1)`（1.278 秒）、`OSError not raised`。关闭错误必须回传，保留可重试的 native handle，并阻止重新认证不确定 backend；worker 异常后 pending 控制完成、入队失败不抢占旧 epoch、有效 successor 的失败清理一致性仍待各自验证。旧只读探针只是退出 0 的坏状态观察，不登记为失败 unittest，也不能由旧 green 掩盖。
- 证据边界：no-content daemon tracer 使用真实 daemon client 构建路径；音乐 tracer 在真实 SDK handler/worker 上接外部 source/clock/PortAudio/Pulse ports，验证同一 backend 和 PCM/FLAC 首段样本，不注入私有音频队列。它不是完整 `daemon.run` + 真实 WebSocket 音乐链路，不是实机声学证明。缺失完整 controller、失联反馈和孤立 READY lease 的故障退场证据仍保留。所有本节结果来自隔离开发源码，不是最终 provenance-validated canonical replay。

### 2026-10-10 基本 SDK 生命周期与 fresh ACK 检查点（取消补齐前）

- 六项 SDK 公开回归均已完成真实 red→green：阻止内容绕过 Prepare 改格式；native close 错误回传并同 backend 重试；worker 失败完成 pending 控制并允许 bounded shutdown；无效 float 入队前拒绝而不抢占 pending owner；队列满拒绝保留最后已接受 owner；drain 中有效 successor 失败仍持有清理权。另验证匹配格式 drain 与 pre-content disconnect。当前 SDK class `Ran 21 tests / OK`（1.573 秒），不是最终取消覆盖。
- Bridge fresh-ACK 两项真实 red 分别为 `TimeoutError not raised`（同 epoch 复验错误接受旧缓存）与 `PreparationError not raised`（ready lease 绕过实际 SDK 确认）。新增有界 request ID 的 typed command 编解码、daemon ACK 和 parent IPC allowlist，显式 lease 复验走同一真实 Prepare 路径；对应单项 green 0.400 秒，manager class `Ran 25 tests / OK`（0.692 秒）。一次中间运行因遗漏 IPC allowlist 挂起，只停止已核验的隔离测试 sandbox，修复后重跑；未操作生产。
- 取消补齐前的完整开发快照：Bridge `Ran 66 tests / OK`（45.632 秒，无 skipped，含 SDK 21 项）、MA `Ran 26 tests / OK`（2.789 秒，无 skipped），三个源码目录 `git diff --check` 通过。**这仅是 pre-cancellation 基线，不授权 canonical 导出。** 两项新取消回归会修改 SDK/测试，之后必须重新完整验证当前快照。
- 架构评估收敛为现有 worker 的控制生命周期 Module：原 Prepare/Release Interface 不变，入队、desired epoch 撤销和晚到清理集中，不增加 backend/音频队列或重写 decoder/clock。计划仅在有效成功入队的 Prepare 后锁定受控模式，Release/取消不退回 legacy；从未 Prepare 的行为保留。标记范围为 worker 生命周期，断线后重建 worker 的重准备/供流安全仍无整链路结论。若局部改动扩成 worker/协议重写则停止重估。
- 尚待真实 red→green：满队列下取消正在 native Prepare 的请求必须退休所有 allocation、阻止排队旧内容重开 backend；取消已入队 successor 必须清理旧资源且不影响更晚 successor。协调者已把既有 `SdkPreparationTests` class 单独交由 SDK 实现者完成，Bridge/MA 其余源码保持唯一写入者；完成后整体重跑、独立审阅、冻结再导出。SDK canonical patch 尚未登记，准备好的 runner-owned export checkout 保持干净。

### 2026-10-10 同一 worker Prepare/Release canonical 检查点

- **Canonical 已接入**：[SDK 补丁](../../../../patches/remote-playback/sendspin-output-preparation.patch)、更新后的 Bridge/MA 补丁及既有锁定清单。版本、镜像 digest 和 patch SHA-256 仍以清单为唯一来源。SDK 差分仅涉及 `audio.py`、`audio_connector.py`；Bridge 17 文件（16 个 Python 文件和 schema），MA 5 文件。没有修改 Navidrome Go/UI、复制队列或跟踪整份 upstream 源码。
- **同一输出生命周期**：实际协商的有符号整数 PCM 经 MA→Bridge→typed IPC→SDK Prepare 传递；音乐与准备使用同一 worker/backend。显式 ready/lease 复验必须匹配本次有界 request ID 的新 SDK ACK，不接受旧缓存。配置默认关闭，未配置保留原行为；来源/时钟/native/Pulse 仍是外部测试 ports，不声称真实音箱就绪。
- **SDK 完成的公开回归**：格式不匹配不重开 backend；native Release 失败回传并保留 handle/epoch 重试；worker 失败完成 pending 控制；无效/队列满请求不抢占 owner；有效 drain 失败持有清理权；满队列或已入队的取消退休全部输出、不由旧内容重开且不影响 successor。两项取消实际 red 为 `Ran 2 tests / FAILED (failures=2)`（1.306 秒），unchanged green 1.298 秒，另逐项 green 1.328/1.316 秒；保持同一 Prepare/Release Interface，不新增 backend、队列或重写 decoder/clock。
- **独立审阅发现并修复额外退场缺口**：初次 SDK 23/23 独立通过（1.618 秒），但新故障端口观察到 prepared 格式替换吞掉旧 native close 错误，两个 handle 在 release/shutdown 后为 `[False, True]`。保留 `test_prepared_replacement_requires_native_retirement_and_retains_latest_cleanup_owner`，实际 red 为 `OSError not raised`、1 项失败（1.440 秒）；统一严格 native retirement，在改变格式/新 allocation 前确认旧 close，失败保留旧 format/handle 与最新清理权，unchanged green 1.257 秒。最终 SDK class 24/24（1.597 秒），独立复核 24/24（1.723 秒）及专门的 fail-closed/retry/stale-release/successor 场景通过；声明的 per-worker Prepare/Release 合同内未再发现已确认 false-ACK。
- **真实干净重放**：在新的 `tmp/remote-playback/provenance-sdk-replay-20261010/` root 调用 runner 公开 fetch/apply/test，provenance 仅由真实 fetch 生成，SDK 依赖通过同一 provenance/pin/applied-tree 校验只读挂载。实际 Bridge `Ran 69 tests / OK`（44.643 秒，含 SDK 24 项）、MA `Ran 26 tests / OK`（2.744 秒），无 skipped；结果位于该 root 的 `bridge-test-result.json`、`ma-test-result.json`。开发快照此前为 Bridge 69/69（48.102 秒）、MA 26/26（2.807 秒），不把测试耗时当首音指标。仅 clone 下载 transport 替换为受管本地 cache，保留 no-hardlinks 并恢复 HTTPS origin，不宣称本轮验证了 GitHub 下载 transport。
- **检查**：三个重放源码目录都用 `git diff --check <pinned-base>` 检查完整已暂存差分，而非空的 unstaged diff；容器 `compile()` 检查全部 23 个改动 Python 文件通过。SDK 的 `__init__`、audio、audio_connector、daemon 均从 `/runtime-sources/sendspin/` 导入，实际 sendspin 7.5.0/aiosendspin 6.1.1、MA aiosendspin 9.1.1 与清单一致。协调者最终 runner suite 57/57（0.796 秒）、canonical checksum/`validate`、仓库 `git diff --check` 通过。容器断网、固定 digest、只读、去 capabilities、禁止提权，无生产数据/host network/音频/D-Bus/Pulse 挂载，无 upstream host import。
- **完整性偏差如实记录**：MA 的既有 helper 与 client 测试最初未暂存，首轮 tracked diff 缺失两文件，未将不完整补丁写回 canonical。一次诊断 `git status` 未禁 optional locks，刷新了原 MA Git 索引缓存；不能声称全会话 MA 元数据未变。经协调者明确授权，owner 仅暂存这两个既有已审查文件，源码/provenance 未变；重新建立基线后，三份原工作区的源码、provenance 与非 objects Git 元数据在最终捕获/重放/写回前后全部匹配。未恢复或伪造元数据，原 SDK 缺失的 provenance 仍缺失，不采用旧开发 checkout。
- **未执行与结论边界**：upstream pre-commit/Ruff/mypy 等完整 lint 未运行，固定镜像缺工具/依赖；未安装宿主库、下载 hooks、构建/发布 patched 镜像或部署生产。controlled-mode 标记只在 worker 生命周期内保持，重建 worker 的 reconnect 需重新准备，未证明整链路安全；legacy worker-finally shutdown 在 native close 故障下不是已确认的资源退休保证。这些限制以及 MA 孤立 READY lease、失联反馈和完整 controller/WebSocket 集成仍保留，不关闭本票或硬件票。

**本票仍 claimed，以下不是已完成项：**

1. 运行时物理失联需要可靠传回 MA，撤销/冻结权威 source 与 timeline；仅本地 daemon pause/release 不证明该闭环。
2. 真实 SDK 的同一 worker、pre-content Prepare 与显式 Release 退休已在隔离外部 ports 上证明；同一 handler 的受控约束跨 worker 重建回归已补齐，见下方检查点。真实 native/Pulse/音箱绑定与声学效果仍未验收；native close 故障下的 legacy shutdown 已复现返回成功但 backend 未关闭，join-timeout 后的退休所有权及完整 daemon/WebSocket reconnect 安全仍待修补和证据，不能把替身 ready 当实机 cold-ready。
3. 完整 MA controller 的 queue/seek/resume、事件竞态及启动 timeout 仍需更广的整应用集成证据；不因公开 session 测试通过宣称全路径等价或多房间支持。
4. 首段完整、10 秒可听、15 秒实机失败截止、占用与闲置提示音仍属于未来逐次确认窗口的 HITL 验收。软件检查点不关闭硬件票。

### 2026-10-10 同一 handler 的受控约束跨 worker 重建检查点

- **真实缺陷与 red→green**：有效 Prepare 后断线，旧 worker 被丢弃，新 worker 默认回到 legacy，未重新 Prepare 的内容实际分配了第二个 backend。新增公开回归的真实 red 为 `Ran 1 test in 1.693s / FAILED (failures=1)`、`AssertionError: 2 != 1`；最小修复后该项与 legacy 播放保护 `Ran 2 tests in 1.385s / OK`。
- **有界实现**：handler 在丢弃或替换 worker 前继承其实际 admitted 的受控事实，通过加锁的只读 handoff property 读取；新 worker 继承约束，不继承旧 epoch 或 prepared backend。标记在有效 Prepare 成功入队后设置，不以调用意图或最终成功判断；invalid/full-queue 拒绝不抢 owner。未使用 Prepare 的 handler 保留 legacy 行为，不改变 decoder、clock、音频队列或 Sendspin wire。
- **公开保护回归**：断线后未经新 Prepare 不重新分配，fresh Prepare 后真实首批 samples 恢复，旧 epoch Release 不关闭 successor；通过外部 clock 故障使真实 worker 崩溃，分别验证 stream-start 和 reattach 恢复；virgin float Prepare 拒绝后重连仍能 legacy 播放并在 stream-end 关闭。这两项额外保护在修复上直接 green，不伪称额外 red；virgin full-queue legacy 暂无直接新用例，入队失败无状态变更仍由实现和既有 accepted-owner 回归约束。
- **冻结验收**：最终重新执行真实 `SdkPreparationTests` 为 `Ran 27 tests in 1.667s / OK`；固定镜像无 pytest/pytest-asyncio，未安装。用标准库 fixture adapter 执行原 `test_audio.py` 两个与 `test_audio_connector.py` 五个原函数和原断言，7/7 PASS、exit 0、无 stderr；不是 pytest 或完整 upstream suite。SDK 原测试仅适配 FakeWorker 的新增可选参数和只读 property，既有行为断言未改，生产没有 mock 兼容分支。SDK suite 的 clock TypeError、native OSError、queue-full/drop warnings 属于报告过的预期故障注入，不把有 traceback 等同于测试失败。
- **独立审阅与 canonical 已验收**：独立 SDK suite `Ran 27 tests in 1.716s / OK`、无 skipped，三个冻结指纹前后匹配；声明的受控重建范围内未发现阻塞。新 `tmp/remote-playback/provenance-sdk-reconnect-export-20261010/` root 经公开 fetch/apply 恢复旧 canonical，仅复制冻结的 SDK connector、原测试 fixture 和 Bridge HTTP 测试后公开 export；`audio.py` 未改，不采用或伪造原 SDK 缺失的 provenance。全新 `provenance-sdk-reconnect-replay-20261010/` root 真实 fetch/apply/test 得到 Bridge `Ran 72 tests in 46.371s / OK`（含 SDK 27 项）、MA `Ran 26 tests in 3.049s / OK`，均无 skipped；结果保留在该 root 的两个 `*-test-result.json`。仅 clone transport 使用受管本地 Git cache、保留 no-hardlinks 并恢复 HTTPS origin，不宣称验证 GitHub 下载；测试耗时不是首音指标。旧 69/26 证据未覆盖。
- **完整性与检查**：SDK 完整差分现为 3 文件（含既有测试 fixture），Bridge 17 文件、MA 5 文件；三个 replay checkout 的 `git diff --check <pinned-base>` 通过。固定 digest、断网、只读、去 capabilities、禁止提权的容器中实际 24 个改动 Python 文件 `compile()` 全通过；依赖版本匹配清单，SDK audio/connector/daemon 实际均从 `/runtime-sources/sendspin/` 导入，冻结指纹匹配。MA patch 字节和完整 project 配置未变，三份新候选与 canonical 旧基线在写回前校验；审阅和干净重放通过后只写回 SDK/Bridge patch 及清单；最终协调者 `validate`、runner suite 57/57（0.781 秒）、仓库 `git diff --check` 通过。完整 inventory 与两个 `*-runtime-probe.json` 留在新 replay root，版本与 patch SHA-256 仍以 canonical 清单为唯一登记处。
- **已复现而未修的 shutdown 缺陷**：真实 handler/worker 的 native close failure probe 返回 `shutdown_returned_success=true`、`native_stream_closed=false`、`allocations=1`；legacy finally teardown 记录 OSError 后仍丢失未退休 backend，不能宣称资源已释放。本阶段仅交付同一 handler 的受控约束持续，不把它扩大为 native 故障退休、有界 join 后所有权、新 daemon 或完整 WebSocket reconnect 安全。
- **范围边界**：upstream pre-commit/Ruff/mypy 等完整 lint 未执行；无宿主安装、生产操作、patched 镜像构建/发布或真实音箱实验。运行时输出失联回传 MA 并冻结权威 source/timeline、完整 controller 集成与硬件/声学验收仍未完成，本票仍 claimed。

### 2026-10-10 shutdown 退休增量：实施前检查点

- **基线与 provenance**：协调者委托独立 runner 回归得到 `Ran 57 tests in 0.821s / OK`，canonical `validate` 通过；默认 `src/` 的公开 `status` 返回 exit 1、`existing checkout is not runner-generated; refusing to adopt or overwrite`。不绕过该拒绝、不补造旧 SDK provenance。用户本轮明确批准新的 `provenance-sdk-retirement-export-20261010/`、`provenance-sdk-retirement-replay-20261010/` 隔离目录，保留旧重放证据。实现者已在新 export root 经公开 fetch/apply 恢复三个 canonical 项目，仅 clone transport 使用受管本地 cache，真实 provenance 由 runner 生成。
- **下一切片与验收**：复用既有公开 handler/worker Interface 与 native/clock 外部 ports，逐项真实 red→green 修补 native close 失败不能误报 shutdown 成功、失败 handle 的可重试退休、有界 join 超时保留所有权和阻止重叠输出、晚到完成不影响 successor；不新增 backend、队列或重写 decoder/clock/wire。本检查点尚无新修复或 green 结论。
- **独立只读调用链核查**：旧 worker `stop()` 在确认 join 完成前清除 thread/queue，handler 随后清除 worker；仅保留 live worker 又会被 start/stream-start 当作可用，必须区分 active 与 retiring。SDK/Bridge server replacement 均先 await disconnect，异常可阻止该次 attach，但 SDK daemon/TUI 对 `TimeoutError`/`OSError` 有重试，不能据此宣称完整 reconnect 安全。handler 的音量监控清理及 daemon/TUI 的后续网络清理可能被退休异常跳过；Bridge subprocess `_force_kill()` 吞掉 post-kill wait 异常后仍可能释放 process handle。后两层生命周期仍须独立补证，本轮 SDK 退休切片不关闭完整 daemon/WebSocket 或 subprocess 票面缺口。
- **范围**：本轮继续限制在无生产数据、host network、D-Bus/Pulse/音频挂载的固定 digest 软件 sandbox；无宿主安装、生产或真实设备操作。本票仍 claimed，硬件票仍 blocked。

### 2026-10-11 shutdown 退休增量：第二版冻结与审阅通过，canonical 重放前

- **实际交付范围**：现有 SDK 模块集中保留 native stream 与单一 pending-close Future；异步 stream-end、Prepare、格式替换和 shutdown 共用退休所有权，确认旧输出退休前不分配 successor。worker 的 stop/重试串行并保留原有 join bound，取消或超时不丢失旧 worker；disconnect 的旧完成不清除 successor。handler 的独立监控清理始终执行，native/取消错误优先级保留；退休中的 worker 不再发布虚假的 start。只修改 SDK `audio.py`、`audio_connector.py` 与 Bridge 两个既有测试文件，复用既有外部 native port 的故障/gate helper，无新模块、backend、队列或 wire 改动。
- **真实 red→green**：第一版复现并修复 shutdown 吞掉 close 错误、join 超时后 attach、无界 native 重试和并发 disconnect 的 `AttributeError`。第一轮审阅另确认既有 legacy stream-end 丢失 handle（旧、新 canonical 都能复现）、严格报错后监控清理的新回归及退休时错误 start。第二版对应实际 red 分别为 `OSError not raised`（1.392 秒）、`0 != 1`（1.688 秒）、`['start'] != []`（1.242 秒），unchanged 单项 green 分别为 1.351、1.271、1.285 秒；stop 失败但 close 成功、重复 pending close/同格式重准备、晚到失败/latest owner/stale release 为直接 green 保护，不冒充额外 red。
- **冻结开发验证**：SDK `Ran 39 tests in 6.046s / OK`、Bridge `Ran 84 tests in 50.431s / OK`、MA 公开 runner `Ran 26 tests in 2.717s / OK`；原 SDK 七个函数用标准库 fixture adapter 执行原断言，7/7、0.004 秒，不是 pytest/full upstream suite。24 个完整 applied Python 文件在各自锁定 runtime 内编译通过，三个项目完整 base/cached-base diff 检查通过，冻结源码与最终检查期间的非 objects Git 元数据匹配。一次 compile helper 错将 MA 的 Python 3.14 语法交给 Bridge 的 Python 3.13，失败于既有 `player.py` 的 `except ValueError, KeyError`；仅修正验证脚本的 runtime 分组后通过，未修改该源码。
- **第二轮独立审阅已接受**：真实 SDK 39/39（6.095 秒）以及公开 native/monitor/transport probes 验证前轮缺陷已修复，取消错误不会被 monitor 错误替换，独立 monitor 失败在 native 已退休时仍回传且可安全准备 successor；helper 重用保留原断言与 red 敏感性。三个整树及四个改动文件指纹前后匹配；审阅者未修改源码、索引、provenance 或 canonical。Bridge/MA/原七函数结果为审阅者核对 owner 证据，不写成独立重跑。
- **证据与完整性边界**：第一版、第二版及公开故障 probes 保留在批准的 `provenance-sdk-retirement-export-20261010/` root。实施前未捕获完整整树指纹，不补造该证据；第二版修订前实际捕获的 snapshot 与第一版 freeze 匹配并已保留。SDK 从 `/runtime-sources/sendspin/` 导入，实际 sendspin 7.5.0/aiosendspin 6.1.1；本开发轮未单独新探测 MA 的包版本。一次 API 配额中断留下部分第二版源码，恢复后核对真实状态继续，未 reset/重复实现或覆盖旧失败证据。
- **尚未交付到 canonical**：候选 export 与全新 runner-owned replay 已委托执行，结果尚未验收；主仓库 SDK/Bridge/MA 补丁与清单在本检查点仍未改变。软件开发快照和独立审阅通过不代替干净重放、真实音箱 ready 或首音时延。完整 daemon/TUI finalizer、subprocess reap、失联回传 MA 并冻结权威 source/timeline、controller/WebSocket 与硬件/声学验收仍未完成，本票仍 claimed。

### 2026-10-11 shutdown 退休 canonical 验收检查点

- **Canonical 已写回**：同一冻结 runner-owned export checkout 经公开 `export --working-tree` 导出 SDK/Bridge 候选，经过第二轮独立审阅及全新干净重放后，协调者只写回现有 SDK 补丁、Bridge 补丁与清单。清单差分仅为两项 patch SHA-256；MA patch 字节及完整 project 配置未变，所有 pin、镜像 digest 与 runtime dependency 配置未变。版本和 checksum 仍以清单为唯一登记来源；未修改 Navidrome Go/UI 或复制 upstream 源码。
- **真实公开重放**：新 `provenance-sdk-retirement-replay-20261010/` root 经真实 runner fetch/apply/test 得到 Bridge `Ran 84 tests in 49.342s / OK`（含 SDK 39 项）、MA `Ran 26 tests in 2.456s / OK`，无 skipped；额外 SDK class `Ran 39 tests in 6.098s / OK`，原七函数 adapter `Ran 7 tests in 0.004s / OK`。后者不是 pytest/full upstream suite，所有耗时均不是首音指标。证据为该 root 的 `validation-summary.json`、`final-supplemental-integrity.json`、两个 `*-test-result.json`、`sdk-class-replay-verified.json` 与 `upstream-function-replay.json`，旧阶段证据未覆盖。
- **完整性与运行时**：三个 replay 整树 inventory、四个增量文件与第二版 freeze 完全一致；三个完整 applied base 差分检查通过，24 个 Python 文件在各自不可变 runtime 内 compile 通过。SDK package/audio/connector/daemon 实际均从 `/runtime-sources/sendspin/` 导入，sendspin 7.5.0/aiosendspin 6.1.1，MA 本次新 probe 为 aiosendspin 9.1.1。export frozen 源码、provenance 和非 objects Git 元数据在导出、重放及额外测试前后一致；新 replay 的对应完整性也一致。provenance 由 runner 真实生成，不收养默认 SDK 开发目录；不把第二版前实际 snapshot 说成第一版修改前捕获。仅 clone transport 使用受管本地 cache、保留 no-hardlinks 并恢复 HTTPS origin，不声称本轮测了 GitHub 下载 transport。
- **失败步骤如实保留与验证层评估**：export 前 helper 按字符串而非原算法的 `Path` 排序，指纹 guard 在任何写入前停止；按原算法核实三树匹配后仅修正 helper。额外 SDK 命令重复 `-B -m unittest`，首次 exit 2、零测试，原 `sdk-class-replay.json` 保留；改为复用捕获的公开 runner 安全 command prefix 后一次重跑 39/39，记录于独立的 verified 文件。宿主修正语句也曾因语法错误在执行前被拒绝，未运行测试或写文件。临时验证脚本的多次配置差错不归因于产品源码：验证层应收敛到既有 runner/清单的单一命令构造，不继续堆叠临时参数拼接或另建 runner；这些证据不支持更换 MA/SDK 产品栈。协调者最终安装后 `validate`、runner `Ran 57 tests in 0.825s / OK` 与仓库 `git diff --check` 通过。
- **范围与下一步**：native 调用无法被线程 join 强制终止；超时/取消保留资源所有权并拒绝重建，清理失败由显式 stop 重试驱动，不宣称自治回收 daemon。完整 daemon/TUI finalizer、subprocess reap、失联反馈到 MA 的 source/timeline、controller/WebSocket 与硬件/声学验收仍开放；本票仍 claimed。下一切片的只读核查建议复用现有 lease Interface，但先改成不重连/不唤醒的 fresh 复验，再由 MA 播放会话有界监测并经权威 pause/stop 冻结进度；当前显式 PCM 复验调用 Prepare 会隐式修复失联，不能直接加轮询后冒充可靠失联反馈。该建议尚未实现或测试。
- **未执行**：完整 upstream lint/pre-commit/pytest、patched image 构建/发布、生产部署、真实音箱或声学实验均未执行；无宿主依赖安装，测试容器固定 digest、断网、只读、去 capabilities、禁止提权，无生产数据、host network、音频、Pulse 或 D-Bus 挂载。真实设备仍须逐次确认维护窗口，软件检查点不关闭硬件票。

### 2026-10-11 运行中失联反馈切片：续认领与实施范围

- **续认领**：用户本轮要求继续推进本目录计划；沿用地图已记录的技术判断与隔离开发授权，本票保持 claimed，不把软件测试当作硬件票解阻塞。
- **顺序与 Interface**：先把现有 lease 复验收敛为不 Connect、不 wake、不重新分配 native output 的 fresh、相关联检查；再评估 MA 现有播放会话中的配置化有界监测。失联由 MA 的权威 pause-as-stop 路径冻结 source/timeline 并保留队列，不当作自然 EOF 或推进下一曲；恢复需新的 Prepare。旧任务、取消和 successor 的竞争由同一使用权关联约束，未配置路径保持兼容。
- **验证计划**：在新的 runner-owned 隔离 root 内恢复锁定 canonical 基线，逐项公开 Interface red→green，独立审阅冻结候选，再全新 provenance 重放。只修改现有补丁/清单和本票进度；不复制上游源码、不另建 runner、不触碰已有未跟踪资产或生产/真实输出。本检查点尚无新实现、测试或成功结论；若 MA 整体接入尚未可安全验收，先交付可独立使用的非修复式复验前置切片并明确剩余范围。
- **只读 MA 核查与范围收敛**：锁定 MA `SendspinPlaybackSession.cancel()` 区分取消与正常 EOF，但普通 `SendspinPlayer.stop()` 清除 media/time 并报告 IDLE；queue 的 pause-as-stop 会先保存 `resume_pos`，却未包含 stop 的全部预加载/缓冲清理。`playback_tracker.py` 的自然结算/动态补队列和 `queue_buffer_completed()` 的延迟恢复不能只靠短暂 `transitioning` 或现有 `stop_called` 避免。Bridge 的 Pause 还可能先于管理面失联观察到达。因此本轮优先交付非修复式复验前置切片，不加入未经权威队列验证的 polling+Stop。下一阶段建议在现有 queue-control seam 集中异常失联 disposition、会话/task/lease 身份检查、既有 `resume_pos` 保留、session-owned source/buffer/preload 清理以及显式恢复前的 EOF/延迟任务抑制，不另存第二份时间线。flow/non-flow 近曲终失联、Pause 先到、旧任务晚到与真正 EOF 的公开集成测试尚未运行；跨 fade/seek、协议 parent routing 与真实事件顺序仍待证明。协调者本轮已实际得到 canonical `validate` 成功、runner `Ran 57 tests in 0.838s / OK`，不把此基线当作新产品功能通过。

### 2026-10-11 非修复式复验前置切片：候选冻结，独立验收中

- **候选范围**：新的 `provenance-loss-feedback-export-20261011/` root 经既有 runner/transport 恢复基线。SDK 的既有 worker control queue 新增只观察 exact prepared backend/epoch/PCM 的操作；Bridge 既有 `BindOutput` 增加 verify-only，fresh observation 与 binding authority 分开，ready Prepare 复验不重连、不 wake、不重新分配/路由修复。新增关联 lease/epoch、配置化有界的 `/api/output/verify`，MA 既有 `OutputPreparationClient` 增加 `verify()`；后者尚未接入 active-session monitoring。只扩展 11 个既有 upstream 模块/测试文件，MA playback/queue/tracker 未修改，无新模块或第二 runner。
- **真实开发 red→green**：公开 SDK handler 缺失 observe Interface（`AttributeError`、1.301 秒→1/1、1.256 秒）；ready lease 路由丢失被隐式修复（`PreparationError not raised`、0.316 秒→1/1、0.332 秒）；daemon verify-only 重开已释放输出（`[True,False,True] != [True,False,False]`、2.134 秒→1/1、2.140 秒）；HTTP verify 尚未注册（404 != 200、0.060 秒→1/1、0.056 秒）；MA client 缺失 verify（`AttributeError`、2.150 秒→1/1、2.222 秒）；uncancellable observation 令 verify 超过 deadline（0.115 秒失败→1/1、0.021 秒）。额外六项 fencing/故障保护直接 green（5.529 秒），不冒充额外 red。均为固定 digest、断网只读软件 sandbox，不是物理输出或首音证据。
- **冻结开发验证与完整性**：Bridge 95/95（55.397 秒，含 SDK 42 项）、MA 29/29（2.582 秒）、额外 SDK 42/42（6.046 秒）。`initial-inventory.json`、`frozen-source-fingerprints.json` 与 `export-integrity.json` 保留整树和非 objects Git/provenance；公开 export 前后 source/metadata/provenance 匹配，三个完整 base-relative diff 检查 exit 0。协调者核对三个 patch 实际 SHA-256 与候选清单一致，清单除三项 checksum 外完全不变；pin、runtime digest、dependency/test 配置均未改。
- **尚未 canonical 验收**：独立只读 Interface/lifecycle 审阅与全新 runner-owned replay 正在执行，结果仍待验收；主仓库三份补丁和清单尚未写回。候选只是供下一阶段使用的复验前置切片，不宣称 MA 已冻结 source/timeline，也不关闭 daemon/TUI finalizer、subprocess reap、controller/WebSocket、硬件/声学缺口；本票仍 claimed。

### 2026-10-11 非修复式复验前置切片：canonical 验收

- **干净公开重放**：全新 `provenance-loss-feedback-replay-20261011/` root 经真实 runner fetch/apply/test 得到 Bridge 95/95（57.546 秒，含 SDK 42 项）、MA 29/29（2.633 秒），无 skipped。额外 SDK 42/42（6.406 秒）、原 SDK 七个函数经既有标准库 fixture adapter 7/7（0.005 秒，不是 pytest/full upstream suite）、runner 57/57（0.919 秒）。19 个 Bridge/SDK 与 5 个 MA 完整 applied Python 文件分别在锁定 runtime compile 成功；SDK 模块实际从 `/runtime-sources/sendspin/` 导入，sendspin 7.5.0/aiosendspin 6.1.1，MA aiosendspin 9.1.1。各耗时均不是首音指标。
- **独立审阅与实测**：窄增量未发现已确认缺陷；独立 Bridge 95/95（58.421 秒）、MA 29/29（2.663 秒）以及额外 wrong-PCM/never-prepared-epoch SDK 非修复 probe 2/2（0.378 秒），均 exit 0、无 skipped。7248 文件审阅前后 inventory（排除 Git objects 与本次 evidence）一致，frozen per-file source、非 objects Git 元数据、runner provenance 与五项候选 canonical artifacts 均匹配。证据位于 export root 的 `review-verify-evidence/`；不把 owner 的补充 SDK/七函数重放说成审阅者独立重跑。
- **生命周期疑虑的明确 disposition**：同一 lease 改变 PCM 被 manager 在调用 adapter 前拒绝，后继使用权新建 opaque epoch 并由现有 current 检查隔离；accepted canonical caller 不复用同 epoch 改格式。daemon handler 在当前生命周期仅初始化一次，reconnect attach/detach 复用它，`validate_output()` 在 await 后核对 exact worker。审阅者未证实自查提出的 captured-format/whole-handler 替换能经当前接受路径形成回归，本版不堆叠推测防御层；直接 adapter 调用违反不可变 epoch/PCM 契约或未来替换 handler 的实现不属于本次证明。
- **完整性与安装**：`final-integrity.json`、`supplemental-integrity.json` 证明 export freeze、replay 源码/Git/provenance 在测试与补充验证前后一致，三个完整 base-relative diff 检查通过。协调者安装前再次核对旧 canonical manifest/patch hash 未变、export 与 replay artifacts 字节一致、候选清单只变三项 checksum，随后仅写回既有 Bridge/MA/SDK 补丁与清单。安装后 canonical `validate` 成功、协调者 runner `Ran 57 tests in 1.043s / OK`、仓库 `git diff --check` 通过；提交范围仅本票、三份补丁与清单。clone transport 复用受管本地 cache、保留 no-hardlinks/恢复 HTTPS origin，不宣称测了 GitHub 下载 transport；无 SDK provenance 收养或旧证据覆盖。
- **验证过程边界**：复用既有 runner、drive、fingerprint/probe/fixture adapter 和捕获的安全 command prefix，无新增 runner/helper。协调者只读证据摘要误将整数 `compiled_python_files` 当列表调用 `len()`，产生 `TypeError`；无测试或文件改动，改用直接 Read 核对 24，不归因于产品失败，也未继续堆叠参数构造。passing suite 中出现的 native/monitor/HTTP 故障 traceback 是既有注入断言，不是测试失败。
- **交付与剩余范围**：交付 authenticated、有界、相关联的非修复式复验 Interface 及 MA client；active-session polling、MA abnormal-loss disposition/source/timeline 冻结仍未接入。下一阶段沿用前述 queue-owned seam 和公开集成测试，不用 polling+普通 Stop 替代权威证明。本票保持 claimed，硬件票保持 blocked；完整 upstream lint/pre-commit/pytest、patched image 构建/发布、生产操作、真实音箱/声学实验均未执行。回退只需撤销本次 canonical 代码提交，不涉及生产容器或数据。
- **验收后的补充证据登记**：canonical 代码提交 `31552020` 已备份到 fork 同名开发分支。审阅者补充报告 MA `/verify` 阴性公开 probes 3/3（0.014 秒，wall 5.406 秒，exit 0），覆盖 HTTP 503、无效 JSON、非 ready/外部 output/null 或 bool epoch，均拒绝且无 Prepare/Release 副作用；协调者核对 `review-verify-evidence/ma-adversarial-probes.json` 真实输出，完整 7248 文件 freeze guard 仍不变。该补充登记不修改产品代码或扩大验收范围；WSGI 外层 504 取消观察与内层 fresh 检查截止 503/撤销当前 lease 已在 Interface 分开说明。

### 2026-10-11 MA 运行中失联权威处理：续认领与范围

- **起点与续认领**：用户要求继续；沿用地图中的技术判断/隔离软件开发授权，本票保持 claimed。起点为已验收的非修复式复验 `31552020` 和补充证据 `68ca8852`；协调者本轮 canonical `validate` 成功、runner 57/57（0.816 秒），已跟踪工作区干净。
- **目标与单一权威**：配置化有界的 active-session 观察调用现有 `verify()`，失联由现有 MA queue-control seam 集中处理异常 disposition；在 awaited teardown 前保存或沿用权威 `resume_pos`、current item/index，并关联 queue session、playback task 和 lease。复用现有源/缓冲/预加载清理，抑制失联后的自然结算、动态补队列与延迟自动恢复；explicit resume 才清除 disposition 并重新 Prepare。Bridge Pause 先到时不得从 STOPPED 的零时间重算位置，旧观察/清理不得影响 successor。不复制队列/时间线，不把普通 Stop 或仅暂停 downstream 当完整冻结。
- **实施与测试范围**：优先只扩展既有 MA provider/queue/controller 模块及现有测试文件；Bridge/SDK 先保持已验收字节。新 runner-owned export root 保留修改前完整 inventory、Git/provenance 和逐项公开 red→green 证据；测试实际 queue/tracker/source/buffer seams，包括 flow/non-flow 近曲终失联、Pause 先到、旧会话晚到/explicit resume、正常 EOF、配置关闭与管理面故障。候选须冻结、独立审阅及全新 runner 重放后再 canonical 写回；若变成 broad queue rewrite 或三次同类集成阻塞，暂停并评估较小安全切片，不盲目扩大实现。本检查点尚无新功能/测试成功结论。
- **边界**：无新模块/文档/runner、无宿主依赖安装、无生产服务/配置/数据库/音乐库/真实音箱操作；只允许固定 digest 断网只读软件 sandbox，硬件票仍 blocked，完整 upstream 和声学证明仍独立计算。

### 2026-10-11 MA 失联权威处理：第一版冻结与独立验收中

- **冻结候选**：新的 `provenance-ma-loss-authority-export-20261011/` root 经既有 runner/drive 恢复基线；初始→冻结仅八个既有 MA 文件变化：queue `controller.py`、`playback_tracker.py`、`queue_loader.py`、`state.py`，Sendspin `constants.py`、`playback.py`、`player.py` 和既有 `test_output_preparation_client.py`。Bridge/SDK 整树与 Git/provenance 未变。公开 `export --working-tree ma` 后候选 MA SHA-256 为 `225403c594b947fc26aa9836464dd16b19b81ccd688a9dc5ab90fb188bfa9176`；清单仅该 checksum 变化，版本、digest、dependencies/commands 未变。7215 source/Git/provenance entries 导出前后匹配 freeze，三个完整 pinned-base diff 检查通过；主仓库 canonical 尚未写回。
- **真实公开开发 red→green**：真实 queue/session/AudioBuffer/tracker 临近曲终 HTTP 503 未停止（2.570 秒失败→7.341 秒）；flow 晚到 buffer-completed 自动启动下一曲（2 != 1→4.412 秒）；Pause 先到泄漏 producer（TimeoutError→7.324 秒）；真实 MA 可重入锁下 explicit resume 保位（IDLE != PLAYING→2.135 秒）；已排队结算仍 fetch dynamic radio、旧 refill timer 在冻结后及 successor 上仍 fetch（分别 1 != 0→7.369、2.137、2/2 共 2.499 秒）；真实锁下新意图先到误复用 lost nonce、分离 nonce 后旧 buffer 仍未取消（分别真实 red→同项 2.199 秒 green）；晚到 timing correction 把 98 改为 500（500 != 98→与未配置兼容项 2/2、2.244 秒）。所有 green 均复用失败断言；直接 green 的管理面故障、晚到 producer 清理与自然 EOF 保护不写成额外 red。各耗时是软件 unittest 时间，不是首音指标。
- **验证与止损**：完整 MA focused suite 43/43（26.490 秒，无 skipped）；九个完整 applied MA Python 文件在锁定 Python 3.14.7/aiosendspin 9.1.1 内 compile 成功。两项初期 external-port 缺失与第三项普通 `asyncio.Lock` 替身造成的恢复死锁单独保留，不计产品 red；第三项触发停止堆叠替身，改为复用现有 `PlayerController` 可重入锁，而非复制 lock/helper 或因此更换产品栈。真实锁的 30 秒 fallback 不能作为所有权保证，候选据真实竞争 red 在等待锁前分离旧 client/lease、启动 captured old source 清理，并用 exact session/task 身份跳过旧 Stop。`pre-commit` 实际在断网只读 sandbox 尝试，exit 1、`FatalError: git failed. Is it installed, and are you in a Git repository directory?`；未据此确认具体 Git 根因，Ruff 不可用，不宣称 lint/full upstream suite 通过。
- **待独立验收**：公开编译/export 及开发证据冻结，独立只读审阅与全新 runner-owned replay 已启动；重点复核 source-finally 而非仅 cancelled flag、外部 fetch 已在 await 中的旧 timer/settlement、同 lease 移交、后继 Stop/清理与配置关闭边界。尚未关闭协议 parent routing、真实 controller/WebSocket、crossfade/声学等缺口；本检查点不是 canonical 验收或硬件票解阻塞。

### 2026-10-11 MA 第一版独立审阅：changes requested，禁止安装

- **已确认缺陷**：真实命名 refill timer 已进入 managed-pool 的外部 radio fetch；fetch 在取消后仍完成，期间 HTTP 503 触发失联、用户经公开 `queues.resume()` 恢复。`queue_loader.py` 的 post-await 只检查 `queue_data` 对象，恢复复用该对象，旧可用 track 因而追加到 successor 队列。独立公开 probe 实际得到 `['Audible track', ' - Late old refill']` 而非单曲队列，`Ran 1 test in 2.132s / FAILED (failures=1)`、exit 1、wall 5.822 秒。协调者已核对真实 `review-ma-authority-evidence/inflight-dynamic-refill-available-probe.json`；它没有修改私有 queue state 或替换内部 managed-pool，门闩仅在外部 radio/HTTP/scheduler ports。
- **审阅与完整性**：独立完整 focused suite 曾 43/43（26.518 秒，wall 29.858 秒，无 skipped），不能覆盖上述失败。第一项探索 probe 因 track 缺 provider mappings、不可播放而 green，已保留且明确不是安全证据；修正 external track 并断言 available 后才真实失败。确认缺陷后审阅停止，7215 frozen source/Git/provenance records 与 7261 文件完整 inventory 前后一致，候选 patch/manifest 未变；更广生命周期审阅尚未完成。第一版不接受、不 canonical 写回，若其并行重放通过也只算已拒绝版本的保留证据。
- **修订路径**：第二版在新的 runner-owned `provenance-ma-loss-authority-v2-export-20261011/` root 从 frozen v1 artifacts 真实 fetch/apply 恢复，先保存与 v1 source 一致的初始 snapshot，再把同一个可用 track/取消后返回公开案例加入现有测试文件并 red→green。修复应复用既有 context helper，在异步 fetch 后核对 captured session/disposition 和 queue identity，有限审查相关 mutating await，不另存队列或不断堆叠局部 guard。v1 freeze/evidence/replay 原样保留；v2 重新完整回归、冻结、独立更广审阅及全新重放后再决定安装。
- **边界校准**：现有新测试下游交付是 external player port 进入真实 `SendspinPlaybackSession`，不是实例化 `SendspinPlayer` 或真实 ControllerPause/WebSocket dispatch。Pause 案例是公开 queue.pause 先到，正常取消 observer，不靠假装 post-cancel loss 回调证明。自然 EOF 案例为真实 session EOF→external group-stop 交付→真实 tracker ended。same-lease/epoch keep_stream/leader-transfer、实际 in-flight `SendspinPlayer.stop` 与 unlocked successor、协议 parent routing、crossfade、queue.clear/观察登记退休和完整 WebSocket/声学仍需证明，不能从 fresh-nonce 测试推导。本票仍 claimed，生产/硬件边界不变。

### 2026-10-11 MA v2 续接：autoplay 异步结果隔离，候选未验收

- **续接而非重复实现**：本轮发现既有 `provenance-ma-loss-authority-v2-export-20261011/` 已有未导出的 v2 source/context-helper 改动和局部 red/green 证据；其 patch/manifest 仍是被拒绝的 v1，不代表当前源码。保留 v1 与原证据，不 reset/覆盖已有开发状态。相对固定上游 base 当前 MA applied 差分共 11 个 Python 文件；本轮实际修复仅修改既有 `queue_loader.py` 与 `tests/test_output_preparation_client.py`，不把先前的 base/managed-pool 等改动说成本轮新增。
- **真实公开 red→green**：episode resolver 晚到结果在 loss→resume 后追加 `Late next episode`，原断言 1 项失败（2.249 秒），post-await 使用 captured `queue_session_context()` 后 unchanged green 1/1（2.285 秒）。新增 music provider/recency 两条公开回归经 `queues.play_media`、`set_autoplay`、真实 scheduler callback、HTTP 503 及 `queues.resume`，只在外部 library/recency ports 设置门闩；两项 red 均追加 ` - Late old refill`（2.230 秒），在 provider 完成、AUTO fallback 前及 recency 完成后复验同一 context，unchanged green 2/2（2.210 秒）。没有 mock context predicate 或修改私有 queue state。
- **开发验证**：固定 digest、断网只读 MA runtime 实际 focused discovery 49/49（26.720 秒，无 skipped）；11 个完整 applied MA Python 文件在 Python 3.14.7/aiosendspin 9.1.1 中 compile 通过，三个 checkout 的完整 pinned-base `git diff --check` exit 0。协调者另跑 runner 57/57（1.202 秒）、canonical `validate` 通过。证据为 v2 root 的 `continuation-{red,green}-autoplay-*.json`、`continuation-full-ma-suite.json`、`continuation-compile-and-checks.json`；`continuation-frozen-ma.json` 保存测试后 2823 条 MA tracked-source/非 objects Git 记录及 provenance/artifact hashes，不倒称为本轮修改前的完整冻结。
- **失败尝试与纠正**：早期子任务误用宿主 pytest，缺 `music_assistant_models` 在收集前退出 4；两条未执行的 private mock 测试新增已精确移除，未安装宿主依赖。随后固定容器的一次单项命令前缀错误在执行测试前失败，保留 `continuation-red-autoplay-episode.json`，实际产品 red 是独立的 `continuation-red-autoplay-episode-verified.json`。协调者读取证据摘要的一次 Python 表达式语法错误没有执行测试或改文件；修正后核对真实 red/green 输出。上述配置错误不算产品回归。
- **仍需验证**：managed-pool `_reconcile_tracks`/`_page_in` 的 paging 后 mutation 是静态风险，已有 finite-source green 主要覆盖 source collection，不能写成 paging race 已复现或已修。窄 autoplay 修复不等于整套 MA loss-authority 候选验收；same-lease/leader-transfer、实际 player stop/successor、queue.clear/observer retirement 和更广控制链路仍开放。本轮尚未 canonical 写回，独立复核与候选导出/全新重放另记结果；完整 upstream pytest/lint、生产操作和硬件/声学实验均未执行，本票保持 claimed。

### 2026-10-11 MA v2 autoplay fencing：canonical 验收

- **独立复核**：在锁定 MA runtime 的断网、只读、去 capabilities Docker sandbox 中，独立重跑 episode、provider-fetch、recency-await 三个公开 queue/session 回归，`Ran 3 tests in 2.315s / OK`，无 skipped。review 前后 2,823 条 source/Git 非 objects 记录、provenance hash 及 Bridge/MA/Sendspin 候选 artifact hashes 一致；未发现本切片 confirmed regression。`managed_pool._reconcile_tracks()`/`_page_in()` 仍仅为未复现的静态风险。
- **全新 provenance replay**：新的 `provenance-ma-loss-authority-v2-replay-20261011/` 经既有 trusted runner/drive 从固定 revision fetch/apply；源码 11 个 MA 文件与 frozen export 内容一致，MA patch hash `7a61eeb3e99969b9152d93689f35e0aaf671860e6bc9e9476feeda5e139b17c1`，Bridge `8418e7e6...9aaa52`、Sendspin `397400f2...e1bbe3` 未变。fresh replay 得到 Bridge `95/95 OK`、MA `49/49 OK`，无 skipped；锁定 runtime 内改动 Python compile 和 Bridge/MA/Sendspin pinned-base `git diff --check` 均 exit 0。一次完整性脚本因跟随 `CLAUDE.md -> AGENTS.md` symlink 误报，按 symlink-aware 规则复核后确认源码、patch 与 provenance 内容匹配；新 replay Git logs/provenance 仅体现新 checkout/runner 元数据差异，非源码差异。
- **Canonical 写回**：仅写回现有 `fork/patches/remote-playback/ma-output-preparation.patch` 与 `manifest.json` 中 MA checksum；Bridge、Sendspin、项目 pins、runtime digests、test commands 未变。安装后协调者 runner `validate` 成功、仓库 `git diff --check` 成功；没有修改 Navidrome Go/UI、生产服务、数据库、音乐库或真实输出。计划票保持 `claimed`，硬件票仍 blocked。
- **范围边界**：本验收证明的是软件 sandbox 中既有 output-preparation/loss-authority seam 及 autoplay 异步结果 fencing，不是实机输出 ready、首音时延、10 秒/15 秒声学指标或生产回滚。完整 controller/WebSocket dispatch、协议 parent routing、same-lease/epoch leader-transfer、实际 `SendspinPlayer.stop`/successor、crossfade、`queue.clear`/observer retirement、daemon/TUI finalizer、subprocess reap，以及 managed-pool paging race 仍开放；完整 upstream pytest/lint 未执行。
