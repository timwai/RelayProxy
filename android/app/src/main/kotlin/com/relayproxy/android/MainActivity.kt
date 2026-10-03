package com.relayproxy.android

import android.Manifest
import android.app.Activity
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Intent
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import org.json.JSONObject

class MainActivity : Activity() {
    private lateinit var statusCard: LinearLayout
    private lateinit var infoCard: LinearLayout
    private lateinit var statusBadge: TextView
    private lateinit var statusSummary: TextView
    private lateinit var statusTransport: TextView
    private lateinit var statusStreams: TextView
    private lateinit var statusLatency: TextView
    private lateinit var statusDetail: TextView
    private lateinit var infoServer: TextView
    private lateinit var infoDevice: TextView
    private lateinit var infoNetworkMode: TextView
    private lateinit var infoActiveNetwork: TextView
    private lateinit var infoP2PPath: TextView
    private lateinit var infoProxyExit: TextView
    private lateinit var infoProxyPath: TextView
    private lateinit var infoPowerMode: TextView
    private lateinit var infoNativeUDP: TextView
    private lateinit var infoApproval: TextView
    private lateinit var infoExitPermission: TextView
    private lateinit var infoClientPermission: TextView
    private lateinit var infoLocalProxy: TextView
    private lateinit var infoVpn: TextView
    private lateinit var infoTraffic: TextView
    private lateinit var infoVpnTraffic: TextView
    private lateinit var infoVpnScope: TextView
    private lateinit var infoVpnDns: TextView
    private lateinit var infoUptime: TextView
    private lateinit var infoDeviceId: TextView
    private var currentDeviceId: String = ""
    private lateinit var toggleButton: Button
    private lateinit var vpnButton: Button

    companion object {
        private const val REQUEST_VPN_PERMISSION = 1401
    }

    private val bg = Color.rgb(246, 248, 252)
    private val surface = Color.WHITE
    private val ink = Color.rgb(15, 23, 42)
    private val muted = Color.rgb(100, 116, 139)
    private val line = Color.rgb(226, 232, 240)
    private val brand = Color.rgb(37, 99, 235)
    private val brandSoft = Color.rgb(239, 246, 255)
    private val success = Color.rgb(22, 163, 74)
    private val successSoft = Color.rgb(240, 253, 244)
    private val warning = Color.rgb(202, 138, 4)
    private val warningSoft = Color.rgb(254, 252, 232)
    private val danger = Color.rgb(220, 38, 38)
    private val dangerSoft = Color.rgb(254, 242, 242)

    private val handler = Handler(Looper.getMainLooper())
    private val pollStatus = object : Runnable {
        override fun run() {
            renderStatus()
            handler.postDelayed(this, 1000)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        configureWindow()
        setContentView(buildUi())
        requestNotificationPermission()
    }

    override fun onResume() {
        super.onResume()
        RelayExitService.setUiVisible(true)
        handler.removeCallbacks(pollStatus)
        handler.post(pollStatus)
    }

    override fun onPause() {
        RelayExitService.setUiVisible(false)
        handler.removeCallbacks(pollStatus)
        super.onPause()
    }

    @Deprecated("Deprecated in Android API; retained for VPN consent result compatibility")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode != REQUEST_VPN_PERMISSION) return
        if (resultCode == RESULT_OK) {
            startVpnService()
        } else {
            android.widget.Toast.makeText(this, "未授予 VPN 权限", android.widget.Toast.LENGTH_SHORT).show()
        }
    }

    private fun configureWindow() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            window.setDecorFitsSystemWindows(true)
        }
        window.statusBarColor = bg
        window.navigationBarColor = bg
        window.decorView.systemUiVisibility =
            View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR or View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR
    }

    private fun buildUi(): View {
        val page = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(bg)
        }

        val content = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(20), dp(50), dp(20), dp(24))
        }
        content.addView(buildStatusCard())
        content.addView(buildInfoCard(), topMargin(18))

        val scroll = ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(bg)
            addView(content)
        }
        page.addView(
            scroll,
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                0,
                1f,
            )
        )

        page.addView(
            buildActionRow(),
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                dp(54),
            ).apply {
                leftMargin = dp(20)
                rightMargin = dp(20)
                topMargin = dp(12)
                bottomMargin = dp(24)
            }
        )

        return page
    }

    private fun buildStatusCard(): View {
        val card = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(18), dp(18), dp(18), dp(18))
            background = rounded(ink, 18)
        }
        statusCard = card

        val top = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        top.addView(LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            addView(TextView(this@MainActivity).apply {
                text = "服务状态"
                textSize = 12f
                setTextColor(Color.rgb(148, 163, 184))
            })
            statusSummary = TextView(this@MainActivity).apply {
                text = "服务未启动"
                textSize = 18f
                setTextColor(Color.WHITE)
                setTypeface(typeface, Typeface.BOLD)
                setPadding(0, dp(2), 0, 0)
            }
            addView(statusSummary)
        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        statusBadge = chip("已停止", muted, Color.rgb(241, 245, 249))
        top.addView(statusBadge)

        top.addView(TextView(this).apply {
            text = "设置"
            textSize = 11.5f
            setTextColor(Color.rgb(191, 219, 254))
            setTypeface(typeface, Typeface.BOLD)
            gravity = Gravity.CENTER
            setPadding(dp(10), dp(6), dp(2), dp(6))
            setOnClickListener {
                startActivity(Intent(this@MainActivity, SettingsHomeActivity::class.java))
            }
        })
        card.addView(top)

        val metrics = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            setPadding(0, dp(16), 0, 0)
        }

        val transportMetric = metric("传输")
        statusTransport = transportMetric.second
        metrics.addView(transportMetric.first, weighted())

        val streamsMetric = metric("连接")
        statusStreams = streamsMetric.second
        metrics.addView(streamsMetric.first, weighted())

        val latencyMetric = metric("延迟")
        statusLatency = latencyMetric.second
        metrics.addView(latencyMetric.first, weighted())

        card.addView(metrics)

        statusDetail = TextView(this).apply {
            text = "等待启动"
            textSize = 11.5f
            setTextColor(Color.rgb(203, 213, 225))
            maxLines = 2
            setPadding(0, dp(14), 0, 0)
        }
        card.addView(statusDetail)

        return card
    }

    private fun buildInfoCard(): View {
        val card = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(18), dp(18), dp(18), dp(18))
            background = rounded(surface, 18, line)
            elevation = dp(1).toFloat()
        }
        infoCard = card

        card.addView(LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            addView(TextView(this@MainActivity).apply {
                text = "运行信息"
                textSize = 17f
                setTextColor(ink)
                setTypeface(typeface, Typeface.BOLD)
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            addView(TextView(this@MainActivity).apply {
                text = "详情"
                textSize = 11.5f
                setTextColor(brand)
                setTypeface(typeface, Typeface.BOLD)
                setPadding(dp(10), dp(5), 0, dp(5))
                setOnClickListener {
                    startActivity(Intent(this@MainActivity, SettingsHomeActivity::class.java))
                }
            })
        })

        infoServer = infoRow(card, "Relay Server")
        infoDevice = infoRow(card, "设备名称")
        infoNetworkMode = infoRow(card, "出口网络")
        infoActiveNetwork = infoRow(card, "当前网络")
        infoP2PPath = infoRow(card, "P2P 直连")
        infoProxyExit = infoRow(card, "代理出口")
        infoProxyPath = infoRow(card, "当前代理路径")
        infoPowerMode = infoRow(card, "P2P 电源策略")
        infoNativeUDP = infoRow(card, "UDP 过载丢弃")
        infoApproval = infoRow(card, "设备审批")
        infoExitPermission = infoRow(card, "出口权限")
        infoClientPermission = infoRow(card, "代理客户端授权")
        infoLocalProxy = infoRow(card, "本机代理")
        infoVpn = infoRow(card, "VPN 代理")
        infoTraffic = infoRow(card, "代理流量")
        infoVpnTraffic = infoRow(card, "VPN TUN 流量")
        infoVpnScope = infoRow(card, "VPN 应用范围")
        infoVpnDns = infoRow(card, "VPN DNS")
        infoUptime = infoRow(card, "运行时长")
        infoDeviceId = deviceIdRow(card)

        return card
    }

    private fun infoRow(parent: LinearLayout, label: String): TextView {
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, dp(13), 0, 0)
        }
        row.addView(TextView(this).apply {
            text = label
            textSize = 12.5f
            setTextColor(muted)
        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 0.42f))

        val value = TextView(this).apply {
            text = "—"
            textSize = 13f
            setTextColor(ink)
            setTypeface(typeface, Typeface.BOLD)
            gravity = Gravity.END
            maxLines = 1
            ellipsize = android.text.TextUtils.TruncateAt.MIDDLE
        }
        row.addView(value, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 0.58f))
        parent.addView(row)
        return value
    }

    private fun deviceIdRow(parent: LinearLayout): TextView {
        val block = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(0, dp(14), 0, 0)
        }

        val header = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        header.addView(TextView(this).apply {
            text = "设备 ID"
            textSize = 12.5f
            setTextColor(muted)
        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        header.addView(TextView(this).apply {
            text = "复制"
            textSize = 12f
            setTextColor(brand)
            setTypeface(typeface, Typeface.BOLD)
            setPadding(dp(10), dp(4), 0, dp(4))
            setOnClickListener { copyDeviceId() }
        })
        block.addView(header)

        val value = TextView(this).apply {
            text = "—"
            textSize = 12.5f
            setTextColor(ink)
            typeface = Typeface.MONOSPACE
            setTextIsSelectable(true)
            setHorizontallyScrolling(false)
            maxLines = 6
            setLineSpacing(dp(2).toFloat(), 1f)
            setPadding(dp(10), dp(9), dp(10), dp(9))
            background = rounded(Color.argb(80, 255, 255, 255), 10, Color.argb(90, 148, 163, 184))
        }
        block.addView(
            value,
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ).apply { topMargin = dp(6) }
        )

        parent.addView(block)
        return value
    }

    private fun copyDeviceId() {
        if (currentDeviceId.isBlank()) {
            android.widget.Toast.makeText(this, "暂无设备 ID", android.widget.Toast.LENGTH_SHORT).show()
            return
        }
        val clipboard = getSystemService(ClipboardManager::class.java)
        clipboard.setPrimaryClip(ClipData.newPlainText("RelayProxy Device ID", currentDeviceId))
        android.widget.Toast.makeText(this, "设备 ID 已复制", android.widget.Toast.LENGTH_SHORT).show()
    }

    private fun buildActionRow(): View {
        toggleButton = Button(this).apply {
            text = "启动"
            textSize = 15f
            setTextColor(Color.WHITE)
            setTypeface(typeface, Typeface.BOLD)
            setAllCaps(false)
            background = rounded(brand, 14)
            setOnClickListener { toggleRelay() }
        }
        vpnButton = Button(this).apply {
            text = "启动 VPN"
            textSize = 14f
            setTextColor(brand)
            setTypeface(typeface, Typeface.BOLD)
            setAllCaps(false)
            background = rounded(Color.WHITE, 14, Color.rgb(191, 219, 254))
            setOnClickListener { toggleVpn() }
        }
        return LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            addView(
                toggleButton,
                LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.MATCH_PARENT, 1f),
            )
            addView(
                vpnButton,
                LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.MATCH_PARENT, 1f).apply {
                    leftMargin = dp(10)
                },
            )
        }
    }

    private fun toggleRelay() {
        val store = ConfigStore(this)
        if (store.isDesiredRunning()) {
            startService(
                Intent(this, RelayExitService::class.java)
                    .setAction(RelayExitService.ACTION_STOP)
            )
        } else {
            startRelay()
        }
    }

    private fun toggleVpn() {
        val store = ConfigStore(this)
        if (store.isVpnDesiredRunning()) {
            stopVpnService()
            return
        }
        val config = store.load()
        if (config.serverAddress.isBlank()) {
            startActivity(
                Intent(this, SettingsActivity::class.java)
                    .putExtra(SettingsActivity.EXTRA_SECTION, SettingsActivity.SECTION_CONNECTION)
            )
            return
        }
        val consent = VpnService.prepare(this)
        if (consent != null) {
            startActivityForResult(consent, REQUEST_VPN_PERMISSION)
        } else {
            startVpnService()
        }
    }

    private fun startVpnService() {
        val intent = Intent(this, RelayVpnService::class.java).setAction(RelayVpnService.ACTION_START)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(intent)
        } else {
            startService(intent)
        }
    }

    private fun stopVpnService() {
        ConfigStore(this).setVpnDesiredRunning(false)
        val intent = Intent(this, RelayVpnService::class.java).setAction(RelayVpnService.ACTION_STOP)
        runCatching { startService(intent) }
    }

    private fun startRelay() {
        val config = ConfigStore(this).load()
        if (config.serverAddress.isBlank()) {
            startActivity(
                Intent(this, SettingsActivity::class.java)
                    .putExtra(SettingsActivity.EXTRA_SECTION, SettingsActivity.SECTION_CONNECTION)
            )
            return
        }

        val intent = Intent(this, RelayExitService::class.java)
            .setAction(RelayExitService.ACTION_START)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(intent)
        } else {
            startService(intent)
        }
    }

    private fun renderStatus() {
        val obj = runCatching { JSONObject(RelayExitService.statusJson()) }.getOrNull()
        if (obj == null) {
            statusSummary.text = "状态不可用"
            updateChip("未知", muted, Color.rgb(241, 245, 249))
            return
        }

        val state = obj.optString("connectionState", "UNKNOWN")
        val approval = obj.optString("approvalState", "unknown")
        val transportValue = obj.optString("transport", "")
        val streams = obj.optLong("activeStreams", 0)
        val latency = obj.optLong("latencyMs", 0)
        val approved = obj.optBoolean("exitApproved", false)
        val clientApproved = obj.optBoolean("clientApproved", false)
        val deviceId = obj.optString("deviceId", "")
        currentDeviceId = deviceId
        val uptimeMs = obj.optLong("serviceUptimeMs", 0)
        val error = obj.optString("lastError", "")
        val activeNetwork = obj.optString("activeNetwork", "")
        val p2pState = obj.optString("p2pState", "")
        val p2pPath = obj.optString("p2pPath", "")
        val p2pRttMs = obj.optLong("p2pRttMs", 0)
        val proxyState = obj.optString("proxyState", "")
        val proxyError = obj.optString("proxyError", "")
        val selectedExit = obj.optString("selectedExit", "")
        val powerConstrained = obj.optBoolean("powerConstrained", false)
        val nativeUdp = obj.optJSONObject("nativeUdp")
        val proxyActiveTcp = obj.optLong("proxyActiveTcp", 0)
        val proxyActiveUdp = obj.optLong("proxyActiveUdp", 0)
        val proxyBytesUp = obj.optLong("proxyBytesUp", 0)
        val proxyBytesDown = obj.optLong("proxyBytesDown", 0)
        val ready = approved || clientApproved

        when (state) {
            "CONNECTED" -> {
                statusSummary.text = when {
                    approved && clientApproved -> "出口与代理均已就绪"
                    clientApproved -> "代理客户端已就绪"
                    approved -> "网络出口已就绪"
                    else -> "已连接，等待授权"
                }
                if (ready) {
                    updateChip("运行中", success, successSoft)
                } else {
                    updateChip("待审批", warning, warningSoft)
                }
            }
            "CONNECTING" -> {
                statusSummary.text = "正在连接"
                updateChip("连接中", brand, brandSoft)
            }
            "WAITING_NETWORK" -> {
                statusSummary.text = "等待可用网络"
                updateChip("等待网络", warning, warningSoft)
            }
            "ERROR" -> {
                statusSummary.text = "服务异常"
                updateChip("错误", danger, dangerSoft)
            }
            "STOPPED" -> {
                statusSummary.text = "服务未启动"
                updateChip("已停止", muted, Color.rgb(241, 245, 249))
            }
            else -> {
                statusSummary.text = state
                updateChip("更新中", brand, brandSoft)
            }
        }

        statusTransport.text = transportValue.ifBlank { "—" }
        statusStreams.text = (streams + proxyActiveTcp + proxyActiveUdp).toString()
        statusLatency.text = if (latency > 0) "$latency ms" else "—"

        val store = ConfigStore(this)
        val config = store.load()
        infoServer.text = config.serverAddress.ifBlank { "未配置" }
        infoDevice.text = config.deviceName.ifBlank { "RelayProxy Android" }
        infoNetworkMode.text = networkModeLabel(
            config.networkMode,
            config.autoNetworkSwitch,
        )
        infoActiveNetwork.text = activeNetworkLabel(activeNetwork)
        infoP2PPath.text = if (config.proxyP2pEnabled) {
            p2pPathLabel(p2pState, p2pPath, p2pRttMs)
        } else {
            "已关闭 · Relay"
        }
        infoProxyExit.text = proxyExitLabel(obj, selectedExit, proxyState)
        infoProxyPath.text = when {
            p2pState == "READY" && p2pPath == "p2p_quic" -> "P2P QUIC"
            state == "CONNECTED" && transportValue.isNotBlank() -> "Relay · $transportValue"
            else -> "—"
        }
        infoPowerMode.text = if (powerConstrained) "省电" else "标准"
        infoNativeUDP.text = nativeUdp?.let {
            "队列 ${it.optLong("queueDrops", 0)} · 重组 ${it.optLong("reassemblyDrops", 0)} · 关联 ${it.optLong("associationRejects", 0)}"
        } ?: "—"
        infoApproval.text = approvalLabel(approval)
        infoExitPermission.text = when {
            !config.exitEnabled -> "未启用"
            approved -> "已授权"
            state == "CONNECTED" -> "等待服务端授权"
            else -> "等待连接"
        }
        infoClientPermission.text = when {
            !config.clientEnabled && !store.isVpnDesiredRunning() -> "未启用"
            clientApproved -> "已授权"
            state == "CONNECTED" -> "等待服务端授权"
            else -> "等待连接"
        }
        infoLocalProxy.text = if (config.clientEnabled) {
            buildList {
                if (config.socks5Enabled) add("SOCKS5 127.0.0.1:${config.socks5Port}")
                if (config.httpEnabled) add("HTTP 127.0.0.1:${config.httpPort}")
            }.joinToString(" · ").ifBlank { "未启用" }
        } else {
            "未启用"
        }
        val vpn = runCatching { JSONObject(RelayVpnService.statusJson()) }.getOrNull()
        val vpnState = vpn?.optString("vpnState", "STOPPED") ?: "STOPPED"
        infoVpn.text = when (vpnState) {
            "RUNNING" -> "运行中"
            "STARTING" -> "启动中"
            "ERROR" -> vpn?.optString("detail", "启动失败") ?: "启动失败"
            else -> "已停止"
        }
        infoTraffic.text = "↑ ${formatBytes(proxyBytesUp)} · ↓ ${formatBytes(proxyBytesDown)}"
        val tunTx = vpn?.optLong("tunTxBytes", 0) ?: 0
        val tunRx = vpn?.optLong("tunRxBytes", 0) ?: 0
        infoVpnTraffic.text = if (tunTx > 0 || tunRx > 0) {
            "TX ${formatBytes(tunTx)} · RX ${formatBytes(tunRx)}"
        } else {
            "—"
        }
        infoVpnScope.text = when (config.vpnAppMode) {
            ExitConfig.VPN_APP_MODE_INCLUDE -> "仅 ${config.vpnPackages.size} 个应用"
            ExitConfig.VPN_APP_MODE_EXCLUDE -> "排除 ${config.vpnPackages.size} 个应用"
            else -> "全部应用"
        }
        infoVpnDns.text = config.vpnDnsServers.joinToString(", ").ifBlank { "未配置" }
        infoUptime.text = formatDuration(uptimeMs)
        infoDeviceId.text = formatDeviceId(deviceId)
        applyStateTheme(state, ready, approval)

        val desiredRunning = store.isDesiredRunning()
        if (desiredRunning) {
            toggleButton.text = "停止出口"
            toggleButton.setTextColor(danger)
            toggleButton.background = rounded(surface, 14, Color.rgb(254, 202, 202))
        } else {
            toggleButton.text = "启动出口"
            toggleButton.setTextColor(Color.WHITE)
            toggleButton.background = rounded(brand, 14)
        }

        val vpnDesired = store.isVpnDesiredRunning()
        vpnButton.text = if (vpnDesired) "停止 VPN" else "启动 VPN"
        vpnButton.setTextColor(if (vpnDesired) danger else brand)
        vpnButton.background = if (vpnDesired) {
            rounded(surface, 14, Color.rgb(254, 202, 202))
        } else {
            rounded(Color.WHITE, 14, Color.rgb(191, 219, 254))
        }

        statusDetail.text = when {
            error.isNotBlank() -> error
            proxyError.isNotBlank() && (config.clientEnabled || vpnDesired) -> proxyError
            approval == "pending" -> "设备等待服务端审批"
            approval == "rejected" -> "设备审批已拒绝"
            state == "CONNECTED" && ready -> "后台常驻运行中"
            state == "STOPPED" -> "点击启动后可退出 App，服务继续后台运行"
            else -> "审批：$approval"
        }
    }

    private fun proxyExitLabel(status: JSONObject, selectedExit: String, proxyState: String): String {
        if (selectedExit.isBlank()) {
            return when (proxyState) {
                "exit_required" -> "需要选择出口"
                "no_exit" -> "无可用出口"
                "not_authorized" -> "等待授权"
                else -> "自动"
            }
        }
        val exits = status.optJSONArray("proxyExits")
        if (exits != null) {
            for (index in 0 until exits.length()) {
                val item = exits.optJSONObject(index) ?: continue
                if (item.optString("deviceId") == selectedExit) {
                    return item.optString("name").ifBlank { selectedExit.take(12) }
                }
            }
        }
        return selectedExit.take(12)
    }

    private fun applyStateTheme(state: String, approved: Boolean, approval: String) {
        val palette = when {
            state == "CONNECTED" && approved -> intArrayOf(
                Color.rgb(21, 128, 61),
                Color.rgb(240, 253, 244),
                Color.rgb(187, 247, 208),
            )
            state == "CONNECTED" && approval != "approved" -> intArrayOf(
                Color.rgb(180, 83, 9),
                Color.rgb(255, 251, 235),
                Color.rgb(253, 230, 138),
            )
            state == "CONNECTING" -> intArrayOf(
                Color.rgb(29, 78, 216),
                Color.rgb(239, 246, 255),
                Color.rgb(191, 219, 254),
            )
            state == "WAITING_NETWORK" -> intArrayOf(
                Color.rgb(180, 83, 9),
                Color.rgb(255, 251, 235),
                Color.rgb(253, 230, 138),
            )
            state == "ERROR" -> intArrayOf(
                Color.rgb(185, 28, 28),
                Color.rgb(254, 242, 242),
                Color.rgb(254, 202, 202),
            )
            else -> intArrayOf(
                Color.rgb(51, 65, 85),
                Color.rgb(248, 250, 252),
                Color.rgb(203, 213, 225),
            )
        }

        statusCard.background = rounded(palette[0], 18)
        infoCard.background = rounded(palette[1], 18, palette[2])
    }

    private fun formatDeviceId(value: String): String {
        if (value.isBlank()) return "—"
        return value.chunked(16).joinToString("\n")
    }

    private fun networkModeLabel(mode: String, autoSwitch: Boolean): String = when (mode) {
        NetworkBinder.MODE_WIFI ->
            if (autoSwitch) "Wi-Fi 优先 · 自动切换" else "仅 Wi-Fi"
        NetworkBinder.MODE_CELLULAR ->
            if (autoSwitch) "移动数据优先 · 自动切换" else "仅移动数据"
        else -> "自动选择"
    }

    private fun activeNetworkLabel(activeMode: String): String = when (activeMode) {
        NetworkBinder.MODE_WIFI -> "Wi-Fi"
        NetworkBinder.MODE_CELLULAR -> "移动数据"
        else -> "未连接"
    }

    private fun p2pPathLabel(state: String, path: String, rttMs: Long): String {
        if (state.isBlank()) return "等待直连"
        if (state == "READY" && path == "p2p_quic") {
            return if (rttMs > 0) "P2P QUIC · ${rttMs} ms" else "P2P QUIC"
        }
        return when (state) {
            "DISCOVERING" -> "发现候选"
            "RENDEZVOUS" -> "候选交换"
            "PUNCHING" -> "UDP 打洞"
            "QUIC_HANDSHAKE" -> "QUIC 握手"
            "COOLDOWN" -> "冷却中 · Relay 回退"
            "DEGRADED" -> "Relay 回退"
            "CLOSED" -> "已关闭"
            else -> state
        }
    }

    private fun approvalLabel(value: String): String = when (value) {
        "approved" -> "已批准"
        "pending" -> "等待审批"
        "rejected" -> "已拒绝"
        else -> "未知"
    }

    private fun formatDuration(uptimeMs: Long): String {
        if (uptimeMs <= 0) return "—"
        val totalSeconds = uptimeMs / 1000
        val hours = totalSeconds / 3600
        val minutes = (totalSeconds % 3600) / 60
        val seconds = totalSeconds % 60
        return when {
            hours > 0 -> hours.toString() + "小时 " + minutes + "分"
            minutes > 0 -> minutes.toString() + "分 " + seconds + "秒"
            else -> seconds.toString() + "秒"
        }
    }

    private fun formatBytes(value: Long): String {
        if (value <= 0) return "0 B"
        val units = arrayOf("B", "KB", "MB", "GB", "TB")
        var amount = value.toDouble()
        var unit = 0
        while (amount >= 1024 && unit < units.lastIndex) {
            amount /= 1024
            unit++
        }
        return if (unit == 0) "$value ${units[unit]}" else "%.1f %s".format(amount, units[unit])
    }

    private fun metric(label: String): Pair<LinearLayout, TextView> {
        val value = TextView(this).apply {
            text = "—"
            textSize = 15f
            setTextColor(Color.WHITE)
            setTypeface(typeface, Typeface.BOLD)
            gravity = Gravity.CENTER_HORIZONTAL
        }
        val group = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            addView(TextView(this@MainActivity).apply {
                text = label
                textSize = 10.5f
                setTextColor(Color.rgb(148, 163, 184))
                gravity = Gravity.CENTER_HORIZONTAL
            })
            addView(value, topMargin(2))
        }
        return group to value
    }

    private fun chip(textValue: String, color: Int, fill: Int) = TextView(this).apply {
        text = textValue
        textSize = 11f
        setTextColor(color)
        setTypeface(typeface, Typeface.BOLD)
        gravity = Gravity.CENTER
        background = rounded(fill, 10)
        setPadding(dp(9), dp(6), dp(9), dp(6))
    }

    private fun updateChip(textValue: String, color: Int, fill: Int) {
        statusBadge.text = textValue
        statusBadge.setTextColor(color)
        statusBadge.background = rounded(fill, 10)
    }

    private fun rounded(fill: Int, radiusDp: Int, stroke: Int? = null) =
        GradientDrawable().apply {
            shape = GradientDrawable.RECTANGLE
            setColor(fill)
            cornerRadius = dp(radiusDp).toFloat()
            if (stroke != null) setStroke(dp(1), stroke)
        }

    private fun weighted() = LinearLayout.LayoutParams(
        0,
        ViewGroup.LayoutParams.WRAP_CONTENT,
        1f
    )

    private fun topMargin(top: Int) = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        ViewGroup.LayoutParams.WRAP_CONTENT
    ).apply { topMargin = dp(top) }

    private fun dp(value: Int) = (value * resources.displayMetrics.density + 0.5f).toInt()

    private fun requestNotificationPermission() {
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) !=
            android.content.pm.PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 100)
        }
    }
}
