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

使用仓库根目录的 `docker-compose.agent.yml`。运行时镜像包含 CA 证书、iptables、nftables 和 iproute2，支持本机透明代理及局域网网关，以下内容全部在宿主机：

- `./bin/relay-agent` -> 容器 `/opt/relayproxy/bin/relay-agent`（只读）；
- `./config/relay-agent.yaml` -> 容器 `/data/relay-agent.yaml`（目录可写，配对保存需要原子替换）。

执行：

```bash
chmod +x ./bin/relay-agent
docker compose -f docker-compose.agent.yml up -d --build
```

Compose 使用 host network/PID namespace 和 `NET_ADMIN`、`NET_RAW`，因此透明代理作用于宿主机网络。若 Web 管理需要从其他机器访问，请在外置配置中设置非回环 `web.listen`，并必须配置至少 32 字节的随机 `web.token`；默认页面仅监听 `127.0.0.1:9090`。内置 Web 服务是 HTTP，跨不可信网络时应放在 HTTPS 反向代理或 SSH 隧道后，避免 token 明文传输。

启用局域网网关时，`gateway.interface` 使用 Linux 宿主机的 LAN 网卡名；配置与接入方法见[网关使用说明](gateway-and-groups-usage.md)。更新 Dockerfile 后需要执行上述 `up -d --build` 重建镜像并重建容器，仅 `restart` 不会安装新增依赖。

## 无桌面环境配对

容器启动后，Web 管理默认位于宿主机 `http://127.0.0.1:9090/`，页面与 Windows GUI 相同，可直接填写服务端管理地址、一次性配对码、设备名和模式完成配对。远程 Linux 主机保持默认回环监听时，可在管理电脑建立隧道：

```bash
ssh -L 9090:127.0.0.1:9090 user@linux-host
```

随后在管理电脑打开 `http://127.0.0.1:9090/`。配对凭据会通过 `/data` 目录内的临时文件和原子替换写回宿主机 `./config/relay-agent.yaml`，因此不要把单个 YAML 文件直接设为 bind mount。
