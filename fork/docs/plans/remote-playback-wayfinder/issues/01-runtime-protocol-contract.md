# 锁定实际部署与协议兼容候选

Type: research
Label: wayfinder:research
Mode: AFK
Status: claimed
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
