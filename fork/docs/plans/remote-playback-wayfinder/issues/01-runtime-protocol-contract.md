# 锁定实际部署与协议兼容候选

Type: research
Label: wayfinder:research
Mode: AFK
Status: resolved
Assignee: LINSUISHENG034
Parent: ../map.md
Blocked by: none
Research branch: research/remote-playback-runtime-contract
Research asset: ../research/runtime-protocol-contract.md

## Question

在不改变运行环境的前提下，哪些**锁定版本**的服务器/Bridge 组合值得进入互通原型，哪些版本信息或 wire 行为仍未核实？

- 区分当前运行镜像/状态、声明配置、历史启动日志、依赖锁定和当前上游，不把配置默认值当成运行事实。
- 核对实际可读取的 MA、Bridge、Go SDK/CLI 版本及对应源码/依赖；记录足以复现的公开 tag/commit/digest，避免无界追踪 latest。
- 标明旧明文、Noise、RC1 等能力的来源与适用版本；不要从当前上游差异推断本机 Bridge 2.75 一定不兼容。
- 给出最小候选矩阵、必须由原型验证的假设、停止条件与可复用的服务器能力；重复已核实的许可证或 SDK 存在性不是本票重点。
- 允许只读宿主文件及脱敏的容器元数据；不执行容器内命令、抓取流量、读数据库、调用带凭据的应用接口、启动/重启服务或尝试握手。

研究报告持有具体证据，票的 Answer 只记录候选决策及证据边界。不能把报告中的静态兼容判断写成已实测互通。

## Answer

详细事实、版本锁、wire 来源、未知与 stop gates 见命名研究资产：[实际部署与协议兼容候选：锁定证据](../research/runtime-protocol-contract.md)。

- 保留已按 RepoDigest 锁定的 MA 2.10.4 + Bridge 2.75.0 artifact 作为历史 baseline/回滚比较候选；观测时两者均 exited，不能称当前活跃生产互通，不能未经批准恢复。Bridge 的 tag、OCI main/revision、source 依赖声明与 installed 包必须分开。
- 第一 Go 候选限定为官方 CLI v0.1.1 → SDK v1.8.2 与旧 Bridge artifact/明确锁定的 2.75.0 source。旧明文首包存在静态交集，值得进入有限私用原型设计；目标互通和可听播放都未经实测。
- 固定新 Bridge source + SDK 9.1.1 与 MA/官方 Python server 作为新协议参考比较；旧 Go + 新 Bridge 默认 Noise 路径暂不纳入第一轮直接接线，先过协议/安全设计 gate。不用当前上游差异否定旧 Bridge 目标。
- installed 依赖、真实历史 boot/log、有效准入配置、队列/输出语义与完整标准符合性仍未知；日志过滤读取被工具隔离校验拒绝后未重做或代读。没有运行、播放、握手、编译测试或修改运行环境；完整 Sendspin 1.0 合规与基础私用原型不同，静态研究不能宣布投产。
