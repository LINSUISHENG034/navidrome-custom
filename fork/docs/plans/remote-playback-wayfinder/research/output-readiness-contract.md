# 输出准备与释放契约：源码研究

日期：2026-10-09。对应票：[确定输出准备与释放的最小协调位置](../issues/02-output-readiness-contract.md)。

本报告是只读研究资产：记录源码/规范事实、候选 Interface 的推断及未知，不是实现、上线或人类音箱试验的替代品。

## 1. Scope 与证据等级

- **部署观察（协调者转述，不是本代理重测）**：MA 与 Bridge 容器目前均为 exited；不能称它们当前正在互通。MA 镜像 tag `2.10.4` 的 OCI revision 为 `e30a4974ba951f38e21bea8d502af3b903df992c`；Bridge 镜像 tag `2.75.0` 的 OCI revision 为 `0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad`。没有执行容器内命令确认实际安装包。
- **公开源锁定**：MA 公共 `2.10.4` 解析到上述 `e30a4974…`。Bridge 公共 `v2.75.0` 解析到 `126e766d00876878aa67b5464d0f8f40e6b8c85d`，不是 OCI revision。官方 compare 显示该 tag 比 OCI revision 多一个 release commit，只修改 `custom_components/sendspin_bridge/manifest.json` 和 `src/sendspin_bridge/config/__init__.py`；本报告引用的生命周期代码及依赖文件以 OCI revision 为准，其相关内容也适用于这个公开 tag。另核对 `config/__init__.py:50`：OCI revision 的源码 VERSION 为 `2.75.0-rc.6`，不能与镜像 tag `2.75.0` 或公开 release 的版本常量混同。[V1][V2][B13]
- **依赖声明，不是 actual installed**：Bridge 此 revision 的 `requirements.txt:14,166` 声明 `aiosendspin==6.1.1`、`sendspin==7.5.0`；MA Sendspin `manifest.json:10` 声明 `aiosendspin[server]==9.1.1`。Bridge 源码注释仍出现 `5.1.1`/`7.0.0`，不能用这些旧注释覆盖依赖文件，也不能用任一声明代替容器内实际版本。[B0][M0]
- **依赖源码候选**：公开 `aiosendspin` tag `6.1.1` → `af9dbc8b625bffbee99d7a67712c0e3b4c44147d`；tag `9.1.1` → `5c024b42893bc6e372f6c87bb8504552051daf24`；公开 `sendspin` tag `7.5.0` → `22b9ff308afedd86e51ea1b88ededa574c9244e5`。[V3][V4][V5]
- **未部署的候选快照**：Go SDK `c61a40513249b22de9a5077b0acd1b4682a0b697`；Sendspin 规范 `ee8aad9660fc57345e726a888486f47f66a41f97`。这两者是公开源码研究的固定 scope，不是现场版本或已经证明的兼容组合。尤其规范快照里的 wire/字段演进，不能反推 Bridge 旧 SDK 已合规。[G1][S1]
- **旧客户端兼容边界**：MA 固定 provider `provider.py:406–408,462–470` 默认 `allow_legacy_clients=True`，并传给 `allow_unencrypted` / `allow_noncompliant_clients`。这只证明源码存在兼容许可路径，配置实际值与现场互通未验证；不能仅按最新 Noise/规范要求宣判旧 Bridge 不能连接。[M6]
- **工具边界**：已先调用 research Skill；Context7 `resolve-library-id("Sendspin")` 返回 `TypeError: fetch failed`，本问题没有获得 Context7 内容，也没有调用 query-docs。随后只读取官方 GitHub/raw/PyPI 公共来源；没有下载源文件到仓库，没有操作 Docker、D-Bus、蓝牙、音频、服务、认证 API，没有安装、构建、测试或实播。

本文中的“源码事实”意味着固定源码确实存在该调用/条件；不意味着现场发生了该时序。“推断”是从这些边界提出的候选设计；“未知”必须在另行批准的原型或 HITL 中回答。

## 2. 最重要的新事实：哪些既有 hook 可以 await

### 2.1 MA 已有合适的异步应用入口，但没有现成的通用 BT prepare ACK

**源码事实**：MA `models/player.py:945–960` 定义 `async play_media(media)`，文档明确让 provider 自己处理请求；这是已有应用扩展边界，不必先改整个 MA controller。[M1]

在目标 Sendspin provider 中，`SendspinPlayer.play_media()`（`player.py:1431–1473`）先取消旧媒体任务，然后调用 `await playback_session.start(media)`。`PlaybackSession.start()`（`playback.py:435–444`）的 await 并不等到声音就绪，而是建立 `_run_playback(media)` 后台 task 即返回。`_run_playback()` 在 `739–784` 创建 PushStream、随后生产/提交 PCM；`849–915` 的 `commit_audio()` 返回时间戳后更新 session timeline。[M2][M3]

**值得优先验证的具体落点**：在这个已有、可被 `PlaybackSession.cancel()` 取消和 await 的 task 中，**进入 `_create_push_stream()` 和 PCM 内容消费/提交之前**等待输出准备。`SendspinPlayer.play_media()` 继续负责接受播放意图，session 管理准备与输出使用权，不把长时间蓝牙等待塞进浏览器或所有 MA controller。此处是可复用的应用边界；不是已经实现的 BT hook。[M2][M3]

以下现有 hook 不能直接冒充该保障：

| 已有入口 | 固定源码行为 | 对 BT output-ready 的意义 |
| --- | --- | --- |
| `Player.on_protocol_playback()` | 是 async，但文档明确 **after play_media**；controller `4347–4354` 也是先 `await target_player.play_media()` 再回调 | 太晚；不能当 pre-stream gate。[M1][M4] |
| controller 的 power-on | `4347–4350` 在 play_media 前 await power，受 powered/power_control 条件限制 | 可供有电源模型的 provider 复用，但不是每次播放都会调用的 generic prepare，也没有本音箱的 ready 结果。[M4] |
| MA Cast app readiness | `player.py:1444–1473` reset future，**start 后**等待；明确 Cast-only | 是已有异步 readiness 模式的证据，不是 BT ready ACK；不能把 Cast future 或其 30 秒设置照搬成 BT 保障。[M2] |
| MA 内置 `BridgePlayerRole.on_stream_start()` | `bridge_role.py:245–249` 同步调用 callback；其 `supports_preconnect_audio()` 返回 true | 这是 MA 内置协议桥角色，不是外部 BT Bridge 的通用 async prepare 接口。[M5] |
| Python SDK `prepare_audio()` | `push_stream.py:928–947` 存储下一次 commit 的 PCM | “prepare”指音频块，不指 BlueZ/Pulse 物理输出。[P1] |
| Python SDK role activation | `group.py:177–184` 为新激活 role 加入现有 stream；`Role.on_connect/on_deactivate` 是同步生命周期入口 | role/连接激活不等于本次会话可 await 的 BT 准备完成。[P2][P3] |

这不是对所有 MA provider 或未来上游的全仓否定；只是在目标版本的原生 Sendspin 路径及上述具体 hook 中，没有找到一个可以直接 await 并获得 BT ready ACK 的现成实现。

### 2.2 Bridge 的 activation/wake 与 CLI hook 不能提供当前所需的 prepared 契约

- Bridge `device_activation.py:438–450` 的 `activate_device()` 是同步的启动/在线激活装配入口；其结果包含 BT 可用性探测，不是由某个播放意图驱动并 await 到输出就绪的事务。[B1]
- Bridge `api_bt_wake()`（`api_bt.py:270–292`）对 `_wake_from_standby()` future 最多等待 5 秒，异常仅日志记录，仍返回 `success: true, "Device waking from standby"`。该回包不是 ready，也没有意图 generation/lease。[B2]
- `_wake_from_standby()`（`client.py:1161–1190`）只进入 waking 并主动 connect；保持 standby 直到随后 reroute。`_reroute_to_bt_sink()`（`1204–1256`）可在无存活 stream 时请求 reconnect，并仍返回 true。因此 await wake、`bt_waking == false`、reroute 返回 true，都不能单独构成“特定 daemon 已能从目标 sink 执行首个音乐样本”的证明。[B3]
- 继承的 `sendspin 7.5.0` `SendspinDaemon._on_stream_event()`（`daemon.py:441–457`）用 `create_task(run_hook(...))` 运行 hook-start/hook-stop，没有 await 完成。Bridge 自己的 override（`bridge_daemon.py:435–449`）是同步通知 playing/status。[D1][B4]
- Bridge 声明的 `aiosendspin 6.1.1` 客户端 `add_stream_start_listener()`（`client.py:648–656`）是 listener 注册，`_notify_stream_start()`（`1238–1243`）同步调用 callbacks。收到 stream/start 后再通知客户端，并不是服务器在内容消费前等待物理准备的反向 ACK。[P4]

**推断**：可扩展已有 Bridge wake/status、adapter lease、sink 路由能力，而非另造 Sendspin wire；但必须补上有相关性的完成/取消语义。单纯给 hook-start 放一个阻塞脚本，或把现有 wake HTTP success 当 ready，不能满足目标。

## 3. 与既有静态事实的最小连接

以下只保留决定落点需要的调用，不重新做 Bridge 全生命周期大全。

1. **冷启动**：`client.run():2549–2557` 对配置 BT 的设备推迟 daemon；`2569–2615` 后台任务主动 Connect，待 link/sink 后启动播放器。因此彻底冷启动下逻辑播放器可能尚不可达。单纯 MA provider 内的 pre-stream gate 主要解决“daemon 仍注册的自动 standby”路径；要实现“物理输出不存在也仍可接受 intent”，还需选择保持逻辑端在线，或由权威保存请求并调用 Bridge 准备，不能谎称现成冷启动路径已有此能力。[B5]
2. **自动 standby**：`_enter_standby():1119–1152` 保 daemon/null sink，null sink 创建失败会 fallback stop；`_on_standby_play_detected():1192–1202` 明写内容在 null sink 静默流动，等 BT 重连后再移动。`_handle_daemon_message():1901–1903` 在 playing true 后调度 wake。这条链没有“物理 ready 后再开始内容时间线”的前置等待。[B3][B6]
3. **待机抑制重连**：`bluetooth/monitor.py:189–191,491–500,533–541` 在 standby 且非 waking 时等待，waking 时允许重连。未来 lease 释放/取消应继续控制同一个策略，不能再起第二个独立重连 loop。[B7]
4. **手动 Release**：`set_bt_management_enabled(False):2666–2697` 关闭管理/取消重连并停止 daemon，后续断开 BT；它不同于保逻辑可控的自动 standby。不要把手动 Release 作为每次暂停/idle 的最小协调实现。[B8]

**证据边界**：这些调用证明“没有前置保障、存在静默消费路径”，不证明每次播放必然缺失，也不证明必丢固定几秒。影响取决于 BT/sink/backend 暖机、lead/buffer、reroute/reanchor 和实际声音时序；本轮没有测量。

## 4. available、lead 与时钟分别保证什么

### 规范事实（仅上述规范快照）

- `messaging.md:114–122,295–307`：时钟过滤器建立服务器/客户端时间映射；player 的 available true 要求时钟足够收敛。available false 描述不让位给 Sendspin 的外部系统占用，不是“请先替我连接蓝牙”。[S1]
- `messaging.md:390–403`：服务器不得在最新 available 为 false 时发送 stream/start；角色激活要求新的 state，但不定义 BT prepare/release 事务。[S1]
- `roles/player/v1.md:47–65,145–160`：required lead 是 startup timing hint，min buffer 是持续缓冲请求；存在最低发送提前量/容量约束，required lead 不构成异步 ready ACK，也不证明蓝牙链路、目标路由或音箱功放就绪。[S2]

### 对目标源码的意义

- Bridge `bridge_daemon.py:173–187` 把 lead/min buffer 交给 SDK，不能把这些数字变成已验证的设备准备结果。[B4]
- Python server SDK `PushStream.commit_audio():1023–1166` 在提交时决定播放时间、推进 channel timing 并发给 roles；`PlayerV1Role.on_stream_start/on_audio_chunk():272–340` 把 stream/start 延迟到首块编码音频。若拿 stream-start 当唯一 wake 触发，单纯延长 hint 并不能创造前置阻塞/相关性。[P1][P5]
- MA `players/controller.py:454–461,3040–3042` 在重定向播放命令时检查 player availability。MA 的 `PlayerState.available` 与 wire `client/state.available` 不是同一个字段，不能直接混同；两层都说明“把可唤醒逻辑目标表示成不可接受请求”会带来风险。[M4]

**推断**：若把 BT 未连映射成 wire available false，又只在 stream/start 时连接，就会出现“不给 start → 没有 wake → 始终 false”的设计循环。必须有独立于内容流的准备触发。是否当前现场存在这个循环未验证；也没有建议在旧协议里硬塞当前规范的 available 字段。

## 5. 最小候选协调位置及 Interface

### 5.1 单一协调边界，两侧各守自己的职责

**最值得进入下一轮验证的候选**：协调在**播放权威的 session → 输出 adapter 的 pre-stream 边界**。MA 路线优先扩展其已有 Sendspin `PlaybackSession`，让受 cancel 管理的准备 task 在创建/提交音乐流前 await Bridge 输出 adapter。Bridge 内集中物理 prepare/release（复用连接、monitor、sink、daemon 能力），但不把队列/推进权威迁给 Bridge。Go 候选复用同样的输出契约，不能照搬 MA management API 作为共同底层。

适用顺序是“接受当前 intent → preparing（保持逻辑可控，未消耗内容时间线）→ ready lease → 核验 intent/lease 仍当前 → 启动内容流”。有界预读可以作为性能优化，但不得提交/丢弃首段或以 wall clock 推进曲目；最小验证先在内容读取前设置 gate，更容易审计。

不选以下位置作为唯一协调者：

- 浏览器、HA 或外置“先 wake 再 play”脚本：不能覆盖别的控制入口，也不能可靠撤销过期步骤。
- Bridge 的 stream-start 回调：过晚；服务器已经有内容时间戳。
- 单独 available/lead 参数：既非相关性 ACK，又可能拒绝触发意图。
- 重写全部 MA、另做完整私有 Sendspin 协议：当前证据没有证明这是必要最小面。

### 5.2 最小行为 Interface（建议，不是实现承诺）

只需先讨论两个操作及一个不透明结果；具体 transport 暂不选择：

- `Prepare(cancel_context, output_id, intent_generation, deadline) -> ReadyLease | failure`
- `Release(cancel_context, lease, reason) -> completion`
- `ReadyLease` 标识 output、当前准备/输出 epoch、所属 intent/播放会话及失效条件；调用方不学习 BlueZ/Pulse 命令顺序。

必要契约：

1. Prepare 可等待、幂等/合并同一当前输出的准备；当前可复用 lease 不为每次切歌重新 Connect。成功至少表明目标 link、音频 profile/sink、daemon/backend 的目标绑定与当前输出 epoch 一致，失败带可区分原因。**这仍只是工程就绪判据，不自动证明声学首段完整**。
2. 不能以 BT Connected、sink 名缓存、wake HTTP success、reroute true、clock sync 或 available 任一单值授予 lease。idle 时可能根本没有 sink-input，不能机械地用“moved > 0”作为所有准备的唯一必要条件；backend 怎样在没有音乐内容时完成初始化，是原型待答问题。[B3][B4]
3. Stop/超时/新 intent 先撤销旧 generation 与内容提交许可，再 cancel 准备/播放任务；物理 cleanup 可后续 await。所有 late completion、sink-found、reconnect/reanchor 必须核验 lease/epoch，不得自行播放或恢复旧队列。单独 cancel Python task 不等于 executor 中 Connect 已终止，不能以“超时返回”当作所有副作用已撤销。
4. Release 仅清理自己持有的 lease；旧失败清理不得断开新会话已取得的同一输出。保持逻辑控制在线与禁止后台重连应明确区分；不默认执行现有停 daemon 的手动 Release。
5. 断线先令 lease 失效，权威停止/冻结内容消费，输出失败不当自然曲终；是否暂停保位、自动恢复或再次接受 intent，是单一会话政策，不由 monitor 自作主张。
6. 暂停可保留 lease；真正进入 idle release 时先停止内容提交/排空或清除按政策保留的 buffer，再释放物理输出。切歌不等于释放输出使用权。所有 resume/direct controller command/seek/skip 入口都必须落到同一 gate，不能只修一个 UI 的 play_media。

地图的“10 秒内可听、15 秒未就绪明确失败”是未来候选预算：deadline 应为配置/政策，不是本轮已达到的性能，也不能直接把 CLI/MA Cast 的现有 timeout 当验收结果。

### 5.3 Stop 的已有保护与未知，严格分开

**已有源码保护**：MA `SendspinPlayer.stop():1402–1429` 调 group.stop，再 cancel playback；`PlaybackSession.cancel():405–433` cancel 并 await 当前 task；Python SDK `commit_audio():1037–1050,1077–1078,1155–1156` 有 stopped/stream-generation 检查。本报告不能把目标 MA 写成“完全没有 Stop-cancel”。[M2][M3][P1]

**未证明之处**：这些保护针对已有播放 pipeline；本轮未完整审计 MA queue/controller locks、Bridge executor、monitor、daemon reconnect 与所有暂停/恢复入口之间的取消传播，也未执行 Stop-during-wake。准备 wait 若放在持有整个播放命令锁的外层，可能延后 Stop，因此优先考虑已有 cancellation-managed session task，而非在 controller 加一个长 await。需要原型证明新准备 task 从开始就归 cancel 管理，旧 lease 的 finally 不影响 successor。这里是新契约必须验证的风险，不是已确认现场 bug。

## 6. MA 与 Go 的复用/改动范围

| 项目 | 候选最小面 | 不作出的承诺 |
| --- | --- | --- |
| MA 基线 | Sendspin provider 的 session-owned lease + pre-stream gate，复用现有 async play_media/start/cancel；核查所有恢复入口及 queue 的 start timeout/状态反馈 | 不承诺只改一行、现有 Cast future 可以直接复用，或无需覆盖其他控制入口；没有提出全 MA 重写。[M1–M4] |
| 外部 BT Bridge | 扩展现有 wake/status 与物理 lifecycle 为 correlated Prepare/Release；保逻辑端和 reconnection policy 一致，核验 daemon 路由而非泛化 success | 不要求先设计私有 Sendspin wire；需要的本地管理 transport/认证/epoch 细节待原型批准后决定。[B2–B8] |
| Go 候选应用 | 自己的播放权威/会话/intent 管理复用相同输出 adapter，先准备后供流；SDK 继续负责已有音频/协议能力 | 不是从零重写协议，也不是 SDK 已有完整应用层。[G1][G2] |
| Go SDK 是否需修改 | 本快照 `Server.Start():server.go:200–207` 起 streaming goroutine；`server_stream.go:13–48` ticker 持续调用 AudioSource.Read。`Group.SetPlaybackState():284–299` 改状态/发事件，不是这里的 source-consumption gate | AudioSource 是复用接缝候选，但不能未经验证就阻塞 Read 并宣称 Stop/时钟安全。可能需小范围 opt-in stream/intent gate 或 source 生命周期控制；本轮不保证零 SDK 改动。[G1][G2][G3] |
| Python server 候选 | `PushStream.prepare_audio/commit_audio` 提供明确调用方控制的供流边界，可在应用层先 await 相同契约 | 不把 PCM prepare 当输出 prepare，不声称它已提供队列/会话/BT 全应用。[P1] |

**MA management API 不是公共最小输出契约**：Bridge 的 MA group/name discovery 和 WebSocket monitor 由 MA_API_URL/TOKEN 条件启用（`ma_integration_service.py:54–69`）；`ma_monitor.send_player_cmd():1276–1315` 走 persistent monitor 或新认证 WS。缺失凭据时 fallback 返回 false。它可以是 MA 路线控制适配器，但 Go-only server 不提供这些 MA 接口，不能指望换 server 后 Bridge 的 MA UI/分组/volume fallback 原样可用。物理准备不应以“MA 管理端在线”作为其通用 ready 定义；管理反馈、重新发起队列等依赖需另行分层。[B9][B10]

## 7. 共享栈与发现副作用边界

- **共享 Pulse/PipeWire**：daemon 使用 per-process `PULSE_SINK`（BridgeDaemon `7–9`）；`amove_pid_sink_inputs():287–326` 按 PID 选 stream，但也设置 unity stream gain。函数注释明确默认 sink 自动路由可能改变重连后的去向；`monitor._correct_other_devices_routing():77–83` 会在一台设备重连后校正**其他运行 player**，原因是 module-rescue-streams。card profile cycle 也改变共享音频栈对象。隔离容器不等于隔离音频权限。[B4][B11][B7]
- **全局 default sink**：本轮审阅的上述路径证明 per-daemon 路由和共享救援/纠正影响，不足以证明该 revision 的每次操作都改了全局 default，也不足以证明全局 default 从不变化。未来观察必须同时记录 default、目标 sink-input 及无关应用路由，而不能仅看 PULSE_SINK 环境变量。[B11]
- **mDNS**：Bridge `bridge_mdns.py:31,179–188` 发布 `_sendspin-bridge._tcp.local.` 管理发现；此记录不要与播放器/服务器的 Sendspin 发现混同。Go `ServerConfigFile:23,26` 有 NoMDNS/DiscoverClients，不能默认启动候选无网络发现副作用。新身份/监听端口/服务公告、自动发现/连接必须显式受控，不能与原有逻辑播放器抢同一 ID。[B12][G4]

下一轮隔离应以一个批准的独占窗口、一份现有基线记录和单一播放权威为前提；候选默认禁用非必要发现/自动连接，不能并行驱动同一个 adapter、speaker 或共享 Pulse 的默认路由。此段是实验要求，不是执行授权。

## 8. 下一轮 HITL/原型观察项（本轮均未执行）

1. **先锁版本**：确认维护窗口与实际安装包、候选源码、发现身份；已 exited 的基线由人决定是否/何时恢复，不由研究代理启动。
2. **分别观察 cold start / 自动 standby / 手动 Release / power-save**：能否在物理断开时仍接受 intent；Prepare 能否独立于 stream/start 完成；如需保持逻辑 daemon，核实改变启动政策的实际面。
3. **一条相关时间线**：intent accepted、prepare begin/ready、link/sink/backend/routing、first content read/commit、首个声音、失败/取消/释放。不要用日志 playing true 替代声学可听，也不要先报固定丢秒数。
4. **首段门槛**：由人批准合适的可识别首段素材和记录方法；验证没有 ready 前静默消耗内容，也没有连接后首段截断。必要的 backend/功放预热是否能在不消费音乐内容时完成，需实机回答。
5. **取消矩阵**：Prepare 中 Stop、15 秒超时后迟到 Connect、切歌/再 Play、断线恢复；确认旧 ready/reanchor/cleanup 不能恢复旧 intent 或夺走新 lease，同时 Stop 反馈不被长锁阻塞。
6. **释放与稳定性**：无内容后 idle release，逻辑可控、无不必要主动重连；Pause/Next 是否保持同一 lease，断线不是曲终。
7. **共享副作用**：记录前后 default sink、rescue 导流、无关 app/player、card profile、sink gain 与 mDNS/自动发现，严格串行独占目标设备。
8. **MA management API 缺失/不通**：物理 Prepare 的完成是否独立；分别标明丢失哪些 MA 元数据/分组/控制功能，不能把单次音频互通推成整个应用替代。

只有这些证据出现后，才能由人决定候选路线、实现范围和预算。当前源码研究不宣布首音/Stop/待机问题已解决。

## 9. 固定公共来源索引

以下 URL 均指固定公开 commit（版本核对 API 除外），行号为读取 raw 后从 1 计数的源码行号。

- [V1] [MA 2.10.4 commit](https://api.github.com/repos/music-assistant/server/commits/2.10.4)。
- [V2] [Bridge v2.75.0 commit](https://api.github.com/repos/trudenboy/sendspin-bt-bridge/commits/v2.75.0)；[OCI revision → release tag compare](https://api.github.com/repos/trudenboy/sendspin-bt-bridge/compare/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad...v2.75.0)。
- [V3] [aiosendspin 6.1.1 commit](https://api.github.com/repos/Sendspin/aiosendspin/commits/6.1.1)。
- [V4] [aiosendspin 9.1.1 commit](https://api.github.com/repos/Sendspin/aiosendspin/commits/9.1.1)。
- [V5] [sendspin 7.5.0 source metadata](https://pypi.org/pypi/sendspin/7.5.0/json)；[tag commit](https://api.github.com/repos/Sendspin-Protocol/sendspin/commits/7.5.0)。
- [B0] [Bridge requirements.txt](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/requirements.txt#L14)。
- [B1] [Bridge device_activation.py:438–450](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/src/sendspin_bridge/services/bluetooth/device_activation.py#L438)。
- [B2] [Bridge api_bt.py:270–292](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/src/sendspin_bridge/web/routes/api_bt.py#L270)。
- [B3] [Bridge client.py:1119–1256](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/src/sendspin_bridge/bridge/client.py#L1119)。
- [B4] [Bridge bridge_daemon.py](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/src/sendspin_bridge/services/ipc/bridge_daemon.py#L173)，含 per-process routing、lead/volume 初始化及 `435–449` stream 事件。
- [B5] [Bridge client.py:2542–2615](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/src/sendspin_bridge/bridge/client.py#L2542)。
- [B6] [Bridge client.py:1886–1903](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/src/sendspin_bridge/bridge/client.py#L1886)。
- [B7] [Bridge bluetooth/monitor.py](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/src/sendspin_bridge/bluetooth/monitor.py#L77)，尤其 `189–191,491–500,533–541` standby/reconnect 条件。
- [B8] [Bridge client.py:2666–2719](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/src/sendspin_bridge/bridge/client.py#L2666)。
- [B9] [Bridge ma_integration_service.py:33–69](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/src/sendspin_bridge/services/music_assistant/ma_integration_service.py#L33)。
- [B10] [Bridge ma_monitor.py:1265–1318](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/src/sendspin_bridge/services/music_assistant/ma_monitor.py#L1265)。
- [B11] [Bridge pulse.py:287–326](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/src/sendspin_bridge/services/audio/pulse.py#L287)；`391–416` card profile cycle。
- [B12] [Bridge bridge_mdns.py](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/src/sendspin_bridge/services/ipc/bridge_mdns.py#L31)。
- [B13] [Bridge OCI source version constant:50](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/src/sendspin_bridge/config/__init__.py#L50)。
- [M6] [MA provider.py:406–470](https://github.com/music-assistant/server/blob/e30a4974ba951f38e21bea8d502af3b903df992c/music_assistant/providers/sendspin/provider.py#L406)。
- [D1] [sendspin 7.5.0 daemon.py:441–457](https://github.com/Sendspin-Protocol/sendspin/blob/22b9ff308afedd86e51ea1b88ededa574c9244e5/sendspin/daemon/daemon.py#L441)。
- [M0] [MA Sendspin manifest.json:10](https://github.com/music-assistant/server/blob/e30a4974ba951f38e21bea8d502af3b903df992c/music_assistant/providers/sendspin/manifest.json#L10)。
- [M1] [MA models/player.py:945–977](https://github.com/music-assistant/server/blob/e30a4974ba951f38e21bea8d502af3b903df992c/music_assistant/models/player.py#L945)。
- [M2] [MA Sendspin player.py:1402–1473](https://github.com/music-assistant/server/blob/e30a4974ba951f38e21bea8d502af3b903df992c/music_assistant/providers/sendspin/player.py#L1402)。
- [M3] [MA Sendspin playback.py:405–461](https://github.com/music-assistant/server/blob/e30a4974ba951f38e21bea8d502af3b903df992c/music_assistant/providers/sendspin/playback.py#L405)；[739–915](https://github.com/music-assistant/server/blob/e30a4974ba951f38e21bea8d502af3b903df992c/music_assistant/providers/sendspin/playback.py#L739)。
- [M4] [MA players/controller.py](https://github.com/music-assistant/server/blob/e30a4974ba951f38e21bea8d502af3b903df992c/music_assistant/controllers/players/controller.py#L4347)，另见 `454–461,3040–3042` availability 检查。
- [M5] [MA Sendspin bridge_role.py:237–267](https://github.com/music-assistant/server/blob/e30a4974ba951f38e21bea8d502af3b903df992c/music_assistant/providers/sendspin/bridge_role.py#L237)。
- [P1] [aiosendspin 9.1.1 push_stream.py:928–1166](https://github.com/Sendspin/aiosendspin/blob/5c024b42893bc6e372f6c87bb8504552051daf24/aiosendspin/server/push_stream.py#L928)。
- [P2] [aiosendspin 9.1.1 group.py:177–189](https://github.com/Sendspin/aiosendspin/blob/5c024b42893bc6e372f6c87bb8504552051daf24/aiosendspin/server/group.py#L177)。
- [P3] [aiosendspin 9.1.1 roles/base.py:385–403](https://github.com/Sendspin/aiosendspin/blob/5c024b42893bc6e372f6c87bb8504552051daf24/aiosendspin/server/roles/base.py#L385)。
- [P4] [aiosendspin 6.1.1 client.py:648–656](https://github.com/Sendspin/aiosendspin/blob/af9dbc8b625bffbee99d7a67712c0e3b4c44147d/aiosendspin/client/client.py#L648)；`1238–1243` callback 派发。
- [P5] [aiosendspin 9.1.1 player/v1.py:272–340](https://github.com/Sendspin/aiosendspin/blob/5c024b42893bc6e372f6c87bb8504552051daf24/aiosendspin/server/roles/player/v1.py#L272)。
- [G1] [Go SDK server.go](https://github.com/Sendspin/sendspin-go/blob/c61a40513249b22de9a5077b0acd1b4682a0b697/pkg/sendspin/server.go#L150)。
- [G2] [Go SDK server_stream.go:13–48](https://github.com/Sendspin/sendspin-go/blob/c61a40513249b22de9a5077b0acd1b4682a0b697/pkg/sendspin/server_stream.go#L13)。
- [G3] [Go SDK group.go:284–299](https://github.com/Sendspin/sendspin-go/blob/c61a40513249b22de9a5077b0acd1b4682a0b697/pkg/sendspin/group.go#L284)。
- [G4] [Go SDK config_server.go:18–28](https://github.com/Sendspin/sendspin-go/blob/c61a40513249b22de9a5077b0acd1b4682a0b697/pkg/sendspin/config_server.go#L18)。
- [S1] [规范 messaging.md](https://github.com/Sendspin/spec/blob/ee8aad9660fc57345e726a888486f47f66a41f97/messaging.md#L114)，另见 `295–307,390–403`。
- [S2] [规范 player/v1.md:47–65](https://github.com/Sendspin/spec/blob/ee8aad9660fc57345e726a888486f47f66a41f97/roles/player/v1.md#L47)；[145–160](https://github.com/Sendspin/spec/blob/ee8aad9660fc57345e726a888486f47f66a41f97/roles/player/v1.md#L145)。
