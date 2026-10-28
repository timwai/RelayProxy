# Linux 网关与设备分组使用说明

## 设备分组（Windows、macOS、Linux、Android）

在「设备中心 → 设备分组」（Android 为「节点 → 管理设备分组」）创建分组，加入已授权的 Agent 设备，并用上下箭头设置顺序。分组按稳定 ID 引用，改名不影响配置；同一设备可加入多个分组，不支持嵌套或将本机自定义代理加入设备分组。

默认代理出口、分流规则、规则订阅、DNS 专用出口、Linux 网关默认出口及桌面/Web 的出口共享上游均可引用分组。Android 现有默认出口、规则和订阅选择器均支持分组。

每个新 TCP 连接或 UDP 关联依次尝试在线、已授权的成员。建立失败时切换下一成员；权限拒绝不绕过。成员恢复后，新连接重新优先选择首个成员。已建立连接保持原成员，不迁移。全部不可用时返回错误，绝不切换到组外设备或本机直连。连接监控保留分组 ID，并记录实际使用的设备 ID。

共享上游的选择仍属于启动配置，修改后重启 Agent。共享上游引用分组时，运行中的成员和顺序变更即时应用。避免在同一链路中互相指定设备；新版本在第 4 跳拒绝循环（转发链中的 Agent 和 Relay Server 均应升级）。

配置示例：

```yaml
proxy:
  default_exit_id: group:home
routing:
  mode: rule
  default_action: PROXY
  device_groups:
    - id: group:home
      name: 家庭主备
      members:
        - primary-agent-device-id
        - backup-agent-device-id
  rules:
    - name: 客厅设备
      enabled: true
      entries: [gateway]
      source_cidrs: [192.168.50.20/32]
      action: PROXY
      exit_id: group:home
```

## Linux 网关

需要 Linux、root 或相应网络管理权限，以及 `nft`（nftables）和 `ip`（iproute2）。内核须支持 nftables TPROXY 与策略路由。提供 IPv4 TCP、UDP、UDP/TCP DNS；不提供 DHCP、IPv6 转发、ICMP 转发或 FakeIP。网关接入本身不需要 Server 审批；使用远程 Agent 出口仍遵循已有代理授权。只有 DIRECT 规则和本机自定义出口可在 Relay 不可用时独立使用。

1. 给 Linux Agent 的 LAN 网卡配置固定 IPv4 地址，例如 `192.168.50.2`。
2. 在「网络配置 → 局域网网关」指定 LAN 网卡（例如 `eth0`）、允许接入来源网段和默认出口，启用并保存。或写入如下 YAML 后以 root 启动 Agent。
3. 将接入设备的默认网关和 DNS 都设为 `192.168.50.2`；使用静态配置或在现有路由器 DHCP 设置中下发。本功能不修改 DHCP，也不修改网卡地址。
4. 网关流量复用公共分流规则，可用 `entries: [gateway]` 和 `source_cidrs` 匹配；规则模式下按顺序首次匹配，不另建网关规则集。

```yaml
gateway:
  enabled: true
  interface: eth0
  source_cidrs: [192.168.50.0/24]
  exit_id: group:home   # 为空则跟随 Agent 默认出口
  port: 15080          # Agent 内部透明监听端口，不是客户端代理端口
  max_connections: 1024
```

启动：

```sh
sudo ./relay-agent-linux-amd64 --config ./relay-agent.yaml --no-gui
```

### Alpine / Docker

Alpine 需要安装 `nftables` 和完整的 `iproute2`；仅安装 `iptables` 不提供 `nft`，BusyBox 自带的 `ip` 也不足以管理网关策略路由：

```sh
apk add --no-cache nftables iproute2
command -v nft
ip -Version
```

若自行设置了 `PATH`，应包含 `/usr/sbin` 和 `/sbin`。Agent 自行管理网关规则，无需启动 Alpine 的 nftables 服务。

仓库的 `docker/agent/Dockerfile` 已包含上述依赖。Linux 宿主机使用现有 Compose 配置（host network、root、`NET_ADMIN` 和 `NET_RAW`），更新后执行：

```sh
docker compose -f docker-compose.agent.yml up -d --build
```

若旧容器尚能运行，可先以 root 临时安装依赖，再重启 Agent；容器重建后这次安装会丢失，最终应重建镜像。容器在重启循环中时，直接重建镜像和容器。宿主机仍须支持 TPROXY，`gateway.interface` 填宿主机的 LAN 网卡名。

#### 群晖与裁剪内核排查

若报 `netlink: Error: cache initialization failed: Invalid argument`，说明 `nft` 已能执行，但与宿主机的 nftables 内核接口通信失败。不能仅凭此错误确定是缺少模块还是版本不兼容。先在容器内执行以下只读检查，并记录群晖型号和 DSM 版本：

```sh
uname -r
nft --version
nft list tables
```

Docker 容器共享宿主机内核，安装 Alpine 的 `nftables` 包不代表群晖内核支持 nftables。当前网关使用 nftables TPROXY，上游 Linux 从 4.18 起提供该能力；4.4 等旧内核需厂商回移，较新内核也须启用所需功能。宿主机至少需具备 `NF_TABLES`、`NF_TABLES_INET`、`NFT_TPROXY`、`NFT_FIB_INET` 与 IPv4 策略路由支持。[Linux 内核透明代理文档](https://docs.kernel.org/networking/tproxy.html)说明了 TPROXY 的内核要求。

若 `nft list tables` 报 `Operation not permitted`，先检查 Compose 的 root、`NET_ADMIN` 与 `NET_RAW` 配置；若仍是 `Invalid argument`，需要检查宿主机内核配置、模块和 nft 兼容性。模块可能内建，不能只凭 `lsmod` 未显示就判定缺失。模块存在时应由宿主机管理员加载，容器不自动安装或加载群晖内核模块。

宿主机确实缺少 nftables TPROXY 时，当前版本无法在该宿主机的 Docker 中运行网关；更换容器发行版或增加 `privileged` 无法补齐内核实现。可部署到具备所需功能的 Linux 机器，或使用有独立兼容内核和 LAN 接口的 Linux 虚拟机。当前未提供 iptables 网关后端。

### DNS 与规则生命周期

DNS/53 在配置的 LAN 接口上统一接管，复用现有经过出口的 DoH/DoT 配置；`routing.dns_exit_id` 非空时优先于网关默认出口。查询失败返回 SERVFAIL，无系统 DNS 降级。DNS 结果按来源客户端隔离、受 TTL 和容量限制；同一客户端的 IP 对应多个域名时按 IP 分流。DIRECT 使用已观察到的原始目标 IP，避免在 Agent 上再次解析域名。客户端私有 DoH/DoT 不经过 DNS/53，因此这类流量通常只能按目标 IP 分流。

内核规则只处理选定 LAN 网卡的入站流量，不接管 Agent 自身 OUTPUT，不清空系统规则或启用全局 IP 转发。已有防火墙必须允许所选 LAN 的透明代理流量进入本机（含策略标记 `0x525047`）；其他防火墙的拒绝策略仍然有效。请使用独立 LAN 接口或明确的来源网段，不要配置在不可信公网接口上。

管理独立 nftables 表 `inet relayproxy_gateway`、IPv4 策略路由优先级 `12520`、路由表 `52520` 和 `/run/relayproxy-gateway.lock`。发现命名、路由表或规则冲突时拒绝覆盖。启动前先检查规则并绑定监听，再原子更新 nftables。

Agent 退出、崩溃或出口不可用后，不会回退到内核普通转发；保留的网关 forward guard 阻断来自该接口的转发（含 IPv6 和不支持的协议）。再次启动会接管自己的规则。取消勾选网关并保存是显式关闭，会移除本功能规则。进程已停止时可手动清理：

```sh
sudo ./relay-agent-linux-amd64 --gateway-cleanup
```

## 验证

```sh
go test ./...
go test -race ./agent/client ./agent/routing ./agent/app ./agent/bridge ./agent/gateway ./internal/traffic ./mobile/androidcore
cd agent/gui/frontend && npm test && npm run build
```

Linux 网络验收测试：在安装 `nftables`、`iproute2` 的 **独立特权容器** 中执行以下测试，不能在工作机现有网络上运行。测试创建两个 namespace 和 veth，验证真实透明 TCP/UDP、UDP/TCP DNS、公共来源规则、停止后的阻断、重启接管与关闭清理，不依赖外网。

```sh
RELAYPROXY_GATEWAY_INTEGRATION=1 go test -v ./agent/gateway -run 'TestGatewayNetworkNamespace|TestDNSAssociation|TestGatewayConfig'
```
