# RDP 公网入口：非中国大陆 IP 永久封禁

此策略仅作用于 **Server 公网 RDP TCP/UDP 入口**，不作用于已认证 Agent、代理、P2P 流量。所有地理位置判断在服务端使用本地 IP 国家数据库，不在接入请求中调用在线 IP 查询服务。

## 启用

下载并接受 [DB-IP Country Lite](https://db-ip.com/db/download/ip-to-country-lite) (CC BY 4.0) 的数据使用条款，把 **Country Lite CSV(.gz)** 放置在 Server 工作目录的：

\`data/dbip-country-lite-latest.csv.gz\`

也可以设置 \`RELAY_RDP_GEOIP_DB=/absolute/path/dbip-country-lite-YYYY-MM.csv.gz\` 指定数据库文件（支持 .csv 和 .csv.gz）。**环境变量设置了但文件缺失或损坏时 Server 启动失败**，避免误以为策略生效。如果默认文件不存在且未设置环境变量，策略不会激活，并在 Server 日志里明确报警；部署时务必确认日志显示 \`country policy active\`。更换数据库需要重启 Server。

建议每月手动更新国家数据库，再重启 Server。数据来自 DB-IP（CC BY 4.0）：https://db-ip.com 。

## 判定与审计

- 源地址按照 IPv4 / IPv6 判断，以 ISO 3166-1 alpha-2 \`CN\`（中国大陆）为唯一允许的国家代码；HK/MO/TW 不属于 CN，会触发永久封禁。
- **明确匹配非 CN 的公网 IP**：第一次连接即拒绝，写入全局 RDP 永久自动封禁（\`expiresAt=null\`，\`reason=GEO_NON_CN:XX\`），后续 TCP/UDP、新入口、Server 重启仍生效。
- **无国家记录或数据库返回 ZZ 的公网 IP**：拒绝但不永久封禁（\`GEO_COUNTRY_UNKNOWN\`），防止数据库覆盖不全导致错误的永久封禁。
- 内网、回环、本地链路地址：不自动因国家策略封禁，继续执行原有来源 CIDR、授权和频率限制。
- 现有手动封禁优先，频率规则的白名单**不能绕过**国家策略；解除已产生的永久封禁需管理员在 IP 安全策略中手动操作。
- RDP 审计记录保存 \`sourceCountry\`，并在来源 IP 汇总和明细显示国家代码。旧记录的国家字段为空，不会被追溯改写。
- 永久封禁是按 IP 的自动封禁，不等同于防火墙规则；网络连入 TCP socket 之后才做应用层拒绝。IP 地理库可能存在误差，使用前应验证服务所在地区与允许来源。

## 依赖和验收

不引入新的 Go 第三方依赖。构建或单元测试无需下载 GeoIP 数据。
验证 \`go test ./server/rdp ./server/repository ./server/api\`；重点覆盖 IPv4/IPv6、永久封禁、重启后恢复、重复访问去重、白名单优先级和无法识别的 IP。
