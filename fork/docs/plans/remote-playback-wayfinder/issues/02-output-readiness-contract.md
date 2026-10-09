# 确定输出准备与释放的最小协调位置

Type: research
Label: wayfinder:research
Mode: AFK
Status: resolved
Assignee: LINSUISHENG034
Parent: ../map.md
Blocked by: none
Research branch: research/remote-playback-output-contract
Research asset: ../research/output-readiness-contract.md

## Question

为实现「输出真正就绪后才消耗可听音频、停止不复活旧请求、待机不反复重连」，最小协调 Interface 应放在哪里，哪些事实可以从源码确定，哪些必须由真实设备原型回答？

- 追踪目标 Bridge 的启动注册、自动待机、唤醒触发、输出重定向、重连抑制及管理接口依赖；区分自动 standby 和停止 daemon 的手动 Release。
- 核对 MA 发流/时间线和 SDK 的最小接入位置，辨别既有 activation/准备钩子是否可用，不先假定必须修改整个 MA 或另造协议。
- 核对 available、lead-time、时钟同步与实际物理输出就绪的关系；识别循环等待和首段静默消耗风险。
- 说明停止/切歌/断线的取消与旧结果判定需要什么契约，但不得把未审计或未复现的 stop-cancel 风险写成已确认 bug。
- 记录共享音频栈的全局默认输出、rescue 和 mDNS/自动发现等副作用边界，以及未来实验的隔离要求。
- 给出 MA 基线和 Go 候选可共用的最小准备/释放模型、需要改动的项目范围、未经实测的部分及原型观察项；不实现或操作设备。

研究报告持有源码与规范证据；Answer 选择最值得验证的协调位置，而不宣布生产问题已经解决。

## Answer

**决策（进入验证的候选，不是生产选型）**：最小协调位置放在**播放权威的 session → 输出 adapter 的 pre-stream 边界**。MA 基线优先扩展现有 Sendspin `PlaybackSession`：`SendspinPlayer.play_media()` 接受 intent，受 `cancel()` 管理的 `_run_playback()` task 在 `_create_push_stream()` 和音乐内容读取/提交前 await 输出准备。Bridge 集中执行物理 Prepare/Release，播放权威持有 session-owned lease、intent generation 与 Stop/超时撤销权；切歌复用有效 lease，不每次重连。Go 候选复用同一输出契约，不把 MA management API 当共同底层。

**最小 Interface 建议**：`Prepare(cancel_context, output_id, intent_generation, deadline) -> ReadyLease | failure` 与 `Release(cancel_context, lease, reason)`。lease 必须关联当前输出 epoch；Stop/超时先撤销旧 intent/提交许可，迟到 ready/reanchor/cleanup 不能自动播放旧内容，也不能释放 successor 的使用权。transport、具体代码结构与实现量未承诺，仍须另行批准原型。

**关键证据与边界**：目标 MA 有 async provider 入口和 session cancellation，但 `start()` 只是创建后台 task。现有 `on_protocol_playback` 在 play_media 之后、Cast-ready 在 start 后且仅 Cast、Bridge CLI hook-start 是 fire-and-forget、SDK `prepare_audio` 是 PCM 准备；这些都不是可直接复用的 BT output-ready ACK。Bridge wake API success 表示 waking，reroute true 也可包含无存活 stream 的 reconnect 请求，不能当 ready。available/lead/clock 不代替物理准备；available false 与仅 stream-start 唤醒组合有设计循环风险，未判定现场已出现。

**适用范围**：Bridge OCI `0f2cd9ab…`（源码 VERSION `2.75.0-rc.6`，镜像 tag `2.75.0`，公开 v2.75.0 tag `126e766d…`）与 MA `2.10.4/e30a4974…`；依赖声明分别为 Bridge aiosendspin `6.1.1`/sendspin `7.5.0`、MA SDK `9.1.1`，不是实际安装包证明。MA 默认存在 legacy/noncompliant 许可路径，不能按最新 Noise 规范否定旧 Bridge。MA/Bridge 当前 exited（协调者观察），互通未实测。

**尚待 HITL**：冷启动下逻辑目标可达性、无音乐内容的 backend/音箱预热、首段声学完整性、Prepare 中 Stop/超时后迟到 Connect 的取消闭环、所有 resume/skip 入口与锁/timeout、共享 Pulse default/rescue 路由及 mDNS 副作用。源码证明有静默消费路径、没有前置首音保障，不证明必丢固定秒数；现有 Stop 保护明确存在，未审计/未复现部分不登记为已确认 bug。真实音箱试验和最终路线由人决定。

源码、规范 facts、推断、未知、改动项目范围及观察项见命名研究报告：[输出准备与释放契约：源码研究](../research/output-readiness-contract.md)。
