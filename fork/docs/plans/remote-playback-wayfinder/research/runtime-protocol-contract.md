# 实际部署与协议兼容候选：锁定证据

研究票：[锁定实际部署与协议兼容候选](../issues/01-runtime-protocol-contract.md)。观察日期：2026-10-09。范围：只读部署元数据、脱敏宿主证据和固定版本的官方源码；**没有握手、互通、音频、性能或标准符合性测试**。

## 结论先行

1. 当前可见的 MA `2.10.4` 与 Bridge `2.75.0` 容器均为 **exited**，不是本轮观测到的活跃互通链路。它们只能称为历史工作链路的候选 baseline；保留其不可变镜像用于将来经批准的比较，不启动或恢复它们。
2. Bridge 的 `Config.Image=:2.75.0`、OCI `version=main`、OCI revision、发布 tag 是不同字段。OCI revision 与发布 tag 的 commit 不同；两份仓库根 `VERSION` 文件均为 `2.75.0`，但 OCI revision 的 Python 配置模块版本常量为 `2.75.0-rc.6`，release tag 的常量已更新。根文件、应用常量、构建输入和实际容器输出不能混同；**不能据此断言装错版本或实际运行的应用版本**。
3. 固定 Bridge 旧源码声明 `aiosendspin==6.1.1` / `sendspin==7.5.0`；MA `2.10.4` 的 provider 声明 `aiosendspin[server]==9.1.1`。这些是 source/build 输入，不是容器内 installed 包证明。
4. 旧 Python client 的首包明文 `client/hello` 与 Go 旧 server 的首包契约存在值得验证的静态交集。新 Python client 的 Noise 流程与该 Go 首包契约不同，不能直接拿当前 Bridge 9.x 源码替旧 Bridge `2.75.0` 作兼容判决。
5. MA 固定源码明确提供 legacy 明文分支准入；不能仅因其依赖 9.x 而判定旧 Bridge 或 Go 必然不兼容。实际配置与端到端行为仍未知。
6. 基础私用播放原型与完整 Sendspin 1.0 标准符合性是两个项目。这里的候选准入不代表安全、角色、wire、输出准备或生产上线已通过。

## 1. 证据分层与边界

| 层次 | 可以证明 | 不能证明 |
| --- | --- | --- |
| Docker `ps` / `inspect` 的白名单字段 | 容器存在、当时状态、配置 image 字符串、实际 image ID、RepoDigest、公开 OCI 版本/revision | 应用已成功启动、installed 包、当前客户端连接、可听播放或故障原因 |
| 宿主 Compose image 行 | 声明的 image/default | 当时使用了该 default，或 tag 当前解析到同一 artifact |
| 历史部署文档 / boot / log | 文档提及的目标版本；若有过滤后的真实 boot 版本则只证明那个时刻 | 当下 runtime 或跨版本互通 |
| 固定 source 的 requirements / manifest / build workflow | 源码声明、构建预期、依赖覆盖规则 | 已部署 image 内部实际安装结果 |
| 固定 source 的 wire 分支 | 默认消息顺序、字段、角色及明确准入分支 | 未调用路径的实际配置、完整标准符合性、端到端音频表现 |

宿主证据用 `<deploy>` 表示获准只读的部署根目录；不列完整挂载、环境、配置、Labels 或日志。仅输出公开 image/version/revision/digest/status 和少量字段 presence。没有数据库读取、认证应用 API、容器内命令、容器文件提取、容器生命周期操作、蓝牙/音频操作、抓包、安装、编译或测试。

历史日志的白名单过滤读取曾被工具的 worktree 隔离命令校验拒绝；未换代理代读或重做。**真实历史 boot/log 版本未取得**；该拒绝不等于证明底层文件不可读。

## 2. 本地部署事实

### 2.1 当时可见容器与不可变 artifact

以下值来自 `docker ps --all` 和 `docker inspect` 的公共元数据字段；image ID 与 registry RepoDigest 不是同一种 digest。

**Bridge**

- `Config.Image`: `ghcr.io/trudenboy/sendspin-bt-bridge:2.75.0`。
- `State.Status`: `exited`；`State.ExitCode`: `255`。
- `State.StartedAt`: `2026-09-29T06:27:09.525662819Z`；`State.FinishedAt`: `2026-10-08T07:11:52.197478875Z`。
- 实际 `Image` / image `Id`: `sha256:d0f54f93c25ca948b9ccf92071a33394f17faf4aa505178a5a54958a65be729c`。
- `RepoDigests`: `ghcr.io/trudenboy/sendspin-bt-bridge@sha256:f024a481824283e1ae7aa7c8e308a3129e286d27bc8e38f85d199d9d182aabba`。
- OCI `org.opencontainers.image.version`: `main`。
- OCI `org.opencontainers.image.revision`: `0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad`。
- OCI `org.opencontainers.image.created`: `2026-08-26T12:05:08.696Z`；public source 为 [trudenboy/sendspin-bt-bridge](https://github.com/trudenboy/sendspin-bt-bridge)。

**Music Assistant**

- `Config.Image`: `ghcr.io/music-assistant/server:2.10.4`。
- `State.Status`: `exited`；`State.ExitCode`: `255`。
- `State.StartedAt`: `2026-09-29T06:27:09.527238084Z`；`State.FinishedAt`: `2026-10-08T07:11:52.230908371Z`。
- 实际 `Image` / image `Id`: `sha256:f8e25836cee73c7277f29a0dca804fbaa05f832a71616d15d50e337b04d4d0c8`。
- `RepoDigests`: `ghcr.io/music-assistant/server@sha256:37a9a2776e838a754c9f5b38c432567389952304e7cb8f6b44b6cd28043de6de`。
- OCI `version`: `2.10.4`；OCI `revision`: `e30a4974ba951f38e21bea8d502af3b903df992c`；public source 为 [music-assistant/server](https://github.com/music-assistant/server)。

两者时间接近与 ExitCode `255` **不解释停止原因**，也不证明它们此前成功互通。

**当前 running 的 Navidrome：仅作部署角色线索**

- 官方 `Config.Image=deluan/navidrome:0.64.2`，`State.Status=running`，OCI version `0.64.2`、revision `101145742f4762164202cb4858c9126258bdc463`。
- 官方 image ID `sha256:3fa63c3ddc32d31c79970194d2d83143ac316b0bcef78695d1d9ab4c5a4f84b9`；RepoDigest `deluan/navidrome@sha256:38dc2727bfcfd5ede290f8ada114fc90368146f265ae4701ddddbcbe2a44ee52`。
- 另一个 `Config.Image=navidrome-bt:dev` 也为 `running`；local image ID `sha256:3bc16891dc48a924fce6e51a99e4e851f2c3740e8b44f3711f3c792d5671a648`，没有 RepoDigests，未取得公开 version/revision 标签。
- 不能仅凭这些字段认定 MA 连接哪一个 Navidrome、当前哪条路径可播放，或谁拥有当前播放权威。

### 2.2 声明配置、历史记录与 presence

- `<deploy>/docker-compose.yml:6` 声明 image/default 中出现 `deluan/navidrome:0.64.2`；`:53` 出现 `ghcr.io/music-assistant/server:2.10.4`；`:68` 出现 `ghcr.io/trudenboy/sendspin-bt-bridge:2.75.0`。报告没有展开插值变量或环境值；它们是声明，不是 runtime 的推导来源。
- `<deploy>/docs/option-a-deployment.md:37-39` 提及同一组三个 image 版本；`:234` / `:236` 分别提及 MA `2.10.4` / Bridge `2.75.0`。这是历史文档提及，不是读取到的真实 boot/log 版本。
- `<deploy>/ma-data/settings.json` 中 `sendspin` key presence 为 true，`allow_legacy_clients` key presence 为 false。没有输出配置值，不能把 absent key 等同 runtime 准入政策。
- `<deploy>/bridge-data/config.json` 中 `SENDSPIN_SERVER`、`SENDSPIN_PORT`、`BLUETOOTH_DEVICES` key presence 为 true，`REQUIRE_PAIRING` / `VERSION` presence 为 false；没有读取为报告输出的服务器地址、设备身份或配置值。
- `ma-data` 可见持久化配置及历史日志文件，未读取任何数据库；`bridge-data` 有配置与 breadcrumbs，但未取得可引用的实际 boot 版本。没有可引用的 container-installed Python 包版本或本机 Go CLI/SDK 安装版本。

## 3. 有限 source / 版本锁

### 3.1 MA：tag 与 image source revision 对齐

MA `2.10.4` annotated tag 对象 `3ef4ec1069b0a9d45aee4d7bcce92e2330ff8a52` 指向 commit `e30a4974ba951f38e21bea8d502af3b903df992c`，与 OCI revision 相同。依据：[tag ref](https://api.github.com/repos/music-assistant/server/git/ref/tags/2.10.4)、[tag object](https://api.github.com/repos/music-assistant/server/git/tags/3ef4ec1069b0a9d45aee4d7bcce92e2330ff8a52)。这仍不证明包安装结果。

该 commit 的 [Sendspin manifest:10](https://github.com/music-assistant/server/blob/e30a4974ba951f38e21bea8d502af3b903df992c/music_assistant/providers/sendspin/manifest.json#L10) 明确要求 `aiosendspin[server]==9.1.1` 与 `av==16.1.0`。

### 3.2 Bridge：实际 image revision 与 tag 分开锁

- `v2.75.0` annotated tag 对象 `4746c3fe8e072eb5e22d2882dc6226d661d4ecad` 指向 commit `126e766d00876878aa67b5464d0f8f40e6b8c85d`；tagger 日期 `2026-08-26T12:04:47Z`。依据：[tag ref](https://api.github.com/repos/trudenboy/sendspin-bt-bridge/git/ref/tags/v2.75.0)、[tag object](https://api.github.com/repos/trudenboy/sendspin-bt-bridge/git/tags/4746c3fe8e072eb5e22d2882dc6226d661d4ecad)。
- 实际 image 标签 source revision 为 `0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad`，不是上述 tag target。两份公开仓库根 [image-revision VERSION](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/VERSION) / [tag-target VERSION](https://github.com/trudenboy/sendspin-bt-bridge/blob/126e766d00876878aa67b5464d0f8f40e6b8c85d/VERSION) 都为 `2.75.0`；这只描述这两个根文件。
- 另一个版本输入是 Python 配置模块：OCI revision 的 [config/__init__.py:50](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/src/sendspin_bridge/config/__init__.py#L50) 为 `VERSION="2.75.0-rc.6"`，release tag 的该常量已变更为 `2.75.0`。该差异也在[固定 compare](https://api.github.com/repos/trudenboy/sendspin-bt-bridge/compare/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad...126e766d00876878aa67b5464d0f8f40e6b8c85d)中可复核。未取得 image build provenance 或容器内版本输出，不能由根 `VERSION`、这个应用常量或 OCI label 单独推导实际包安装/运行版本。
- actual-revision [requirements.txt:14](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/requirements.txt#L14) pins `aiosendspin==6.1.1`，[:166](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/requirements.txt#L166) pins `sendspin==7.5.0`；[pyproject:11-12](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/pyproject.toml#L11-L12) 为相同版本并带 `[server]` extra。
- [pyproject:43-50](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/pyproject.toml#L43-L50) 记录对 CLI 自身 SDK 约束的 override 理由；tag-target 也有相同明确依赖声明，不能把 `Bridge 2.75`、`CLI 7.5`、`SDK 6.1.1`、hello `version=1` 混成一种版本。
- [Dockerfile:78-110](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/Dockerfile#L78-L110) 先安装过滤后的 requirements，再用 `SENDSPIN_VERSION` build arg 独立安装 CLI；arg 为空时使用 `sendspin>=5.3.0,<6.0.0`，不是 requirements 中的 exact CLI pin。
- 固定 [release workflow:110-125](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/.github/workflows/release.yml#L110-L125) 从 requirements 提取版本，[:266-280](https://github.com/trudenboy/sendspin-bt-bridge/blob/0f2cd9aba95827a4786f550c3d7cdf75c9ccfdad/.github/workflows/release.yml#L266-L280) 传 CLI build arg 并设置 SDK runtime 检查的期望版本。因此公开发布流程的**预期**是 CLI `7.5.0` / SDK `6.1.1`；没有读到本地 image 的 build provenance 或 installed 结果，仍须标未知。

### 3.3 当前上游 Bridge：只固定一次的对照，不冒充旧目标

有限对照 snapshot：`81c70ddf4e6ded44028f53c230508f49318638b1`，2026-10-08 的公开 commit，来源：[commit](https://api.github.com/repos/trudenboy/sendspin-bt-bridge/commits/81c70ddf4e6ded44028f53c230508f49318638b1)。不继续追移动的 main/latest。

- [pyproject:11](https://github.com/trudenboy/sendspin-bt-bridge/blob/81c70ddf4e6ded44028f53c230508f49318638b1/pyproject.toml#L11) 要求 `aiosendspin==9.1.1`；旧 CLI 依赖路径已换成自有播放器。
- [bridge_daemon:147-148](https://github.com/trudenboy/sendspin-bt-bridge/blob/81c70ddf4e6ded44028f53c230508f49318638b1/src/sendspin_bridge/services/ipc/bridge_daemon.py#L147-L148) 构造 `StreamPlayer` / Pulse sink factory；[:329-337](https://github.com/trudenboy/sendspin-bt-bridge/blob/81c70ddf4e6ded44028f53c230508f49318638b1/src/sendspin_bridge/services/ipc/bridge_daemon.py#L329-L337) 给出 retarget 路径。
- [:365-407](https://github.com/trudenboy/sendspin-bt-bridge/blob/81c70ddf4e6ded44028f53c230508f49318638b1/src/sendspin_bridge/services/ipc/bridge_daemon.py#L365-L407) 选择 PLAYER / METADATA / CONTROLLER，直接构造官方 `aiosendspin.client.SendspinClient`；[:211](https://github.com/trudenboy/sendspin-bt-bridge/blob/81c70ddf4e6ded44028f53c230508f49318638b1/src/sendspin_bridge/services/ipc/bridge_daemon.py#L211) 调其 `connect(url)`。
- [:238-268](https://github.com/trudenboy/sendspin-bt-bridge/blob/81c70ddf4e6ded44028f53c230508f49318638b1/src/sendspin_bridge/services/ipc/bridge_daemon.py#L238-L268) 有 pairing store / pairing policy。`require_pairing=False` **不是已证明的明文传输开关**；不从 pairing 禁用推导 Noise 禁用。

## 4. 官方 Python SDK 的固定 wire 证据

| Snapshot | 可复核 wire 行为 | 对候选的意义 |
| --- | --- | --- |
| `aiosendspin 6.1.1` = `af9dbc8b625bffbee99d7a67712c0e3b4c44147d` | [client/client.py:479-490](https://github.com/Sendspin/aiosendspin/blob/af9dbc8b625bffbee99d7a67712c0e3b4c44147d/aiosendspin/client/client.py#L479-L490) 建 reader 后发送 `client/hello`；[:774-802](https://github.com/Sendspin/aiosendspin/blob/af9dbc8b625bffbee99d7a67712c0e3b4c44147d/aiosendspin/client/client.py#L774-L802) 使用 `version=1`、角色/支持字段以及 `ws.send_str` | 是旧明文候选的来源，不代表实际 Bridge 包安装就是这个 snapshot |
| 同一 6.1.1 | [models/types.py:55-70](https://github.com/Sendspin/aiosendspin/blob/af9dbc8b625bffbee99d7a67712c0e3b4c44147d/aiosendspin/models/types.py#L55-L70) 的角色为 `player@v1` / `metadata@v1` / `controller@v1`；[models/core.py:124](https://github.com/Sendspin/aiosendspin/blob/af9dbc8b625bffbee99d7a67712c0e3b4c44147d/aiosendspin/models/core.py#L124) 的 player support alias 与 [:214](https://github.com/Sendspin/aiosendspin/blob/af9dbc8b625bffbee99d7a67712c0e3b4c44147d/aiosendspin/models/core.py#L214) 的 alias 序列化 | 比较必须按实际 wire alias / 角色字符串，而非只看类的字段名 |
| `aiosendspin 9.1.1` = `5c024b42893bc6e372f6c87bb8504552051daf24` | [server/connection.py:880-913](https://github.com/Sendspin/aiosendspin/blob/5c024b42893bc6e372f6c87bb8504552051daf24/aiosendspin/server/connection.py#L880-L913) 分 `client/init` → Noise 与获准的 `client/hello` 明文入口；[server/server.py:157](https://github.com/Sendspin/aiosendspin/blob/5c024b42893bc6e372f6c87bb8504552051daf24/aiosendspin/server/server.py#L157) 默认 `allow_unencrypted=False` | 真实 server 有 legacy 准入能力，但需要具体宿主 wrapper/config 允许 |
| 同一 9.1.1 | [client/connection.py:330-372](https://github.com/Sendspin/aiosendspin/blob/5c024b42893bc6e372f6c87bb8504552051daf24/aiosendspin/client/connection.py#L330-L372) 默认客户端走 Noise 后再发送内部 hello；该路径没有旧明文 fallback | 当前 Bridge 使用该官方 client 的默认路径，与旧 Go 首包契约不相同；不是对旧 Bridge 2.75 的判决 |

MA 固定 [provider.py:406-408](https://github.com/music-assistant/server/blob/e30a4974ba951f38e21bea8d502af3b903df992c/music_assistant/providers/sendspin/provider.py#L406-L408) 给 `allow_legacy_clients` 默认 true；[:462-470](https://github.com/music-assistant/server/blob/e30a4974ba951f38e21bea8d502af3b903df992c/music_assistant/providers/sendspin/provider.py#L462-L470) 同时传给 `allow_unencrypted` / `allow_noncompliant_clients`。[:1063-1066](https://github.com/music-assistant/server/blob/e30a4974ba951f38e21bea8d502af3b903df992c/music_assistant/providers/sendspin/provider.py#L1063-L1066) 启动实际 SDK server，公共 [constants.py:134](https://github.com/music-assistant/server/blob/e30a4974ba951f38e21bea8d502af3b903df992c/music_assistant/constants.py#L134) 定义端口 `8927`。这些是**可复用 server / legacy admission 能力**，不是读取到的 runtime 配置或端到端证据。

## 5. Go SDK / Go CLI、有限矩阵与标准边界

### 5.1 官方 Go SDK 与 CLI 是真实 server，但版本和职责分开

- SDK `v1.8.2` 固定 commit：`830294f67f2a19607a7cdab0e11a3a3f71bd2749`；CLI `v0.1.1` 固定 commit：`94836a9672060b49170b51d9853582c8e76aa149`。版本到 commit 来自官方 [SDK tags API](https://api.github.com/repos/Sendspin/sendspin-go/tags) / [CLI tags API](https://api.github.com/repos/Sendspin/sendspin-go-server/tags)，下列 source 链接全部按 commit 固定。
- CLI [go.mod:6](https://github.com/Sendspin/sendspin-go-server/blob/94836a9672060b49170b51d9853582c8e76aa149/go.mod#L1-L10) 明确依赖 `github.com/Sendspin/sendspin-go v1.8.2`。这是 CLI source 的实际 module lock，不拿 SDK 移动 HEAD 替换它；本机 CLI/SDK installed 版本未知。
- CLI [main.go:86-104](https://github.com/Sendspin/sendspin-go-server/blob/94836a9672060b49170b51d9853582c8e76aa149/main.go#L86-L104) 选择音频 source 并构造 server，[:147-150](https://github.com/Sendspin/sendspin-go-server/blob/94836a9672060b49170b51d9853582c8e76aa149/main.go#L147-L150) 调 `Start`，[:165](https://github.com/Sendspin/sendspin-go-server/blob/94836a9672060b49170b51d9853582c8e76aa149/main.go#L165) 调 `Stop`。不是仅有协议类型或 client 的 SDK。
- SDK [server.go:201](https://github.com/Sendspin/sendspin-go/blob/830294f67f2a19607a7cdab0e11a3a3f71bd2749/pkg/sendspin/server.go#L201) 注册 `/sendspin`；[:348-360](https://github.com/Sendspin/sendspin-go/blob/830294f67f2a19607a7cdab0e11a3a3f71bd2749/pkg/sendspin/server.go#L348-L360) 首先解析 JSON 并要求 `client/hello`，[:416-424](https://github.com/Sendspin/sendspin-go/blob/830294f67f2a19607a7cdab0e11a3a3f71bd2749/pkg/sendspin/server.go#L416-L424) 回 hello version/roles；[constants.go:7](https://github.com/Sendspin/sendspin-go/blob/830294f67f2a19607a7cdab0e11a3a3f71bd2749/pkg/sendspin/constants.go#L7) 定义 `ProtocolVersion=1`。该固定入口是旧明文契约，不是 Noise `client/init` 入口。
- 可复用 ingress 是 [source.go:5-21](https://github.com/Sendspin/sendspin-go/blob/830294f67f2a19607a7cdab0e11a3a3f71bd2749/pkg/sendspin/source.go#L5-L21) 的 `AudioSource`：`Read([]int32)`、`SampleRate()`、`Channels()`、`Metadata()`、`Close()`。已有 [server_stream.go:13-48](https://github.com/Sendspin/sendspin-go/blob/830294f67f2a19607a7cdab0e11a3a3f71bd2749/pkg/sendspin/server_stream.go#L13-L48) 定时读取 source，[:79-131](https://github.com/Sendspin/sendspin-go/blob/830294f67f2a19607a7cdab0e11a3a3f71bd2749/pkg/sendspin/server_stream.go#L79-L131) 编码并发送 binary。应复用该 server/ingress，而不是重写协议。
- 未核实其队列/队列所有权集成 API，也没有证据证明 CLI 已实现 Navidrome 曲库、统一播放权威、输出就绪门控、持久化或应用级远端控制。PCM ingress 不等于这些应用能力。

6.1.1 Python client 与此 Go server 都使用首包 `client/hello`、core version `1` 及旧角色/支持字段，形成**有限 legacy 原型的静态交集**。额外 CONTROLLER 角色、协商结果、音频格式及后续命令仍须验证；不能把首包相似改写成 Bridge `2.75.0` 已成功互通。

### 5.2 最小候选矩阵

矩阵的“准入”仅决定将来哪些固定组合值得做经批准的原型，不授权运行操作。artifact 依第 2 节的 RepoDigest 锁；source build 必须另行取得可复现依赖与 artifact provenance。

| 候选 | server 锁 | Bridge / client 锁 | 有限决策与未决 gate |
| --- | --- | --- | --- |
| 历史 baseline | MA `2.10.4` 的现有 RepoDigest，revision `e30a4974…` | 现有 Bridge `2.75.0` RepoDigest，revision `0f2cd9ab…` | **保留作为比较/回滚候选**，当前都 exited；source 显示 MA 有 legacy 准入，Bridge source 预期 SDK 6.1.1。installed、有效配置与历史/未来互通未知；不得擅自恢复 |
| 第一 Go 私用 legacy 原型 | Go CLI `v0.1.1@94836a96…` → SDK `v1.8.2@830294f6…` | 优先相同旧 Bridge artifact；若另建旧 source，则明确 `v2.75.0@126e766d…` 与 SDK 6.1.1 lock，不称它等同原 image | **可进入有限原型设计**，依据首包/旧角色静态交集；先确认真实依赖和 handshake，之后才讨论音频/输出与唯一权威。没有协议成功声明 |
| 新协议参考比较 | MA 相同固定 artifact / SDK 9.1.1 source | Bridge `81c70ddf…`，source 声明 SDK 9.1.1 | **只作为第二组 source-pinned 对照**；双方有 Noise 路径，但 pairing、角色、输出与互通未知。新 image/部署不在本轮获准范围 |
| 官方 Python 参考 server | `aiosendspin[server] 9.1.1@5c024b42…` 的真实 server 实现 | 同一新 Bridge source snapshot；或在单独明确 legacy server policy 下比较旧 client | **可复用为协议/问题隔离参考**，不是另造完整 MA，也不是无需 wrapper 的 Navidrome 产品。server 默认拒绝明文，不偷换为 MA 的 default override |
| Go 旧入口 + 新 Bridge 默认 client | 相同固定 Go CLI / SDK | Bridge `81c70ddf…` → SDK 9.1.1 默认 client | **不纳入第一轮直接接线候选**：`client/init`/Noise 与 first `client/hello` 不同。先证明显式旧模式或单独估安全/wire 升级；不据此否定旧 Bridge 2.75，也未实测全配置不兼容 |

### 5.3 官方 Sendspin RC1 与完整符合性边界

规范仅固定一个参考版本：`1.0.0-rc1` / commit `671a34d408581fc25ea56b3528a4a3f13e3be901`。软件 Go `v1.8.2`、SDK `9.1.1`、hello `version=1` 均不等于符合 Sendspin 1.0。

- 固定 [connection.md:66-89](https://github.com/Sendspin/spec/blob/671a34d408581fc25ea56b3528a4a3f13e3be901/connection.md#L66-L89) 要求 Noise `KKpsk2`；[messaging.md:5-16](https://github.com/Sendspin/spec/blob/671a34d408581fc25ea56b3528a4a3f13e3be901/messaging.md#L5-L16) 给出安全建链/消息顺序。这与 Go 固定 legacy 首包入口不同，不能以它处理旧明文音频就认证 RC1 安全符合性。
- [messaging.md:146-158](https://github.com/Sendspin/spec/blob/671a34d408581fc25ea56b3528a4a3f13e3be901/messaging.md#L146-L158) 的 init core version 也为 `1`；数字相同不抹平 `client/init` 与 `client/hello`、加密与明文、角色/wire revision 的差异。
- [RC1 messaging.md:219](https://github.com/Sendspin/spec/blob/671a34d408581fc25ea56b3528a4a3f13e3be901/messaging.md#L219) 的 `supported_pair_methods` 为 object/map；固定 [aiosendspin 9.1.1 models/core.py:169](https://github.com/Sendspin/aiosendspin/blob/5c024b42893bc6e372f6c87bb8504552051daf24/aiosendspin/models/core.py#L169) 对应字段是 list。至少存在需逐项校核的 snapshot 差异；不能从“有 Noise”推导 SDK/MA/Bridge 已完整符合该 RC1。
- [官方 SCOPE](https://github.com/Sendspin/spec/blob/671a34d408581fc25ea56b3528a4a3f13e3be901/SCOPE.md) 是该固定规范的范围入口。本票不重跑许可证，也不扩成完整合规审计；任何完整 1.0 安全、适用角色和 wire MUST 的声明都需要独立范围与证据。

因此 Go 路线的第一问题是“旧 client 下能否在受限私用范围复用真实 server，且保持一个播放权威”，不是“把首轮原型命名为完整 Sendspin 1.0 合规服务器”。

## 6. 明确未知与下一步 stop gates

- 实际 installed `aiosendspin` / `sendspin` / 音频依赖、image build provenance、历史 boot/log 版本未知；source 声明和 release workflow 不能替代它们。当前没有获准读取容器 filesystem 的途径，不执行 `exec/cp/export`。
- MA 当前 legacy 准入、pairing/角色协商结果、Bridge 实际采用的 client、双方端点与客户端方向未知；宿主 key presence 不等同有效运行值。
- 两个 Navidrome 的实际供库关系、MA/Go 的媒体入口、统一队列/曲目推进权威、重启持久化和控制端语义不由本票的 wire 静态阅读证明。
- 旧 Go + 旧 Bridge 是否成功握手、codec/时钟/流取消/重连行为、首段音频、输出准备/释放、可听就绪时间全部未验证。
- 新 Bridge + Go 的 Noise 默认路径差异应先停在设计/协议 gate：证明显式可用的 legacy 模式，或另估 Go 安全/wire 升级；不做无界兼容修补，也不直接给 production Go 服务器打开不安全例外。
- 后续任何互通/恢复 baseline/启动隔离服务/读取运行依赖/占用音箱都需要独立批准，说明具体窗口、命令、读写面、退出与回滚步骤。此研究票的结论不是执行授权。
- 原型前固定 artifact 或 source + dependency lock；若新 build 不能产出可追溯 provenance，应停止版本比较，不能只记录 `latest` 或 tag。
- 基础私用原型仅在明确接受 legacy 安全边界的隔离范围进入；若要求完整 Sendspin 1.0，先重新界定安全、角色和 wire 符合性范围，再估工程与维护成本，不能用一次声音演示替代。
- 硬件验收仍由后续独占窗口的实验给出；首次可听与首段完整、取消过期任务、暂停/停止/待机/唤醒、唯一播放权威及回滚各有独立 gate。本报告不提供达到 10 秒或 15 秒指标的声明。

## 7. 文档获取方法

按要求先尝试 Context7：Bridge resolve 失败；Music Assistant resolve 得到 `/music-assistant/server`，但该问题的一次 query-docs fetch 失败。本票的公共 Go/官方协议后台研究先以 `sendspin-go` 和 Go CLI/wire/Noise/RC1 问题 resolve，结果也是 `TypeError: fetch failed`，未取得 library ID，未调用 query-docs。之后使用固定 commit 的官方 source / GitHub tag API，不借二手文章补齐；没有声称 Context7 提供了 version-specific 证据。后台代理只研究公共 primary sources、未写文件或操作 runtime，其有限结果已合入本资产；所有本机受阻操作没有转交他人。
