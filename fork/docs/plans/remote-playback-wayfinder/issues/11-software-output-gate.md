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
- 两接口要求显式机器访问 token，使用 `X-Output-Preparation-Token` 和常量时间比较；功能默认关闭，未配置 token 不开放控制。不得把 token 写到 URL、日志或仓库。MA 接入 URL 不含用户信息/query/fragment，明文只允许 loopback，其他地址要求 HTTPS。
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
