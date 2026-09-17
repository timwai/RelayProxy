# Linux 透明代理与容器部署

Linux Agent 使用 `mangle/OUTPUT -> NFQUEUE` 处理 TCP、UDP 和 IPv4/IPv6。DIRECT 包直接返回 `NF_ACCEPT`，不复制包体回内核；PROXY/REJECT 包返回 `NF_DROP`，需要回复时通过带 `SO_MARK` 的 raw socket 注入，iptables 的 mark 规则会跳过这些注入包，避免递归捕获。INPUT 只观察 UDP/53 响应，用于域名与目标 IP 的保守关联，不把普通下载流量送进用户态。

## 主机运行

前置条件：

- root，或同时拥有 `CAP_NET_ADMIN`、`CAP_NET_RAW`；
- 内核启用 `NETFILTER_NETLINK_QUEUE`；
- 安装 `iptables` 与 `ip6tables`。发行版提供的 nftables 兼容前端可以使用；
- `/proc` 可见。进程归属通过 `/proc/net/{tcp,udp,tcp6,udp6}` 与 `/proc/<pid>/fd` 解析，并按五元组短期缓存。

配置 `network.mode: divert` 后启动 Agent。Agent 创建独立的 `RELAYPROXY_OUT` / `RELAYPROXY_IN` chain；正常退出时删除规则。规则使用 `--queue-bypass`，因此进程崩溃或 NFQUEUE 未绑定时不会把主机网络永久黑洞。

## Docker Compose

使用仓库根目录的 `docker-compose.agent.yml`。运行时镜像只包含 CA 与 iptables 工具，以下内容全部在宿主机：

- `./bin/relay-agent` -> 容器 `/opt/relayproxy/bin/relay-agent`（只读）；
- `./config/relay-agent.yaml` -> 容器 `/data/relay-agent.yaml`（目录可写，配对保存需要原子替换）。

执行：

```bash
chmod +x ./bin/relay-agent
docker compose -f docker-compose.agent.yml up -d --build
```

Compose 使用 host network/PID namespace 和 `NET_ADMIN`、`NET_RAW`，因此透明代理作用于宿主机网络。若 Web 管理需要从其他机器访问，请在外置配置中设置非回环 `web.listen`，并必须配置至少 32 字节的随机 `web.token`；默认页面仅监听 `127.0.0.1:9090`。内置 Web 服务是 HTTP，跨不可信网络时应放在 HTTPS 反向代理或 SSH 隧道后，避免 token 明文传输。

## 无桌面环境配对

容器启动后，Web 管理默认位于宿主机 `http://127.0.0.1:9090/`，页面与 Windows GUI 相同，可直接填写服务端管理地址、一次性配对码、设备名和模式完成配对。远程 Linux 主机保持默认回环监听时，可在管理电脑建立隧道：

```bash
ssh -L 9090:127.0.0.1:9090 user@linux-host
```

随后在管理电脑打开 `http://127.0.0.1:9090/`。配对凭据会通过 `/data` 目录内的临时文件和原子替换写回宿主机 `./config/relay-agent.yaml`，因此不要把单个 YAML 文件直接设为 bind mount。
