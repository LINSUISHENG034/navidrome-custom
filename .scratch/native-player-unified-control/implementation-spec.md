# 原生播放器统一控制实施规格

Status: ready-for-agent

## Problem Statement

用户目前在浏览器播放与服务端播放之间面对两套播放器界面、两套队列交互和分散的命令处理。相同的播放、暂停、跳曲、队列编辑和输出切换操作，可能因入口或输出目标不同而产生不同结果。现有原型已经证明统一架构的部分候选契约，但生产宿主仍使用旧路径，不能把原型或模拟远端的成功当作用户已经获得统一能力。

用户需要真正复用原生 Player UI，而不是再维护一个外观相似的独立远端面板。服务端播放时，浏览器应只负责展示和控制，不应创建音频引擎或请求音频流；关闭网页后，Playback Mode 和 Natural End 推进仍应由服务端执行。

当前共同行为还受到以下问题影响：

- Queue Item 缺少贯穿 UI、协议与服务端的稳定身份，重复 MediaFile 无法始终被准确定位。
- PREPARING 中的起播意图、迟到的媒体事件、旧 HTTP 响应和旧 SSE 快照可能破坏新 Playback Context。
- Output Migration 涉及目标准入、旧端停止、现场导入、音量和启动等多个阶段；超时或响应丢失时，用户无法可靠区分未执行、已执行和结果未知。
- 目标已有 Playback Control Authority 或 Playback Context 时，破坏性接管可能误中断他人播放；旧端停止失败也可能留下两个输出之间的不安全现场。
- 浏览器规范音量与服务端设备增益混用会造成跨设备漂移，重复快照和重连可能累积映射误差。
- 刷新、多标签页、失权和混合版本部署需要明确的控制权交接与 fail-closed 行为，不能依靠共享 ClientID 或盲目重试维持假象。
- ReplayGain、转码预取、歌词、标题、通知、MediaSession、Scrobble 和 keepalive 仍依附旧宿主；只替换可见 UI 会丢失或重复这些副作用。

本规格将既有实施方案转化为生产实施与验收契约。它授权按门槛实施和测试，不代表这些能力已经实现，也不自动授权生产部署、服务重启、硬件操作、外部 Scrobbler 验证或旧路径删除。

## Solution

交付一个由原生 Player UI、统一 PlaybackControlModule、BrowserAudioAdapter 和生产 RemoteAudioAdapter 构成的统一控制系统：

- 同一原生控制条、进度、播放模式、歌词和 Playback Queue 面板同时服务浏览器播放与服务端播放。
- 所有用户操作作为显式意图单次进入 Module；所有适配器事件作为权威状态单向回填，回填不再产生命令。
- BrowserAudioAdapter 独占真实浏览器媒体元素；RemoteAudioAdapter 只使用既有 Jukebox 控制、快照和事件通道，不创建浏览器音频引擎。
- 服务端持有服务端播放的 Playback Queue、Playback Mode、Natural End、Playback Control Authority、BindingGeneration、状态顺序和播放实例权威。
- Output Migration 由 Module 作为唯一跨输出协调器，按非破坏预留、旧端停止确认、现场提交、音量确认和条件启动执行；服务端负责其自身状态变更的执行侧鉴权与原子提交。
- 统一规范音量 V、感官滑块 U 与设备增益 G，防止跨输出和重连漂移。
- 所有受保护写入具有稳定 operationId、代次校验和可查询结果；UNKNOWN 先对账，不盲目重放。
- 刷新使用每文档新 ClientID 和单次 handoff token；短时断线可重附，服务重启则不承诺恢复并停止自动写入。
- 宿主副作用逐项迁移到唯一责任方，通过配置和单标签页覆盖灰度；只有完整替代证据和生产验收通过后才删除旧路径。
- 在线有效控制者的本地与远端 Scrobble 由同一协调器负责；无人附着时的服务端独立上报仍明确保留为后续缺口。

## User Stories

### 统一界面与输入

1. 作为用户，我希望浏览器播放与服务端播放使用同一套原生控制器，以便不需要学习两套操作方式。
2. 作为用户，我希望两种输出共用原生 Playback Queue 面板，以便保留熟悉的查看、拖拽、删除和点选体验。
3. 作为用户，我希望播放、暂停、seek、上一首和下一首在两端语义一致，以便操作结果可以预期。
4. 作为用户，我希望列表操作、播放器按钮、全局快捷键和 MediaSession 遵循同一规则，以便入口不同不会改变业务行为。
5. 作为用户，我希望一次点击或按键只产生一次用户意图，以便不会因事件冒泡或状态回写而重复播放、跳曲或切换模式。
6. 作为用户，我希望 PREPARING 时起播中的播放／暂停按钮提供暂停动作，以便慢网加载期间可以取消起播而不是重复签发 play。
7. 作为用户，我希望拖动进度条时界面流畅响应、释放时才提交一次 seek，以便拖动不会产生命令风暴。
8. 作为用户，我希望音量滑块和键盘调节具有一致的感官步进，以便不同输出上的调节体验一致。
9. 作为用户，我希望加载、播放、暂停和失败状态清晰可区分，以便请求已发送不会被误报为正在播放。
10. 作为用户，我希望歌词、标题、当前项高亮和进度来自同一权威状态，以便服务端播放没有浏览器音频事件时界面仍正确工作。
11. 作为用户，我希望位置显示的平滑插值不触发命令或状态序列变化，以便仅浏览界面不会扰动播放。
12. 作为观察者，我希望失去控制权后仍能查看权威状态并看到明确提示，以便知道为何不能继续写入。

### Playback Queue

13. 作为用户，我希望同一 MediaFile 重复入队后每次出现都是独立 Queue Item，以便准确操作指定实例。
14. 作为用户，我希望删除重复曲目中的某一项只删除该项，以便不会误删同曲目的其他实例。
15. 作为用户，我希望点选重复曲目中的某一项跳到该实例，以便不会总是跳到第一个相同 MediaFile。
16. 作为用户，我希望拖拽基于稳定 Queue Item 和相对锚点，以便命令含义不依赖容易漂移的数组下标。
17. 作为用户，我希望基于旧队列视图的拖拽被明确拒绝并刷新，以便并发编辑不会被覆盖。
18. 作为用户，我希望移动当前 Queue Item 不重启媒体、不重置位置且不改变播放实例，以便重排不会打断聆听。
19. 作为用户，我希望暂停时移动或删除队列项不会自动起播，以便整理队列不会突然出声。
20. 作为用户，我希望删除当前项后优先选择后继，以便播放按可预期的顺序继续。
21. 作为用户，我希望删除当前末项后选择剩余末项，以便删除操作不会意外循环到队首。
22. 作为用户，我希望清空队列后停止并清除当前项和播放实例，以便界面与实际输出一致。
23. 作为用户，我希望自引用或已经就位的移动作为幂等 no-op，以便重复操作不会增加队列版本或重载媒体。
24. 作为用户，我希望源项或目标锚点已消失时收到明确失败和最新快照，以便可以基于真实现场重新操作。
25. 作为用户，我希望队列写命令响应丢失后可以查询原操作结果，以便不会重复移动、删除或添加。
26. 作为维护者，我希望旧 ID-only 全量替换原子升级实例身份并按旧 currentIndex 保留当前项，以便重复 MediaFile 不会导致错误恢复。
27. 作为维护者，我希望新旧移动字段同时出现时被拒绝，以便兼容路径不会产生歧义或绕过保护。

### Playback Mode 与持续播放

28. 作为用户，我希望顺序、列表循环、单曲循环和随机播放是四个互斥模式，以便界面和协议保持简单一致。
29. 作为用户，我希望未知播放模式被明确拒绝，以便错误配置不会被静默改成其他模式。
30. 作为用户，我希望手动下一首不被单曲循环拦截，以便主动操作始终能离开当前项。
31. 作为用户，我希望顺序模式在末项 Natural End 后停止并保留末项，以便队列内容不会意外消失或回卷。
32. 作为用户，我希望列表循环在末项 Natural End 后回到首项，以便列表持续播放。
33. 作为用户，我希望单曲循环在 Natural End 后从头重播当前项，以便连续聆听同一 Queue Item。
34. 作为用户，我希望随机播放只改变选曲决策而不物理重排可见队列，以便界面顺序保持稳定。
35. 作为用户，我希望多项队列随机选曲严格排除当前项，以便不会立即重复同一项。
36. 作为用户，我希望单项队列在循环和随机模式下可以重播唯一项，以便边界行为明确且不会卡死。
37. 作为用户，我希望上一首在当前位置超过一秒时先回到当前曲开头，以便保留熟悉的原生行为。
38. 作为用户，我希望上一首在一秒以内按当前模式选择其他项，以便非随机与随机行为一致可预期。
39. 作为用户，我希望绝对点选 Queue Item 不参与随机选择，以便点选始终准确。
40. 作为用户，我希望输出迁移延续当前 Playback Mode，以便切换设备不会改变后续选曲策略。
41. 作为用户，我希望关闭网页后服务端仍执行循环、随机和自动下一首，以便网页不是服务端播放引擎。
42. 作为用户，我希望真实启动失败不会被显示为新一轮成功播放，以便错误不会被自动推进掩盖。

### Output Migration 与故障安全

43. 作为用户，我希望切换 Output Target 时延续 Playback Queue、当前 Queue Item、位置、播放／暂停意图、音量和模式，以便无需重建 Playback Context。
44. 作为用户，我希望 PREPARING 中已接受的起播意图也能跨迁移保留，以便慢网加载不会被误判为暂停而丢弃。
45. 作为用户，我希望迁移期间显式暂停或停止立即取消后续自动启动，以便最后意图优先。
46. 作为用户，我希望暂停后重新点击播放可以按新的意图序号生效，以便取消不会永久锁死播放。
47. 作为用户，我希望迁移期间普通命令不会穿透到尚未就绪的目标，以便现场导入和音量门槛不会被绕过。
48. 作为用户，我希望过期 seek、队列编辑和旧曲目命令在迁移后被丢弃，以便旧操作不会作用于新现场。
49. 作为用户，我希望目标未就绪时有明确且有界的失败，以便界面不会无限等待挂载或网络。
50. 作为用户，我希望浏览器目标只有在真实元素完成挂载后才进入后续阶段，以便不会发生脱离 DOM 的隐藏起播。
51. 作为用户，我希望旧输出明确停止前目标绝不启动，以便受控迁移不会新增并行音频输出。
52. 作为用户，我希望目标音量未明确应用前不启动，以便不会以错误增益突然出声。
53. 作为用户，我希望只有迁移前接受的起播意图仍为 play 时才启动目标，以便暂停现场迁移后保持暂停。
54. 作为用户，我希望目标已有控制者或播放现场时先看到确认，以便不会误中断其他播放。
55. 作为用户，我希望确认后目标现场已变化时重新确认，以便系统不会覆盖第三方刚产生的新现场。
56. 作为用户，我希望预留目标不会立即驱逐原控制者或清空其队列，以便旧端停止失败时目标仍可继续使用。
57. 作为用户，我希望旧端停止失败或 UNKNOWN 时迁移终止且目标不启动，以便故障不会被当成成功继续。
58. 作为用户，我希望提交后的音量或启动失败保留可见的已迁移失败现场，以便真实状态可诊断且不会偷偷恢复旧输出。
59. 作为用户，我希望迁移失败不会自动恢复浏览器出声，以便故障处理不会制造意外播放。
60. 作为用户，我希望取消或超时不会无限挂起，并能区分未执行、已应用、失败和未知，以便知道下一步能否安全重试。
61. 作为用户，我希望响应丢失后系统先查询原 operationId，以便不会重复执行或从头重放整个迁移。
62. 作为用户，我希望新迁移不会隐式覆盖正在进行的迁移，以便两个事务不会交叉修改现场。
63. 作为维护者，我希望预留释放和预留状态也可对账，以便释放响应丢失时不会错误假定目标已释放。
64. 作为维护者，我希望清理只撤销当前 migration 自己仍持有的预留，以便不会回滚更新 owner 或更新代次的现场。

### 控制权、恢复与兼容

65. 作为用户，我希望共享服务端输出同时只有一个有效控制者，以便多个标签页不会相互覆盖命令。
66. 作为观察者，我希望只有显式 Control Takeover 才能获得控制权，以便附着观察不等于接管。
67. 作为原控制者，我希望 recovering 状态下可以按原身份重附，以便短时断线不会丢失会话。
68. 作为用户，我希望第三方在 recovering 状态下也必须显式接管，以便恢复态不会成为鉴权漏洞。
69. 作为用户，我希望旧 owner 和旧 BindingGeneration 的写入均被拒绝，以便迟到命令不会修改新现场。
70. 作为用户，我希望同一 owner 在旧设备现场发出的迟到命令也被拒绝，以便身份相同不能绕过代次保护。
71. 作为用户，我希望刷新页面后播放位置、队列、音量、模式和播放实例保持连续，以便刷新不像重新播放。
72. 作为用户，我希望刷新后的新文档通过单次 token 接管原文档控制权，以便不需要共享旧 ClientID。
73. 作为用户，我希望克隆标签页竞争 handoff 时只有一个成功，以便其他标签页保持只读而不会争抢控制。
74. 作为用户，我希望无有效 token 的新标签页即使知道 Session ID 也不能获得控制权，以便会话发现不等于授权。
75. 作为用户，我希望短时断线重连后先重新握手并对账 UNKNOWN 命令，以便恢复不会重复写入。
76. 作为用户，我希望失权或 403 后在途写入被取消并转为只读，以便旧控制者不会持续重试。
77. 作为用户，我希望服务端重启后不会盲目自动起播或重放旧命令，以便无法恢复权威状态时保持 fail-closed。
78. 作为维护者，我希望 HTTP 快照、SSE 和重连首包遵守同一代次与状态顺序，以便旧状态不会覆盖新状态。
79. 作为维护者，我希望 SSE 被视为可丢失的失效通知而非持久日志，以便重连始终重新取得完整权威快照。
80. 作为维护者，我希望新客户端明确探测服务端安全能力，以便旧服务端忽略新字段时不会被误判为支持。
81. 作为维护者，我希望旧客户端不能写入已受保护的服务端现场，以便兼容不会削弱新代次防护。
82. 作为维护者，我希望后端回退前停止新写入、对账在途操作并重新握手，以便 UI 开关不会成为伪回退。

### 音量、宿主副作用与 Scrobble

83. 作为用户，我希望普通输出切换到蓝牙输出后再切回仍保持相同规范音量，以便滑块不会不可逆漂移。
84. 作为用户，我希望重复增益快照和多次重连不累积曲线映射，以便音量不会逐渐趋近最大值。
85. 作为用户，我希望设备外部音量变化能被识别并回填一次，以便幂等处理不会吞掉真实变化。
86. 作为用户，我希望 mute 与音量值相互独立，以便静音和恢复不会破坏最后非零音量。
87. 作为用户，我希望零音量、接近零、最大音量和非法输入都有明确行为，以便边界不会导致增益突跳或异常状态。
88. 作为用户，我希望未知 volumeProfile 在目标启动前失败，以便设备提示不会静默选择错误曲线。
89. 作为用户，我希望 ReplayGain 只作用于浏览器真实媒体元素，以便服务端播放不会创建隐藏 AudioContext。
90. 作为用户，我希望转码预取和错误失效在浏览器播放中继续工作，以便统一 UI 不损害既有能力。
91. 作为用户，我希望通知、标题、歌词、ReactGA 和 radio 特判不因统一切换而丢失或重复，以便外围体验保持稳定。
92. 作为用户，我希望本地与远端在线播放都发送正确的 starting、playing、paused 和 stopped 生命周期，以便播放统计完整。
93. 作为用户，我希望暂停恢复和输出迁移不产生新的播放实例，以便 Scrobble 不会重复开始或停止。
94. 作为用户，我希望 Natural End 的单曲循环结束旧实例并开始新实例，以便统计能区分两次实际播放。
95. 作为用户，我希望刷新 handoff 和重复 SSE 不重复上报生命周期，以便恢复不会制造重复记录。
96. 作为用户，我希望只有当前有效控制者上报，以便观察者和失权文档不会双写。
97. 作为用户，我希望本地 pagehide 终止播放时发送 stopped keepalive，以便最后状态能被保存。
98. 作为用户，我希望远端 pagehide 但设备继续播放时不伪造 stopped，以便统计不违背真实播放状态。
99. 作为用户，我希望上报失败不阻塞播放并以有界方式重试，以便统计故障不会损害核心功能。
100. 作为维护者，我希望 50%／240 秒阈值仍由服务端 PlayTracker 裁定，以便客户端不会形成第二套统计规则。

### 交付与运维

101. 作为维护者，我希望浏览器与服务端消费同一组行为案例，以便发现语义漂移而不是各自通过不同规则。
102. 作为维护者，我希望测试同时断言禁止副作用和过程不变量，以便最终状态相同不会掩盖中途出声或重载。
103. 作为维护者，我希望模拟、真实 Chrome、真实 MPV 和物理蓝牙证据分级记录，以便低层证据不会冒领生产验收。
104. 作为维护者，我希望保留可复验的源码、锁文件、构建、浏览器和配置证据，以便测试结果可以追踪到同一产物。
105. 作为项目负责人，我希望九个实施任务具有真实依赖和阶段门禁，以便未通过安全契约时不会提前集成或切网。
106. 作为项目负责人，我希望旧路径在替代证据充分前保留、之后彻底退役，以便回退可行且不会长期维护两套业务规则。
107. 作为运维人员，我希望配置默认值和单标签页覆盖的生效条件被如实说明，以便不会把重新加载需求误认为即时全量回退。
108. 作为项目负责人，我希望无人附着 Scrobble、外部投递和硬件验收的缺口明确列出，以便实施完成不会被误报为所有能力均已验证。

## Implementation Decisions

### 1. 架构与唯一数据流

1. UnifiedNativePlayer 是原生 Player UI 的外部受控模式，不负责网络协议、Session 租约、设备状态机或独立播放规则。
2. PlaybackControlModule 是唯一用户意图收敛点和跨输出状态协调器。控件、列表、快捷键和 MediaSession 不得绕过 Module 直接操作 Adapter。
3. Module 对 UI 发布权威状态、准备中、错误、控制权、连接状态和迁移状态；Adapter 对 Module 发布归一化执行结果与权威快照。
4. BrowserAudioAdapter 与 RemoteAudioAdapter 实现共同命令契约，但只有当前代次、当前输出绑定的 Adapter 可以推进现场。浏览器专用元素挂载和远端专用租约不进入共享接口。
5. Module 是唯一 Output Migration 跨输出事务协调器。服务端是其自身预留、鉴权、现场提交和状态广播的执行侧权威，不另跑一套与 Module 竞争的迁移阶段链。
6. 既有服务端设备切换能力只通过兼容入口复用同一组执行原语；统一路径不得同时调用旧 SwitchDevice 事务和新迁移事务。
7. Adapter 状态回填只更新 Module 和 UI，绝不反向触发 play、pause、seek、队列或音量命令。必要的对账读操作不得转化为写命令。
8. 原生依赖的默认独立用法保持兼容；本项目宿主显式使用受控模式，不通过静音媒体元素、假 Audio 或第二套 renderer 模拟统一。

### 2. Module 状态、意图与代次

1. 标准状态为 IDLE、PREPARING、PLAYING、PAUSED、ERROR；另有可见的连接、失权和 UNKNOWN 信息，不用 PREPARING 掩盖永久失败。
2. `acceptedIntent` 独立记录最后接受的 play、pause 或空意图，并附单调意图序号。PREPARING 不抹除已接受的 play，包括 Natural End 后的下一首准备。
3. PREPARING 且 acceptedIntent 为 play 时，播放／暂停控件呈现可取消起播的暂停动作并调用 requestPause；不得因状态尚非 PLAYING 再调用 requestPlay。acceptedIntent 为 pause 时可表达新的 play。
4. 显式 pause 或 stop 立即中和起播意图；后续 play 以更高序号重新表达。迁移最终阶段只服从届时仍有效的最后意图。
5. 每次 Adapter 切换或 Output Migration 增加 currentEpoch。Promise、媒体事件、HTTP 响应和事件流回调必须同时属于当前 epoch 与当前 BindingGeneration；旧代次写结果只能用于对账，不能推进新现场。
6. 每个用户意图只接纳和签发一次。复合意图可包含契约规定的多步命令，但中间状态回填不得再次触发同一意图。
7. 所有等待具有整体 deadline、阶段 deadline 和取消传播。AbortSignal 只取消等待或传播取消请求，不证明执行端未运行。
8. 迁移期间普通写意图被隔离并记录提交时的 queueVersion、playbackInstanceId 和意图序号。安全门槛后只结算仍适用于当前现场的意图；过期 seek、队列编辑和旧曲目命令丢弃并读取最新快照。
9. 新迁移不得隐式取代进行中的迁移。调用方必须显式取消旧迁移，并等待其达到终态或完成 UNKNOWN 对账后才能继续。
10. 错误必须统一进入终端处理并保留可诊断原因。原生 Promise 异常、IPC 错误、异步执行失败和超时不得绕过统一错误状态。

### 3. Adapter 命令、结果与预留契约

1. 每个受保护命令携带 epoch、稳定 operationId、取消信号；远端写还携带 Session、ClientID 和 BindingGeneration。
2. 命令结果为 APPLIED、NOT_EXECUTED、FAILED 或 UNKNOWN，并尽可能携带 BindingGeneration 和 stateSequence。HTTP 成功、Promise 结束或内存字段变化本身不等于真实输出已确认。
3. 同一 operationId 的重试返回第一次确定结果，不得再次执行。结果丢失或超时后先 queryOperation 和最新权威快照；结果仍 UNKNOWN、已淘汰或服务已重启时保持 fail-closed。
4. 共同契约包含 play、pause、stop、seek、模式感知 skipNext／skipPrev、setVolume、setMute、setPlayMode、队列增删移清和绝对 Queue Item 跳转。UI 不得把 next／previous 换算成绝对下标。
5. mute 是共享权威状态中的独立布尔值，不得以破坏性的 setVolume(0) 代替。浏览器输出使用真实媒体 mute；服务端输出保存独立 mute 状态。静音期间调整 V 更新期望值但保持静音，解除静音后应用当前 V。
6. prepareForMigration 建立有界 Output Readiness 和非破坏 MigrationReservation；reserve、commit、release 均有稳定 migrationId 和派生 operationId。
7. releaseMigrationReservation 返回可查询的 CommandResult。另提供只读 reservation 查询，至少区分 ACTIVE、RELEASED、EXPIRED 和 NOT_FOUND，并验证其 migration 与 Session 归属。
8. 释放响应丢失时先查 release operation，再查 reservation。NOT_FOUND 不得在服务重启或身份不明时被解释为“已安全释放”；此时重新握手并放弃旧重试。
9. reservation 过期只释放预留，不停止目标播放，不替换 owner，也不清理目标 Playback Context。
10. BrowserAudioAdapter 保留真实 audio.play() Promise 拒绝、媒体错误、事件监听幂等和资源清理语义；不自动创建脱离 DOM 的 Audio。
11. RemoteAudioAdapter 封装能力探测、Session 租约、heartbeat、handoff、结果查询、预留、音量 profile 和状态排序；不得把这些职责泄漏到 UI 或 Module 的通用业务规则中。

### 4. 浏览器就绪与生产远端隔离

1. Module 等待浏览器目标前发布 pendingOutputTarget；宿主据此切换到同一个 UnifiedNativePlayer 的浏览器渲染分支，确保真实元素有明确挂载来源。
2. 已有有效元素时本地预留立即完成；未挂载时等待 attachElement，detach 立即使预留失效。默认可配置就绪看门狗为 3000ms。
3. 尚未发出底层命令的挂载超时可确定为 TARGET_NOT_READY；命令发出后的超时必须按 UNKNOWN 对账。迟到 attach 不得清空或重置已迁移现场。
4. 同元素同代次重复 attach 短路；deactivate 解除监听并清理媒体资源。清理期间 error、pause、ended 和 timeupdate 尾事件由 epoch 与资源代次守卫丢弃。
5. autoplay NotAllowedError 与解码 MediaError 归一化为可见错误；迁移路径始终进入统一失败处理，不伪造 PLAYING，不自动恢复旧输出。
6. 生产 RemoteAudioAdapter 在挂载、交互、重连、迁移和卸载全过程不得实例化 Audio、HTMLMediaElement 或 AudioContext，不得请求音频流。
7. 从浏览器切入服务端时，旧浏览器引擎可在停止与清理阶段短暂存在；必须单独确认旧输出停止、资源清理和回调失效，不能把旧引擎清理误计为远端创建引擎。
8. 元数据和封面请求不属于音频流，但转码预取、ReplayGain 或其他宿主 effect 不得借远端模式发起媒体请求。

### 5. Playback Mode 权威契约

规范值仅为 order、orderLoop、singleLoop、shufflePlay：

| 模式 | 手动下一首 | Natural End | 手动上一首 |
| --- | --- | --- | --- |
| order | 下一项；末项回首项 | 下一项；末项停止并保留末项 | 进度大于 1 秒先回卷，否则上一项；首项回卷末项 |
| orderLoop | 下一项；末项回首项 | 下一项；末项回首项继续播放 | 同 order |
| singleLoop | 强制下一项，不被单曲循环拦截 | 当前项从头重播并产生新播放实例 | 进度大于 1 秒先回卷，否则上一项 |
| shufflePlay | 从其他项中等概率选取 | 从其他项中等概率选取 | 进度大于 1 秒先回卷，否则随机选择其他项 |

1. 多项队列的随机候选严格排除当前 Queue Item；单项队列允许重播唯一项。随机只选择实例，不修改可见队列顺序，不引入历史栈或逻辑随机游标。
2. 空队列可保存模式但不执行选曲。skipToQueueItem 是绝对实例跳转，不参与模式选择。
3. 服务端持有服务端 Playback Mode，并在 Natural End 和手动命令中分别执行上表；浏览器不得通过 ended 补偿命令驱动服务端持续播放。
4. 模式写复用既有执行命令、用户、owner、设备范围和 BindingGeneration 防护，不增加无鉴权平行入口。
5. 未知模式写返回 400 INVALID_PLAY_MODE，不修改现场。展示不可信历史缓存时可回退 order，但此回退不得用于服务端写入或覆盖权威模式。
6. 生产随机源可注入确定性测试序列；测试不得通过物理洗牌替代随机选择。
7. 新曲目只有在真实输出启动后才进入 PLAYING。加载或 Unpause 失败进入可见错误，不伪报成功。

### 6. Queue Item 与移动协议

1. Queue Item ID 在入队时分配全局唯一 UUID，独立于 MediaFile ID 和数组位置；UI key、删除、点选、拖拽、协议和服务端快照均只以该 ID 定位实例。
2. 相对移动参数为 sourceItemId、targetItemId、before／after placement 和 expectedQueueVersion。新版本路径不发送绝对 toIndex。
3. 源与目标相同或源已处于目标相邻位置时返回 APPLIED no-op，不增加 queueVersion，不重载媒体。
4. 源缺失返回 QUEUE_ITEM_NOT_FOUND；目标缺失返回 TARGET_ITEM_NOT_FOUND；二者均附最新快照且不改变现场。
5. expectedQueueVersion 不匹配返回 409 QUEUE_VERSION_CONFLICT 和最新快照。相对锚点不意味着接受基于旧视图的拖拽。
6. 相对字段与任何 legacy from、to 或 toIndex 同时出现返回 400 MOVE_PROTOCOL_CONFLICT，不猜测调用者意图。
7. 移动当前项只更新其实例位置；currentQueueItemId、playbackInstanceId、媒体资源和位置保持不变。
8. Sortable 只负责乐观视觉预览。Drop 使用拖动开始时的稳定 ID 和目标锚点提交；冲突、失权和确定失败均以最新快照重绘，UNKNOWN 先对账。
9. 删除当前项优先后继，删除末项选择剩余末项，不触发循环；保留原播放／暂停意图。删除非当前项不得改变当前播放实例。
10. 清空队列停止输出并清除当前项、位置与 playbackInstanceId。
11. legacy ID-only 全量替换在一个原子操作中分配全新 Queue Item ID，并在重写前按原 currentIndex 映射当前项。受保护增量编辑缺少实例身份时明确拒绝。
12. 旧客户端兼容入口不得绕过 owner、BindingGeneration、operationId 或版本保护，也不得以全量替换伪装相对移动。

### 7. 统一音量、mute 与真实执行确认

1. Module 唯一持有规范音量 V，范围 0 到 1；UI 感官滑块 U 满足 U=sqrt(V)，用户输入满足 V=U²。键盘以 U 为步长。
2. 蓝牙现有候选 profile 以 V 表示为 G=V^0.15，逆映射为 V=G^(1/0.15)。该曲线在 V=0 连续，但不据此宣称物理响度一致或固定 dB 标准。
3. 显式 volumeProfile 优先于设备提示；未知 profile 在目标启动前返回 VOLUME_PROFILE_UNKNOWN。不得混用 isBluetooth、名称子串和不同曲线。
4. Adapter 保存本 BindingGeneration 最近确认的 V、G 和代次。相同权威 G 的重复快照复用已确认 V；只有超出明示硬件量化容差的外部变化才执行一次逆映射。
5. 容差只识别同一硬件值，不用于连续快照的自我累积映射，也不得吞掉真实外部音量变化。
6. NaN、Infinity 和越界输入在 Module 边界拒绝。V=0 不覆盖 lastNonZeroVolume；mute 由独立 setMute 和权威布尔状态处理。
7. 远端快照区分 DesiredGain 与最后确认 Gain。应用失败可保留新的期望值，但不得把期望值显示为已应用值。
8. 无活跃轨时只保存期望增益。新轨在首次 Unpause 前以初始增益创建并确认；加载阶段 SetVolume 失败必须清理待启动轨并返回错误。
9. 浏览器播放的媒体命令使用真实 Promise 和媒体事件确认；MPV 的 SetVolume、Pause 和 Unpause 通过既有有界 IPC 调用同步返回错误，不得只记录日志。
10. Output Migration 的旧端 stop 表示真实暂停输出并保留可恢复的源轨语义，不与关闭并销毁 ActiveTrack 的 Shutdown 混用。
11. 旧端 Pause 失败或 UNKNOWN 时不得越过停止门槛。目标 Gain 未明确应用时返回 TARGET_VOLUME_FAILED 或 TARGET_VOLUME_UNKNOWN，目标 play 调用为零。
12. 目标 Unpause 失败不得伪报 PLAYING。同步错误和异步 PlaybackEventFailure 按 operationId／playbackInstanceId 去重，不重复推进状态。
13. IPC 超时只表示结果 UNKNOWN；不得推断未设置、已停止或未启动。软件确认不代替真实 MPV、PipeWire 和物理蓝牙验收。

### 8. 服务端状态顺序与播放实例

1. Playback Device 持有 Playback Mode、Playback Queue、当前 Queue Item、当前 Track、DesiredGain、确认 Gain、mute、stateSequence 和 playbackInstanceId；读写遵守既有锁顺序。
2. 播放、暂停、seek、模式、当前项和其他语义变化在设备锁内递增 stateSequence；纯位置时钟插值不递增。
3. queueVersion 仅表示队列结构变化，不能替代 stateSequence。sceneRevision 用于目标现场指纹，也不能替代状态顺序或授权代次。
4. Attach、Status、SessionSnapshot 和 SSE 显式传播 BindingGeneration、stateSequence、queueVersion、playbackInstanceId、Playback Mode、mute 和稳定 Queue Item ID。
5. 每次实际开始一个新的曲目播放实例时签发新 playbackInstanceId。Natural End 的单曲循环产生新 ID；暂停恢复、Output Migration 和刷新 handoff 保留原 ID。
6. Output Migration 将源播放实例身份带入目标，目标不得因跨输出恢复而另发实例。后续真正的新曲目实例再由当前权威方签发。
7. 浏览器新播放实例由 Module 签发 UUID，服务端新播放实例由服务端签发；跨输出时以被迁移实例为准。
8. HTTP 快照、SSE 和重连首包进入同一接纳通道。BindingGeneration 是现场归属前提，stateSequence 是同代次内的全序依据，queueVersion 不独立决定播放状态新旧。
9. 同代次较低 stateSequence 丢弃；相同序列的重复快照幂等；相同序列但语义冲突视为同步错误并重新获取完整快照；较高序列才推进。
10. 只有完成能力和代次握手后的首个全量快照可建立新基线。新 BindingGeneration 的新现场允许较低 queueVersion，不被旧现场版本拒绝。
11. 每次重连增加 channelEpoch，旧连接事件全部丢弃。SSE 是可能丢失的失效通知，重连必须重新获取原子全量快照，不能把 broker event ID 当 stateSequence 或持久日志。

### 9. Playback Control Authority 与操作结果

1. 受保护 Session 的 BindingGeneration 从 1 开始；同 owner 普通 heartbeat 和重附保留，设备绑定变化、显式 takeover 和刷新 handoff 在线性化点递增。
2. 所有受保护写显式校验 Session、User、ClientID 和 BindingGeneration。缺失或零代次返回 GENERATION_REQUIRED；小于或大于当前值均返回 COMMAND_SUPERSEDED。
3. JSON 解码必须区分缺失／零值与合法数值，不能用普通零值默认值绕过校验。
4. 鉴权在解析或修改目标设备前完成。成功测试必须同时证明旧写被拒绝和新 Playback Context 零副作用，不能只检查错误码。
5. 非 owner 在 Attached 或 Recovering 状态均必须显式 takeover；原 owner 可按有效身份重附。显式 takeover 保留 Playback Context、撤销旧 owner 写入并递增代次。
6. Playback Control Authority 与播放生命周期解耦。observer、detach、heartbeat 过期和远端 pagehide 不自动停止服务端播放。
7. 操作结果按 Session 与 operationId 保存有界结果和响应。容量、TTL 和淘汰可配置并有并发测试；淘汰后无法确定的结果保持 UNKNOWN。
8. 旧代次 queryOperation 只用于只读对账，不授予写权限。结果表不承诺跨服务重启恢复。
9. 受保护现场禁止未携带新安全语义的 legacy 写入；只有显式未保护的默认 legacy 设备路径可省略代次。
10. 保持 ps 锁到 device 锁的既有顺序、准备取消、播放 token、迟到输出退休和锁外发布回调约束，不为新协议倒置锁序或在锁内执行外部通知。

### 10. Output Migration 单一事务协议

Module 的 migrateOutput 是唯一跨输出编排入口；服务端执行原语负责各自原子性。统一路径按以下阶段执行：

1. **现场固定**：记录源 Playback Context、acceptedIntent、意图序号、queueVersion、playbackInstanceId、Playback Mode 和规范 V／mute，建立稳定 migrationId、currentEpoch、整体 deadline 和迁移锁。
2. **目标准入**：发布 pendingOutputTarget 并取得非破坏预留。目标已有有效 owner、活跃轨或非空队列时先返回 409 TargetOccupied、opaque sceneFingerprint 和队列数量；用户确认后以相同 migrationId 和 expectedFingerprint 申请预留。
3. **旧端停止确认**：浏览器源确认真实媒体暂停，服务端源确认 MPV Pause。失败或 UNKNOWN 立即终止后续写阶段，只释放当前 migration 仍持有的预留，目标原 owner 和现场保持不变，也不自动恢复源输出。
4. **现场原子提交**：服务端目标重新取得既有顺序的锁，校验预留、预期 owner、BindingGeneration 和当前 sceneFingerprint；任何第三方变化均提交失败并保留第三方最新现场。校验通过后才原子撤换 owner、导入现场并递增代次。
5. **目标音量与 mute 确认**：在目标启动前应用当前 profile 的 G、独立 mute 和必要初始位置。失败或 UNKNOWN 不启动目标。
6. **按最后意图启动**：只有最后 acceptedIntent 仍为 play 才执行目标 play；pause 或 stop 保持暂停。启动以真实执行结果和后续权威快照确认，不仅以 HTTP 完成判断。
7. **完成与回填**：接纳新 BindingGeneration 和 stateSequence 的全量快照，清理仍属于本 migration 的临时状态，再结算仍有效的新意图。状态回填不发送补偿命令。

补充约束：

- 派生命令使用 migrationId、phase 和 step 形成稳定 operationId；阶段重试只能查询或复用原 ID，不能重新随机生成。
- sceneFingerprint 由服务端绑定 deviceName 与 sceneRevision 签发，是 opaque 一致性令牌而非授权凭证；位置时钟推进不改变指纹，队列、当前项、播放／暂停、模式、owner 或绑定变化会改变。
- 冲突预留、指纹变化、owner 变化和代次变化均拒绝覆盖。预留创建本身不得驱逐 owner、清空队列或改变目标播放状态。
- 提交后的音量或启动失败保留可见的已迁移失败 Playback Context；不得自动恢复原 owner、旧端出声或回滚更新现场。
- 清理只能释放仍属于本 migration 的预留。所有取消、释放和清理都受 deadline 与对账契约约束。
- 浏览器到服务端、服务端到浏览器和服务端到服务端共用这一阶段语义；浏览器目标的就绪与现场安装由 Module 和 BrowserAudioAdapter 完成，服务端目标的原子提交由服务端完成，不引入第二事务权威。

### 11. 刷新、重连与混合版本

1. ClientID 是每文档身份。LocalStorage 只保存用于发现可观察 Session 的 ID，不保存可共享控制身份。
2. 当前 owner 的 Attach／Heartbeat 响应可签发或轮换 controlHandoffToken；普通 SSE 和观察者响应不得包含该秘密。
3. token 绑定 Session、User、当前 DeviceName、单次 nonce 和短 TTL；服务端只存哈希。新文档以新 ClientID 和 token 请求 handoff。
4. handoff 原子消费 token、替换 owner 并递增 BindingGeneration，不改变队列、位置、播放状态、音量、模式或 playbackInstanceId。
5. sessionStorage 在同标签页刷新保留 token。克隆标签页即使复制 token，也只有首个消费者成功，其余保持只读；无 token 时必须显式 takeover。
6. 旧文档在 handoff 后写命令因 owner 或代次不符被拒绝并转只读。403 同样取消在途写入、停止旧命令重试并转观察者。
7. 短时断线在 45s TTL 内以原文档 ClientID 重附，重新握手能力、代次和状态序列；离开 recovering 前先对账 UNKNOWN，不恢复写入。
8. heartbeat 默认可配置为 15s；重连采用有上限的指数退避。heartbeat 与 Scrobble keepalive 是不同职责。
9. 服务端崩溃或重启不恢复内存 Session、预留和操作结果。客户端收到 404 或能力重置后进入未连接态，丢弃待重试命令并重新握手，不自动起播。
10. 新客户端必须肯定探测严格代次、结果查询、稳定 Queue Item ID、stateSequence、服务端模式、非破坏预留和 handoff。缺少当前路径所需能力时禁用对应远端写路径，降为受限浏览器播放或只读。
11. 旧客户端只能操作未受保护的 legacy 默认设备；目标 BindingGeneration 大于零时旧写返回 409 Conflict。
12. 后端回退前停止新写入、等待或查询在途操作，客户端丢弃待重试队列和 capabilities 缓存并重新握手。旧服务端不具备前向读取能力时阻止降级；备份恢复需单独授权。

### 12. 宿主灰度、副作用与退役

1. 宿主通过受控 UnifiedNativePlayer 接入 Module；旧 RemotePlayerController／RemotePlayer 与旧 LocalPlayer 分支在最终退役前保留为灰度回退路径。
2. 服务端配置只决定新页面默认值；URL／localStorage 覆盖用于单标签页灰度和回退。覆盖在重新加载后生效，配置默认值需要服务配置生效并让客户端重新加载。
3. 快捷键、列表操作和 MediaSession 全部进入 Module。根节点 keydown 与宿主全局快捷键不得重复消费同一事件；组件卸载和 controller 变化完整注销。
4. ReplayGain AudioContext 只连接 BrowserAudioAdapter 的真实元素，迁移时断开并清理；服务端输出创建数为零。
5. 转码决策、预取和错误失效复用既有 decision service；浏览器能力保留，服务端模式不得因此请求媒体流。
6. 通知、ReactGA、AudioTitle、歌词、radio 特判和位置展示消费同一权威实例事件，旧新组件不得双重执行。
7. Session lease／heartbeat 属于 RemoteAudioAdapter；播放统计 keepalive 属于 ScrobbleCoordinator，两者不得互相替代。
8. 旧上报路径与新协调器的交接必须原子，灰度切换前后任一事件只有一个上报执行者。
9. 远端状态同步采用带 stateSequence 的快照／SSE 与展示微插值，替代 TTL 时钟猜测和冗余轮询。
10. 旧远端组件、重复队列 saga、TTL 猜测和旧 LocalPlayer 只在共同案例、宿主副作用、回退演练和 M4 生产门禁通过后删除；删除后不得留下第二套业务规则。
11. 回退不得掩盖已经发出的 UNKNOWN 命令。切换路径前先对账，不以重新加载或关闭开关推定执行端未运行。

### 13. 在线 Scrobble 责任

1. 单一 ScrobbleCoordinator 消费 Module 权威生命周期与 playbackInstanceId，负责浏览器和在线受控服务端播放的 starting、playing、paused、stopped、周期 heartbeat、pagehide keepalive 和 Natural End keepalive。
2. 只有 ownershipState 为 attached 且当前文档持有 Playback Control Authority 时上报；observer、失权文档和迁移中的非 owner 不上报。
3. 实际开始发送 starting 后接 playing；暂停／恢复发送 paused／后续 playing；权威停止或 Natural End 发送 stopped。
4. Output Migration、React 重渲染、重复快照、暂停恢复和刷新 handoff 保留实例且不产生额外 starting／stopped；Natural End 单曲循环对旧实例 stopped、新实例 starting。
5. 浏览器 pagehide 导致播放终止时发送 stopped keepalive；服务端 pagehide 但设备继续播放时不得伪造 stopped，可发送最后已知 playing 后停止客户端 heartbeat。
6. 以 user、device、playbackInstanceId 和 event 去重，并使用 sessionStorage 降低刷新重复；不宣称跨服务重启或跨独立浏览器全局去重。
7. 客户端只提供真实 mediaId、位置和生命周期，50%／240 秒阈值仍由服务端 PlayTracker 裁定。
8. 上报失败不阻塞播放，采用有界退避且不重放过期生命周期；持续漏报必须可诊断。
9. 本期不实现无人附着的 DeviceScrobbleBridge。远端网页关闭后的持续 Scrobble 仍是已知缺口，不因“避免重复”而记为已修复。
10. 外部 Last.fm／ListenBrainz 实际投递没有独立授权与证据时始终标记 UNVERIFIED。

### 14. 工作分解、依赖与发布门禁

九个任务按真实依赖实施：

| 任务 | 责任 | 前置依赖 |
| --- | --- | --- |
| 1 | Module 状态机、迁移锁、取消、UNKNOWN 与幂等协调 | 无 |
| 2 | BrowserAudioAdapter 就绪握手、真实挂载与媒体错误 | 1 |
| 3 | 统一 V／U／G、独立 mute、MPV 停止／启动同步确认 | 1；后端基础可并行 |
| 4 | BindingGeneration、操作结果、非破坏预留、handoff 与准入 | 无 |
| 5 | 服务端 Playback Mode、stateSequence 与 playbackInstanceId | 4、6 |
| 6 | 稳定 Queue Item 与相对锚点协议 | 4 |
| 7 | 生产 RemoteAudioAdapter、能力探测、状态排序与重连 | 3、5、6 |
| 8 | 宿主接线、副作用迁移、配置与可执行回退 | 2、7 |
| 9 | 在线 Scrobble 生命周期、keepalive 与排他上报 | 8 |

1. M1 依赖任务 1—4：Level 2／3 安全契约通过，代次矩阵、UNKNOWN、真实停止错误传播和非破坏接管无盲区；真实 MPV 证据仍单独标记。
2. M2 依赖任务 6、5、7：稳定身份、服务端四模式、状态顺序、生产 RemoteAudioAdapter、handoff 和真实 Chrome 零引擎贯通。
3. M3 依赖任务 8、9：宿主副作用完整、在线上报排他、UI 与后端回退均演练。
4. M4 在单独授权后验证真实 MPV Pause／SetVolume／Unpause、无客户端 Natural End、当前项移动、迁移故障和物理蓝牙缓冲／断连。
5. M1—M3 只授予下一阶段集成资格。所有生产必需 Level 4／5 证据通过前，不得默认切网或删除旧路径。
6. M、L、XL 只表示相对工程复杂度，不构成日期承诺。每个任务开工前根据当前 diff、复用资产和未验证项重新评估。

## Testing Decisions

### 1. 好测试的标准与主要 seam

1. 测试从调用者或用户可观察的意图、命令、权威状态、快照、请求和禁止副作用断言行为，不绑定私有字段、内部函数调用顺序或特定实现结构。
2. 已确认的主要共享行为 seam 是 PlaybackControlModule 的公开意图／状态 interface。浏览器与远端 Adapter 在同一 seam 下消费共同案例，不分别建立业务规则。
3. 服务端独有的权威行为通过既有播放服务命令和 Native API 的命令／快照／SSE 外部契约验证，不以私有辅助函数代替生产准入、序列化和广播。
4. 真实原生 UI 和真实 Chrome 是集成验证环境，不另建一套端到端业务预期。Adapter 小范围测试只证明其外部能力，不复制 Module 状态机。
5. 每条案例包含稳定编号、关联需求、初始 Playback Context、用户意图或权威事件、预期最终状态和过程禁止项。
6. 过程不变量必须显式断言：暂停编辑全程零起播、移动当前项零重载、状态回填零命令、旧停止或目标音量未确认时目标 play 为零、失权后零成功写入。
7. 不以最终高亮相同、最终暂停、ERROR 被清除或测试总数增加代替正确性。接口允许的坏时序只有在测试实际注入并观察破坏时才称为已复现。
8. 相同 operationId、相同 SSE 和重复媒体事件必须验证幂等；响应丢失与确定失败分开注入，UNKNOWN 不得直接按失败重试。

### 2. 共同案例与既有测试资产

1. 复用现有 dual-engine loop 集成套件作为 Module、BrowserAudioAdapter、MockRemoteAudioAdapter 和 UnifiedNativePlayer 的 Level 2 回归主场，扩展 TC-DE、TC-MIG、TC-QC、TC-PM 和 TC-IN，而不是新建平行测试体系。
2. 该套件主要使用 jsdom 和 mock Adapter；其通过只能证明候选契约，不能记为生产 RemoteAudioAdapter、真实 Chrome、生产服务端或真实 MPV 通过。
3. 复用现有 Go Playback Server 迁移验证，覆盖暂停／播放迁移位置、显式 takeover、旧 owner 串行撤销、准备取消、同 owner 跨设备迟到命令、目标已有 Session 和目标启动失败保留现场。
4. 复用现有设备取消与播放 token 测试，确保停止取消待执行 Natural Advance、取消的 start 不取代后继、迟到输出被退休。
5. 现有 QueueCandidateModel 的重复项与四模式测试可作为行为案例来源，但不能冒充生产 Playback Queue 或 playbackDevice 验收；任务 5／6 必须在生产路径重跑相同预期。
6. 复用现有 RemotePlayerController 的旧 HTTP 快照、SSE 失效重取、重连全量快照和显式 takeover 案例，迁移到生产 RemoteAudioAdapter 后保留相同外部语义。
7. 复用宿主事件、文档身份、eventStream、Session 状态、SSE 过滤和 Native API 鉴权／错误映射测试；新增字段必须在真实出站映射上断言，而非只检查内部 struct。
8. 复用 MPV IPC 的乱序响应匹配和 deadline 测试，以及真实进程／null output 生命周期测试。fakeTrack 与真实 MPV 证据分层报告。
9. 共同案例以数据或明确表格表达初始条件、动作和预期，Go 与 TypeScript 可分别消费；不要求共享实现代码，也不要求 mock 远端复制生产服务端内部逻辑。

### 3. Module 与 Output Migration 测试

1. 覆盖 TC-MIG-01—12 的共同行为，并补充 PREPARING 起播、自然下一首准备、显式 pause／stop、后续新 play 和意图序号。
2. 注入迁移中 play、pause、seek、队列编辑、自然结束和第二次迁移，证明命令不穿透门槛、过期意图不结算、新迁移不隐式覆盖。
3. 分别注入旧端停止拒绝、挂起、超时和响应丢失；目标 play 必须为零，只有当前 migration 预留被释放，目标原现场不变。
4. 分别注入现场提交冲突、目标音量 FAILED／UNKNOWN、目标启动拒绝和最终 pause 挂起；验证有界终端状态、可见错误和禁止自动恢复输出。
5. 对 reserve、commit、start、pause、queue move 和 reservation release 注入响应丢失，证明 queryOperation／reservation 查询先于任何重试。
6. 验证 APPLIED、NOT_EXECUTED、FAILED、UNKNOWN 的分支，以及结果淘汰、服务重启和无法对账时 fail-closed。
7. 注入旧 epoch Promise、迟到媒体事件、旧 owner 写、同 owner 旧代次写、旧 HTTP 快照和旧 channel SSE，分别验证 UI 接纳与执行侧拒绝。
8. 验证迁移后新 BindingGeneration 可建立较低 queueVersion 的新基线，同代次旧 stateSequence 则不能覆盖新状态。
9. 所有阶段使用受控时钟证明 deadline，不用真实长时间等待，也不把 Promise.race 当执行取消证明。

### 4. BrowserAudioAdapter 与真实原生 UI

1. Level 2 覆盖同元素重复 attach、跨代次 attach、detach、deactivate、资源尾事件和真实 Promise 错误信封。
2. Level 3 在真实 Chrome 验证 pendingOutputTarget 到真实元素挂载的完整链路；不存在脱离 DOM 的预先起播，迟到 attach 不清空现场。
3. 验证未挂载 TARGET_NOT_READY、命令发出后的 UNKNOWN、autoplay NotAllowedError、媒体 404／解码失败和用户再次显式播放。
4. 使用真实原生控件执行点击、进度拖动、音量拖动、模式切换、Queue Item 点选、删除和 DOM 拖拽，不用模拟控制器证明原生 UI 可用。
5. 覆盖根节点与全局快捷键、空格默认行为与冒泡、MediaSession play／pause／previous／next／seekto，以及卸载和 controller 变化后的完整注销。
6. PREPARING 且 acceptedIntent=play 时验证一次 toggle 只发送 pause，不重复 play；UI 同时保留真实准备状态。
7. 验证无浏览器远端音频事件时，进度、歌词、标题、当前项高亮、加载和错误仍由 Module 状态正确驱动。
8. 本地输出测试保留真实媒体确认，不用 mock play 成功覆盖浏览器自动播放限制。

### 5. 生产 RemoteAudioAdapter 与零引擎

1. 在真实服务端和真实 Chrome 上覆盖挂载、播放交互、重连、刷新 handoff、Output Migration、失权和卸载全过程。
2. 在被测模块加载和挂载前安装探针，断言 Audio、HTMLMediaElement、AudioContext 创建数为零，媒体流请求为零；封面和元数据请求单独分类。
3. 浏览器切服务端时分别记录旧引擎停止／清理和远端新引擎创建，避免用清理阶段的既有元素误判，也避免漏掉残留回调。
4. 能力探测逐项缺少严格代次、操作结果、稳定 ID、状态序列、服务端模式、预留或 handoff，验证对应写路径禁用；旧服务端静默忽略字段不得判为支持。
5. 注入 HTTP 与 SSE 交错、重复、乱序、重连首包和 channelEpoch 过期，验证统一排序和零命令回声。
6. 模拟 SSE 丢失和缓冲溢出后的恢复，证明重连重新获取完整快照，而不是依赖事件 ID 重放。
7. 注入 403、lease 过期、observer、显式 takeover、handoff 和克隆竞争，验证在途写取消、只读降级和唯一控制者。
8. 弱网与重连测试必须有上限退避、UNKNOWN 对账和恢复后的新基线，不以模拟 SSE 替代 Level 3 真实联调。

### 6. 服务端队列、模式、权限与状态顺序

1. 在生产 Queue 和 playbackDevice 上运行 TC-QC-01—12 与 TC-PM-01—10 的共同预期，包括重复 MediaFile、单项／多项／空队列和所有手动／自然分支。
2. 使用注入随机序列验证多项严格排除当前项、单项允许重播和队列物理顺序不变；不依赖概率性断言。
3. 无浏览器和无客户端附着时分别验证四种模式的 Natural End；网页关闭不能是自动推进的必要条件。
4. 验证移动当前项不重载 Track、不重置位置和 playbackInstanceId；暂停删除零起播；清空停止并清除实例。
5. 验证 no-op 不增 queueVersion，源／锚点缺失和版本冲突附最新快照且现场不变，双协议字段返回 400。
6. 验证 legacy ID-only 原子升级按旧 currentIndex 保留正确重复实例，legacy 路径不能绕过代次或 owner。
7. 对 BindingGeneration 缺失、0、较小、相等、较大分别测试，并在 reattach、takeover、refresh handoff 和多次 rebind 后重复；失败写必须零副作用。
8. 对 operationId 并发重试、响应丢失、TTL、容量淘汰和服务重启验证不重复执行与 fail-closed；旧代次查询不得获得写权限。
9. 验证 recovering 非 owner 普通 Attach 被拒绝、显式 takeover 才准入，以及原 owner 合法重附。
10. 验证 sceneFingerprint、冲突预留、确认后第三方变更、TOCTOU 和非破坏释放；目标原 owner 与现场在旧端停止失败时保持可用。
11. 验证所有语义变化递增 stateSequence、位置 tick 不递增、Natural Loop 新实例、迁移和 handoff 保留实例。
12. 运行 Go race 测试和 Native API 并发测试，重点检查 ps→device 锁序、TTL 清理、预留竞争、锁外事件发布和迟到准备取消。

### 7. 音量、MPV 与硬件证据

1. Level 2 验证 U／V 往返、蓝牙 G=V^0.15、逆映射、V=0、趋近 0、V=1、NaN、Infinity、越界和未知 profile。
2. 验证普通→蓝牙→普通保持 V=0.64、U=0.8，运行中调整、重复快照和至少五次重连不累积漂移。
3. 验证量化容差内重复值幂等，容差外真实外部变化只逆映射一次；mute、静音中调音量、解除 mute 和 lastNonZeroVolume 均独立。
4. fakeTrack 验证活跃轨和新轨 SetVolume、Pause、Unpause 错误贯穿停止门槛、API 和 Adapter，DesiredGain 不冒充确认 Gain。
5. Level 3 验证真实滑块、U 域键盘、mute 和迁移前后展示；这些结果不证明物理响度。
6. Level 4 在单独授权的真实 MPV／PipeWire 环境验证 SetVolume、Pause、Unpause 成功、失败、超时、新轨初始增益、无客户端 Natural End 和移动当前项。
7. 真实迁移验证旧停止未确认和目标音量未确认时零启动，启动失败保留目标失败现场；fakeTrack 不能替代此证据。
8. Level 5 在单独授权的物理蓝牙环境验证输出切换、缓冲、断连和实际出声。软件先停后启不等于物理无重叠，也不承诺无间隙或等响度。

### 8. 宿主、Scrobble 与回退

1. 在真实宿主验证所有列表、快捷键、MediaSession 和原生控件只进入 Module 一次，旧路径不再执行同一意图。
2. 验证 ReplayGain 只连接浏览器真实元素，远端 AudioContext 为零；转码预取保留本地能力且远端不请求媒体流。
3. 验证通知、ReactGA、AudioTitle、歌词、radio 和转码错误失效与旧基线一致，灰度切换不双发。
4. 验证 URL／localStorage 覆盖重新加载后回退、配置默认值注入链路，以及回退前 UNKNOWN 对账；不得宣称免刷新或全量即时回退。
5. ScrobbleCoordinator 覆盖本地和在线远端 starting／playing／paused／stopped、周期 playing、Natural End keepalive 和 pagehide 差异。
6. 验证 Natural Loop 旧实例 stopped／新实例 starting，迁移、重复 SSE、React 重渲染和 handoff 不重复生命周期。
7. 验证 observer、失权和非 owner 零上报，旧新路径切换同一事件只有一个执行者。
8. 验证上报失败有界退避、不阻塞音频、不重放过期生命周期；服务端测试证明 PlayTracker 接纳与阈值，客户端不复制阈值。
9. Level 2／3 只证明生命周期、实例去重、排他和正确 REST 请求。外部 Scrobbler 投递没有独立证据时标记 UNVERIFIED。
10. M3 回退演练必须覆盖在途命令、播放现场、重复副作用和重新握手；通过 UI 回退不等于后端协议回退。

### 9. 证据等级与里程碑准出

1. Level 1：格式、静态检查和类型检查，只证明静态一致性。
2. Level 2：确定性 Vitest 与 Go 契约，只证明相应模拟或 fake 环境下的外部行为。
3. Level 3：真实 Chrome、DOM 和真实服务端联调，证明真实浏览器交互、零引擎探针和真实协议行为。
4. Level 4：真实 MPV IPC／PipeWire，证明真实输出进程行为；需要单独授权。
5. Level 5：物理蓝牙 A2DP／Sink，证明实际硬件行为；需要单独授权。
6. 未执行的层级明确记录 UNVERIFIED；“待授权”不等于通过，也不等于生产必需项可跳过。
7. 每次证据记录宿主提交、工作树 diff、依赖锁与 integrity、实际安装产物、构建命令与 bundle hash、浏览器版本、配置开关和输出位置。
8. 声明依赖、实际安装产物和测试 bundle 必须一致；历史版本差异先消除或通过新鲜锁定构建重建，不能拼接不同产物证据。
9. M1、M2、M3 只在各自 Level 1—3 必需项通过后推进；M4 的生产必需 Level 4／5 全部通过后才允许默认开启和旧路径删除。
10. 本规格编写过程本身不记为任何产品测试已通过；实施票据必须引用实际运行结果，而不是测试文件或参考代码存在。

## Out of Scope

- 服务端进程崩溃或重启后的 Session、预留、操作结果和 Playback Context 恢复；当前决策是 fail-closed 并重新握手。
- 无人附着时由服务端独立持续上报的 DeviceScrobbleBridge，包括其用户归属、租约、takeover 和依赖注入设计。
- 未经独立授权与证据的 Last.fm／ListenBrainz 实际投递验收。
- 随机历史、shuffle×repeat 正交组合、逻辑随机游标或额外模式别名。
- 所有浏览器标签页强制单实例播放；单控制者约束针对共享服务端输出。
- 不同设备物理响度、解码延迟、蓝牙缓存完全一致，或无间隙 Output Migration。
- 以独立 Remote Dock、假音频对象或第二套业务规则替代原生 Player UI 复用。
- 重写蓝牙发现、音频基础设施、React 技术栈或整个服务端播放系统。
- 在 M4 前默认切网、删除旧路径，或把保留回退分支解释为永久维护两套业务规则。
- 未经单独授权的生产部署、服务重启、备份恢复、真实数据库操作、硬件操作、依赖发布或其他外向发布。
- 把上游接受补丁作为交付前提，或在现有证据不足时承诺精确日期。
- 将实现任务的完成等同于生产验收、硬件验收或外部 Scrobbler 验收完成。

## Further Notes

1. 本规格是既有验证规格之后的生产实施规格；原验证与规划交付保持历史记录，不通过覆盖旧规格改变其范围。
2. 当前统一 Player、Module 和 MockRemoteAudioAdapter 属于候选／原型资产；生产宿主仍走旧路径，生产 RemoteAudioAdapter、完整服务端四模式和若干安全协议仍须实施。
3. 现有 revalidation、QueueCandidateModel、fakeTrack、jsdom 和 mock 远端证据可以指导设计与回归，但必须按其真实环境标级，不得直接转化为生产能力声明。
4. 新实施票据应按九个任务一票拆分，保留本规格的依赖、共同案例、Exit Criteria 和证据门槛；不得把九项工作合并为一个不可验收的大票。
5. 研究、验证收据和未提交 diff 归档在本功能既有 artifacts 区域，不提交运行数据、凭证、构建输出或个人配置。
6. 新依赖需单独审查来源、许可证、维护成本和锁文件变化；优先复用已核实的播放器发布包和现有 Sortable，不修改安装目录或执行来源不明脚本。
7. UI 灰度回退、后端协议回退、生产部署和硬件验收是不同授权边界。任何阶段失败都应如实停止推进并保留反例，不用降低共同契约换取通过。
8. `ready-for-agent` 表示本实施规格已具备可执行的行为、接口、测试和边界决定；它不表示部署、远程发布、服务重启、硬件操作或旧路径删除已获批准。
