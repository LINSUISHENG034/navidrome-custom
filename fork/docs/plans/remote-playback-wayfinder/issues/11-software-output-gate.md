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
