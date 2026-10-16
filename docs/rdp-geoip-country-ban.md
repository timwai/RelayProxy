# RDP 公网入口：非中国大陆 IP 永久封禁

此规则仅作用于 **Server 公网 RDP TCP/UDP 入口**；不影响已认证 Agent、代理或 P2P 流量。数据完全在 Server 本地查询，连接时不发起第三方 HTTP 请求。

## 推荐数据源：Loyalsoldier geoip.dat

1. 下载 [Loyalsoldier/v2ray-rules-dat geoip.dat](https://cdn.jsdelivr.net/gh/Loyalsoldier/v2ray-rules-dat@release/geoip.dat)，保存到 Server 的工作目录 **`data/geoip.dat`**。
2. 或通过 `RELAY_RDP_GEOIP_DB=/absolute/path/geoip.dat` 指定文件。原有 DB-IP Country Lite `.csv`、`.csv.gz` 仍兼容：只有未找到 `data/geoip.dat` 时才读取默认 `data/dbip-country-lite-latest.csv.gz`。
3. 重启 Server，查看日志是否出现 `country policy active`。显式指定的文件不存在、损坏、缺失 `CN` 分类时 Server 拒绝启动。
4. 项目上游每日更新，建议在维护窗口定期下载并**校验完整文件**（必要时核对同源 `.sha256sum`），原子替换本地数据后重启 Server。运行中数据库不会被网络请求自动替换。

项目源：https://github.com/Loyalsoldier/v2ray-rules-dat 。规则与生成数据的许可和使用条款以其上游说明为准。此功能并不把上游数据内嵌或重新分发至 RelayProxy 安装包。

## 判断规则

- 使用 V2Ray GeoIPList Protobuf 格式读取 ISO 国家/地区分类 CIDR（IPv4/IPv6），跳过特殊功能类别。
- 如果源 IP 落入 `CN` 类别则认为是中国大陆 IP，允许通过地理策略；如果类别存在交叠，`CN` 优先，降低误封风险。
- 如果明确匹配非 `CN` 国家/地区（包括 `HK`、`MO`、`TW`）并且是公网 IP，首次连接即拒绝，持久化为全局永久封禁（`expiresAt=null`、`GEO_NON_CN:XX`）。适用于全部 RDP TCP/UDP 入口且 Server 重启依然生效。
- 如果源 IP 不在任何已识别国家/地区分类中，拒绝连接但**不永久封禁**（`GEO_COUNTRY_UNKNOWN`）；`ZZ` 类别同样视为未知。特殊用途、私网、回环地址不触发国家策略永久封禁。
- 原有手动封禁优先于 GeoIP。普通频率限速白名单不能绕过 GeoIP，已封禁 IP 可由管理员在 IP 安全策略手动解封，但仍在非 `CN` 规则匹配时可能再次封禁。
- IP 所属国家来自规则文件而不是对来源者物理位置的证明；VPN、云服务、运营商路由变更和 GeoIP 误差均可能影响结论。
- 审计中 `sourceCountry` 为国家/地区代码，未匹配为“未识别”；连接是否被转发不代表远程 Windows 登录成功。

## 验证

不增加运行时 Go 第三方依赖。测试：`go test ./server/rdp ./server/repository ./server/api`；需覆盖真实格式的 Protobuf 编码测试样本、损坏文件、CN 优先级、IPv4/IPv6、永久封禁持久化、缺失分类与白名单。
