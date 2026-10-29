# Linux 透明代理与容器部署

Linux Agent 使用 `mangle/OUTPUT -> NFQUEUE` 处理 TCP、UDP 和 IPv4/IPv6。DIRECT 包直接返回 `NF_ACCEPT`，不复制包体回内核；PROXY/REJECT 包返回 `NF_DROP`，需要回复时通过带 `SO_MARK` 的 raw socket 注入，iptables 的 mark 规则会跳过这些注入包，避免递归捕获。INPUT 只观察 UDP/53 响应，用于域名与目标 IP 的保守关联，不把普通下载流量送进用户态。

## 主机运行

前置条件：

- root，或同时拥有 `CAP_NET_ADMIN`、`CAP_NET_RAW`；
- 内核启用 `NETFILTER_NETLINK_QUEUE` 和 `NETFILTER_XT_TARGET_NFQUEUE`，对应模块已在宿主机加载；
- 安装可用的 IPv4 `iptables` mangle 后端。Agent 先探测默认前端，不可用时尝试 `iptables-legacy`、`iptables-nft`；IPv6 单独探测 `ip6tables`、`ip6tables-legacy`、`ip6tables-nft`，不修改系统 alternatives；
- `/proc` 可见。进程归属通过 `/proc/net/{tcp,udp,tcp6,udp6}` 与 `/proc/<pid>/fd` 解析，并按五元组短期缓存。

配置 `network.mode: divert` 后启动 Agent。Agent 创建独立的 `RELAYPROXY_OUT` / `RELAYPROXY_IN` chain；正常退出时删除规则。规则使用 `--queue-bypass`，因此进程崩溃或 NFQUEUE 未绑定时不会把主机网络永久黑洞。

IPv6 是可选能力。缺少 IPv6 防火墙前端、监听或回注能力时，Agent 仍可接管 IPv4；IPv6 规则安装失败也不会撤销正常工作的 IPv4 规则。管理页显示实际接管的协议范围，此时 IPv6 流量不受透明代理接管。

也可在 Agent Web 管理页（默认 `http://127.0.0.1:9090/`）打开「接入」→「Linux 透明代理」，启用「接管系统流量」并保存，然后重启 Agent 生效。该卡片会显示运行条件、接管状态、抓包后端和错误；权限或依赖不足时仍显示入口，并说明无法启用的原因。局域网网关位于同一页，单独控制 LAN 设备的接入。只改本机透明代理或本地监听设置时，不提交网关配置，也不执行网关的 nftables 清理；网关自己的 nftables / TPROXY 依赖不会阻止本机透明代理开关保存。

## 旧内核 NAS

容器共享宿主机内核。若 `nft list tables` 报 `Invalid argument`，但 `iptables -t mangle -S` 或 `iptables-legacy -t mangle -S` 正常，本机 NFQUEUE 透明代理仍可使用可用的 iptables 后端；局域网网关使用的 nftables / TPROXY 需要另外满足内核条件。

NFQUEUE 模块需在 NAS 宿主机加载，例如：

```bash
sudo modprobe nfnetlink_queue
sudo modprobe xt_NFQUEUE
```

若 NAS 的模块目录不符合 modprobe 的查找布局，应按厂商的模块管理方式加载匹配当前内核的 `.ko` 文件及依赖。重新启动后的自动加载也由宿主机负责；重装容器内的 nftables 不会补齐宿主机内核功能。

## Docker Compose

使用仓库根目录的 `docker-compose.agent.yml`。运行时镜像包含 CA 证书、iptables、nftables 和 iproute2；静态 Linux Agent 程序与完整配置目录从宿主机挂载：

- `./bin/relay-agent` -> 容器 `/opt/relayproxy/bin/relay-agent`（只读）；
- `./config/` -> 容器 `/data/`（可写）。`relay-agent.yaml`、`device-identity.json` 和其他本地状态都保存在此目录，重建容器不会丢失设备身份；不要只挂载单个 YAML 文件。

先把对应宿主机架构的新版 `relay-agent` 放入 `./bin/`，再执行：

```bash
mkdir -p ./bin ./config
cp .env.agent.example .env.agent
chmod +x ./bin/relay-agent
# 编辑 .env.agent，填写服务器、设备身份和其他所需参数
docker compose --env-file .env.agent -f docker-compose.agent.yml up -d --build
docker compose --env-file .env.agent -f docker-compose.agent.yml logs -f relay-agent
```

`.env.agent` 可配置以下变量；布尔值使用 `true` / `false`（也接受 `1` / `0`），端口范围为 1–65535：

| 环境变量 | 含义 |
| --- | --- |
| `RELAY_AGENT_BIN_DIR` | 宿主机程序目录，默认 `./bin`，只读挂载 |
| `RELAY_AGENT_CONFIG_DIR` | 宿主机配置目录，默认 `./config`；Compose 将其挂载到容器 `/data` |
| `RELAY_AGENT_IMAGE` | 运行时镜像名，默认 `relayproxy-agent-runtime:alpine` |
| `RELAY_AGENT_SERVER` | Server 域名或 IP，不含协议和端口 |
| `RELAY_AGENT_SERVER_TCP_PORT` / `RELAY_AGENT_SERVER_QUIC_PORT` | Server TCP / QUIC 端口 |
| `RELAY_AGENT_SERVER_TLS_ENABLED` / `RELAY_AGENT_INSECURE_TLS` | TLS 开关 / 跳过证书验证；通常保持默认值 |
| `RELAY_AGENT_DEVICE_NAME` / `RELAY_AGENT_IDENTITY_ID` | 设备名 / Server 生成的 16 位身份 ID |
| `RELAY_AGENT_TRANSPORT_MODE` | `auto` / `quic_only` / `tcp_only` |
| `RELAY_AGENT_DEFAULT_EXIT` | 默认出口 ID |
| `RELAY_AGENT_TRANSPARENT_PROXY` | 本机透明代理开关；与局域网网关独立 |
| `RELAY_AGENT_SOCKS5_ENABLED` / `RELAY_AGENT_SOCKS5_LISTEN` / `RELAY_AGENT_SOCKS5_PORT` | SOCKS5 开关、监听 IP、端口 |
| `RELAY_AGENT_HTTP_ENABLED` / `RELAY_AGENT_HTTP_LISTEN` / `RELAY_AGENT_HTTP_PORT` | HTTP 代理开关、监听 IP、端口 |
| `RELAY_AGENT_WEB_ENABLED` / `RELAY_AGENT_WEB_LISTEN` / `RELAY_AGENT_WEB_PORT` / `RELAY_AGENT_WEB_TOKEN` | Web 管理开关、监听 IP、端口、访问 Token |

未设置或留空的参数保留 YAML 中的值；首次运行使用 Agent 默认值。非空环境变量在每次启动时覆盖对应 YAML 字段，经校验后原子保存，Web 页面读取同一份配置。比如 `RELAY_AGENT_TRANSPARENT_PROXY=false` 可显式关闭透明代理，留空则保留当前开关。若希望以后由 Web 页面管理某项，修改后应将相应环境变量留空；留空不会清除已保存的值，需要清除字符串时请修改 YAML 或管理页。复杂分流规则与局域网网关参数继续在 YAML 或管理页配置。

环境变量需要新版 Agent 支持，仅重建运行时镜像不会更新外挂程序。程序本身也支持 `RELAY_AGENT_CONFIG_DIR` 选择配置目录；Compose 内固定为 `/data`，在宿主机启动程序时则指实际目录。显式 `--config` / `--config-dir` 优先于环境变量目录，已有 CLI 参数仍可覆盖本次运行设置。

Compose 使用 host network/PID namespace 和 `NET_ADMIN`、`NET_RAW`，因此透明代理作用于宿主机网络。默认 Web 页面仅监听 `127.0.0.1:9090`。需要局域网管理时，设置 `RELAY_AGENT_WEB_LISTEN=0.0.0.0` 和至少 32 字节的随机 `RELAY_AGENT_WEB_TOKEN`；没有环境变量时也可设置外置 YAML 的 `web.listen` / `web.token`。包含 `$`、`#` 的值在 `.env.agent` 中用单引号包裹。`.env.agent` 已加入 Git 忽略。内置 Web 服务是 HTTP，跨不可信网络时应放在 HTTPS 反向代理或 SSH 隧道后。

启用局域网网关时，`gateway.interface` 使用 Linux 宿主机的 LAN 网卡名；配置与接入方法见[网关使用说明](gateway-and-groups-usage.md)。修改 `.env.agent` 后重新执行上述 `up -d` 以重建容器并注入变量；`restart` 不会读取新环境变量。更新 Dockerfile 后使用 `up -d --build`。

## 无桌面环境配对

容器启动后，Web 管理默认位于宿主机 `http://127.0.0.1:9090/`，页面与 Windows GUI 相同，可填写 Server 地址、服务端生成的设备身份 ID 和设备名，然后在服务端审批设备。也可通过上述环境变量预先填写。远程 Linux 主机保持默认回环监听时，可在管理电脑建立隧道：

```bash
ssh -L 9090:127.0.0.1:9090 user@linux-host
```

随后在管理电脑打开 `http://127.0.0.1:9090/`。配对凭据会通过 `/data` 目录内的临时文件和原子替换写回宿主机 `./config/relay-agent.yaml`，因此不要把单个 YAML 文件直接设为 bind mount。
