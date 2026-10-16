# RDP 公网入口：GeoIP 非中国大陆来源永久封禁

> 仅作用于 **Server 公网 RDP TCP / UDP 入口**。不影响经过认证的 Agent、P2P 或普通代理隧道，也不代表 Windows 登录成功。

## 配置（Server Web）

打开 **服务配置 → RDP 公网入口**，设置：

| 字段 | 默认值 | 用途 |
| --- | --- | --- |
| 启用 GeoIP 非中国大陆封禁 | 关闭 | 显式开启后才执行 GeoIP 地域判定和新封禁 |
| GeoIP 规则下载地址 | `https://cdn.jsdelivr.net/gh/Loyalsoldier/v2ray-rules-dat@release/geoip.dat` | HTTPS 可配置，支持重定向到公开 HTTPS 主机 |
| GeoIP 规则更新间隔（小时） | 24 | 支持 1–720 小时 |

也可使用 YAML：`rdp.geoip.enabled`、`rdp.geoip.source_url`、`rdp.geoip.update_hours`。**Web 保存配置后需重启 Server 才生效**；之后定期自动更新成功即可热加载，无需再重启。实时生效状态、最近更新时间、下一次计划时间、最近错误及手动「立即更新规则」位于 **IP 安全策略 → GeoIP 国家限制**。

## 下载、缓存和失败回退

开启后优先读取已经校验的本地 `data/geoip.dat`。无可用缓存时，启动流程必须成功从规则源下载并完整解析，否则 Server 拒绝启动，避免错误宣称防护已开启。缓存有效时，离线仍可启动，缓存超过设置的更新间隔会异步刷新。

下载使用 HTTPS、响应大小与超时限制、公开 IP 校验、禁用环境代理，阻断通过私人/保留网络地址和 DNS 绑定重定向访问内网。数据写入同一目录的临时文件，解析成功后才原子覆盖缓存；下载或校验失败均保留最后可用规则。可选使用 `RELAY_RDP_GEOIP_DB` 指定缓存路径（.dat/.csv/.csv.gz），自定义源必须与缓存格式匹配。规则数据不会打包进 RelayProxy 发布包。

本功能使用 V2Ray GeoIPList Protobuf 格式解析 `geoip.dat`，也兼容 DB-IP Country Lite CSV / CSV.GZ。数据来源：[Loyalsoldier/v2ray-rules-dat](https://github.com/Loyalsoldier/v2ray-rules-dat)。

## 连接判定

- CN 地址通过国家检查，但仍受原有手动封禁、白名单、授权等策略约束。
- 明确命中其他国家/地区（包括 HK/MO/TW）的公网 IP 首次连接被拒绝，自动写入全局 RDP 永久封禁 `GEO_NON_CN:XX`，重启后继续有效。
- 未被数据集覆盖的公网 IP：拒绝连接但不**自动永久封禁**；`ZZ` 归为未知。
- 私网/回环地址不因国家判断自动封禁。
- 规则条目存在重叠时，优先考虑 CN 归属；IP 国家数据库有误差，VPN/代理的出口国家也可能影响判断。
- 关闭开关时既有 GeoIP 自动封禁记录保留用于审计，但 GeoIP 自动封禁策略不再执行；其他手动封禁与防护仍继续生效。

## 验收

`go test ./server/rdp ./server/repository ./server/api`；包含 Protobuf CIDR 匹配、损坏文件、缓存失败回退、IPv4/IPv6、禁用后策略关闭、永久封禁、Web 管理权限、配置持久化及重启语义。
