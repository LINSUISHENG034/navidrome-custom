# 稳定远端蓝牙播放：验证与实现路线地图

Label: wayfinder:map
Created: 2026-10-09

## Destination

用可核验的版本、协议和真实设备证据，选定 Ubuntu Server 播放 Navidrome 曲库到蓝牙音箱的可投产路线，并形成可交接的实现、测试、迁移、维护与回滚方案。地图结束于「选型和实施所需的决策已经清楚」，不等于代码完成或生产上线。

## Notes

### 已确认的范围与约束

- 优先服务当前单服务器、单蓝牙输出；稳定性、日常操作和维护成本优先于合并容器数量。
- 原生 Navidrome 页面是加分项，不是第一阶段的前置条件。
- 排除「静音浏览器仍实际播放、远端同时播放、两边共同推进曲目」的方案；任何保留页面的路线都必须是真正的远端控制模式。
- 将用户曾确认工作稳定的 MA + Bridge 链路保留为比较候选和恢复资产；实际运行状态以研究证据为准，未经恢复验证不称为正在服务或可直接回滚的生产基线。Go SDK 是有证据基础的候选，不预先承诺替换 MA，也不从零重写已有协议。
- 首段音频完整是硬门槛。首次连接以 10 秒内可听为初始目标；15 秒未就绪应明确失败、撤销本次准备工作，不能由过期任务稍后自动恢复播放。指标待实测验证，不作为已达到的性能声明。
- 用户同意在约定维护窗口短时独占音箱；**每次实际执行前仍须再次确认具体窗口和操作**。不得删除生产容器、改动生产数据库或音乐库。
- 2026-10-09 用户进一步明确「全部由你判断确定即可，请继续推进开发」。本 effort 的 planning-only 默认据此覆盖为：代理可自行确定技术/产品细节、源码布局、软件原型和隔离开发测试，不再逐项等待技术签字；既有研究结论仍不等于生产或声学验收。
- 开发结构仍先检查既有模块、说明路径与职责；只版本化必要改动，不跟踪整个第三方源码副本。外部源码与测试输出放独立开发工作区，保持可复现版本和单一来源。
- 可启动不含生产数据、host network、D-Bus/Pulse/音频设备挂载的独立软件测试容器；不启动/重启既有生产服务、不连接/断开真实音箱、不播放宿主音频、不修改生产配置、数据库或音乐库。实际设备窗口仍须逐次确认。宿主不安装库；如必须安装，先满足 conda 和环境描述要求。
- 候选版本必须锁定。历史日志、配置默认值、当前上游源码和实际运行依赖必须分开标注；未实测的互通不得写成成功或失败。
- 私用基础播放原型不是完整 Sendspin 1.0 合规服务器；角色、安全和 wire 的完整标准符合性不能由一次音频演示推导。
- 首轮预算只讨论可行性与有限修补；生产加固和原生界面改造须在证据出现后重估，不能把工程日粗估当成交付承诺。

### 本地 tracker 约定

项目未配置专用 tracker。本次依照 Wayfinder 的 local-markdown 语义，把默认 `.scratch/<effort>/` **经用户批准覆盖为本目录**，遵守 `AGENTS.md` 的 fork 文档位置规则；不另建第二份地图或自动发布 GitHub issue。

- 唯一地图：`map.md`；术语唯一来源：[远端播放术语表](CONTEXT.md)。
- 子票：`issues/NN-<slug>.md`，每个文件一个决策问题；`Type` 对应 `wayfinder:research|prototype|grilling|task`，`Mode` 标明 AFK/HITL。
- `Status: open` 表示未认领；`Status: claimed` 与 `Assignee` 是认领；必须先保存认领再工作；`Status: resolved` 表示已关闭。
- `Blocked by: NN, NN` 是本地 tracker 没有原生依赖 UI 时的规定 fallback；`none` 表示无 blocker。全部 blocker resolved 才解阻塞。
- frontier 是 open、unclaimed、unblocked 的子票，按文件编号取第一个；向人汇报时使用带链接的票名，不用孤立编号。
- 问题留在票的 `Question`；解决时在该票追加 `Answer` 并关闭。地图 `Decisions so far` 只追加一行 gist 和票名链接，不复制决策全文，也不列开放票。
- 研究证据位于 `research/`，从研究票链接；报告是事实资产，不另存一份决策。评论追加在票的 `Comments`。
- planning 阶段沿用一次会话一张非 research 票；用户进一步委托全部技术判断并要求开发后，隔离软件开发与技术决策可连续推进并记录授权来源。不能伪造用户逐项回答；保留真实音箱、生产操作和声学验收的 HITL 边界。研究可并行，真实音箱实验因共享硬件必须串行并排他。
- 研究资产使用 `research/<name>` 临时分支，在隔离 worktree 内生成。合入地图分支前校验范围；不把已有未跟踪文件顺带提交。
- 规划检查点使用 `plan/remote-playback-wayfinder`；继续开发使用 `dev/remote-playback-output-gate`，与生产/主线分离。按用户的任务完成后 GitHub 备份要求，协调者只提交受审查的本次文件到自己的 fork 同名分支，不合并主线、不发布 issue/PR或镜像、不套用不存在的 `main` 模板。研究及上游临时源码分支不由子代理自行推送。
- 研究仅记录脱敏的版本、能力、状态和来源。禁止保存凭据、完整环境变量、原始 MAC、私有地址、数据库、完整运行配置或原始敏感日志。

### 工作方法与已有证据

- 每次工作先读 Destination、术语表和目标票，再按需读取其 blocker 和已关闭票。
- Research 票由调用 `mattpocock-skills:research` 的子代理处理；Prototype 票调用 `mattpocock-skills:prototype` 并与用户讨论具体产物；Grilling 票调用 `mattpocock-skills:grilling` 与 `mattpocock-skills:domain-modeling`。
- 模块和 Interface 设计遵守 `codebase-design`：一个播放权威，输出准备/释放集中，调用端不学习蓝牙命令顺序；测试跨相同 Interface，真实硬件证据另计。
- 已有历史资产仅作为本地路径参考：`fork/docs/todo/2026-03-06-jukebox-session-architecture-upgrade.md`、`fork/docs/plans/2026-04-28-bluetooth-module-optimization.md`。它们不是当前状态证明；其静音浏览器保留方向不能作为本路线实现模板。它们当前未跟踪、不随本轮提交，因此不生成在 GitHub 备份中无法访问的 Markdown 链接。
- 已查到的候选和规范来源：[Go SDK](https://github.com/Sendspin/sendspin-go)、[Go server CLI](https://github.com/Sendspin/sendspin-go-server)、[Python SDK](https://github.com/Sendspin/aiosendspin)、[Bridge](https://github.com/trudenboy/sendspin-bt-bridge)、[规范 scope](https://github.com/Sendspin/spec/blob/main/SCOPE.md)。存在 SDK 不等于目标版本互通或应用层完整。

## Decisions so far

- [锁定实际部署与协议兼容候选](issues/01-runtime-protocol-contract.md)：锁定历史 MA/Bridge 镜像候选，首轮 Go 私用原型限定 CLI v0.1.1 + SDK v1.8.2 与旧 Bridge；静态首包交集不等于已实测互通。
- [确定输出准备与释放的最小协调位置](issues/02-output-readiness-contract.md)：优先验证播放会话到输出 adapter 的 pre-stream 准备/释放契约；现有 wake success 不等于物理 ready，首段与取消闭环仍需原型。
- [确定验收语义与验证止损预算](issues/03-acceptance-and-budget.md)：按用户最新技术委托进入 MA-first 隔离软件开发；首期沿用暂停即 stop 保位、释放后重新准备的原生语义，物理宽限期留作后续选项；真实设备验收仍需维护窗口。
- [实施隔离的软件输出准备闭环](issues/11-software-output-gate.md)：交付 MA+Bridge+SDK 同 worker 准备、退休、失联复验与权威队列隔离的软件闭环；95/49/42 干净重放通过，判定值得进入硬件窗口。

## Not yet specified

- 选型后的实现拆分：模块落点、持久化需求、打包方式和版本升级责任，待候选证据与 Interface 原型把范围照亮后细化。
- 若保留新的控制界面，其具体视觉/交互原型、媒体标识映射和旧组件退出方式，待控制入口范围确定后毕业为新票。
- 若输出准备协调涉及 MA、Bridge 或 SDK 的修改，上游贡献与私有补丁维护的具体边界，要看最小验证得到的改动面。
- 真实硬件时序、失败来源及声学测量方法可能暴露额外决策；当前不把未知故障假设写成确定缺陷或预切实施任务。

## Out of scope

- 未另行确认维护窗口的生产配置/服务改变、真实播放、蓝牙连接控制，以及发布上游 issue/PR或镜像；用户已委托的隔离软件开发不在此排除范围。
- 在已拒绝的静音浏览器双播放器回路上继续做稳定性补丁。
- 复制完整 MA 产品、为了一个音箱移植全部输出协议，或从零维护已有协议的等价实现。
- 第一阶段的多房间同步、DSP、广播源、移动客户端全面等价、所有蓝牙设备兼容承诺。
- 正式完整 Sendspin 1.0 合规/认证项目；若成为硬要求，应重新界定 destination，不能把基础原型改名冒充。
- 整理、移动、跟踪或提交全部既有 `fork/docs/`、聊天记录、运行数据和其他未跟踪文件。
