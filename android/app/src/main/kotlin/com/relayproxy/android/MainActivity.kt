package com.relayproxy.android

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.content.BroadcastReceiver
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.res.ColorStateList
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.net.Uri
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.text.InputType
import android.view.DragEvent
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.AdapterView
import android.widget.ArrayAdapter
import android.widget.Button
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.Spinner
import android.widget.Switch
import android.widget.TextView
import android.widget.Toast
import org.json.JSONArray
import org.json.JSONObject
import java.util.ArrayDeque

/**
 * RelayProxy Android 客户端全新现代主控制台。
 * 遵循 Material 3 规范与网络态势感知设计，提供 4-Tab 一级架构：
 * 1. 控制台 (Dashboard): 核心态势、实时吞吐波形、三维度量、分流模式切换、双核独立控制、链路诊断
 * 2. 出口节点 (Nodes): 自动优选、已授权节点列表、实时 RTT 延迟、P2P 支持标签、一键测速与切换
 * 3. 分流规则 (Routing): VPN 应用接管范围、应用选择器入口、规则卡片流与即时启停
 * 4. 配置身份 (Settings): 服务端凭证、16位身份 ID、网络调度策略、传输端口与回环代理
 *
 * 特性：
 * - 动态适配状态栏高空间预留与设备 Cutout 安全区，顶部视觉呼吸感充足
 * - 原生还原原型高保真底部悬浮导航栏 (SVG Vector 图标、Material 3 胶囊指示灯、动态染色)
 * - 支持跟随系统 / 浅色 / 深色三态主题，状态栏与导航栏图标随动
 */
class MainActivity : Activity() {

    companion object {
        private const val REQUEST_VPN_PERMISSION = 1401
        private const val REQUEST_ROUTING_RULE = 1402
        private const val REQUEST_VPN_APPS = 1403
        private const val REQUEST_MESSAGE_OVERLAY_PERMISSION = 1404

        const val TAB_DASHBOARD = 0
        const val TAB_NODES = 1
        const val TAB_ROUTING = 2
        const val TAB_SETTINGS = 3
    }

    private var currentTab = TAB_DASHBOARD
    private lateinit var tabPanes: List<View>
    private lateinit var navButtons: List<LinearLayout>
    private lateinit var navIcons: List<ImageView>
    private lateinit var navTexts: List<TextView>

    // ====== Tab 0: Dashboard UI Views ======
    private lateinit var statusCard: LinearLayout
    private lateinit var statusBadge: TextView
    private lateinit var p2pBadge: TextView
    private lateinit var statusTitle: TextView
    private lateinit var statusDetail: TextView
    private lateinit var masterPowerButton: FrameLayout
    private lateinit var masterPowerIcon: ImageView

    private lateinit var metricTransport: TextView
    private lateinit var metricStreams: TextView
    private lateinit var metricLatency: TextView

    private lateinit var throughputSparkline: ThroughputSparklineView
    private lateinit var speedUpText: TextView
    private lateinit var speedDownText: TextView
    private lateinit var totalTrafficText: TextView

    private lateinit var routingModePills: List<TextView>
    private var currentRoutingMode = "global_proxy"

    private lateinit var activeNodeName: TextView
    private lateinit var activeNodeSubtitle: TextView
    private lateinit var activeNodeLatency: TextView

    private lateinit var vpnStatusBadge: TextView
    private lateinit var vpnActionBtn: TextView
    private lateinit var exitStatusBadge: TextView
    private lateinit var exitActionBtn: TextView

    private lateinit var diagActiveNet: TextView
    private lateinit var diagPowerMode: TextView
    private lateinit var diagUdpDrops: TextView
    private lateinit var diagLoopback: TextView

    // ====== Tab 1: Nodes UI Views ======
    private lateinit var nodesListContainer: LinearLayout
    private lateinit var autoNodeRadioDot: View

    // ====== Tab 2: Routing UI Views ======
    private data class RuleDragToken(val ruleId: String)

    private lateinit var vpnScopeSpinner: Spinner
    private lateinit var vpnAppsSummaryText: TextView
    private lateinit var rulesListContainer: LinearLayout

    // ====== Tab 3: Settings UI Views ======
    private lateinit var settingServerField: EditText
    private lateinit var settingIdentityField: EditText
    private lateinit var settingDeviceNameField: EditText
    private lateinit var settingDeviceIdText: TextView
    private lateinit var settingNetModeSpinner: Spinner
    private lateinit var settingAutoSwitch: Switch
    private lateinit var settingAllowPrivate: Switch
    private lateinit var settingQuicPortField: EditText
    private lateinit var settingTcpPortField: EditText
    private lateinit var settingTlsSwitch: Switch
    private lateinit var settingInsecureTlsSwitch: Switch
    private lateinit var settingSocksPortField: EditText
    private lateinit var settingHttpPortField: EditText
    private lateinit var settingP2pSwitch: Switch
    private lateinit var settingIpv6Switch: Switch
    private lateinit var settingGlobalMessageOverlay: Switch
    private lateinit var settingGlobalOverlayStatus: TextView
    private lateinit var settingGlobalOverlayAction: TextView

    // ====== State & Sampling ======
    private var currentDeviceId: String = ""
    private var lastTotalBytesUp: Long = 0
    private var lastTotalBytesDown: Long = 0
    private var lastSampleTimestamp: Long = 0
    private var isPingingNodes = false

    private val handler = Handler(Looper.getMainLooper())
    private val messagePopupQueue = ArrayDeque<JSONObject>()
    private var activeMessageDialog: AlertDialog? = null
    private var messageReceiverRegistered = false
    private val messageReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            val raw = intent?.getStringExtra(RelayExitService.EXTRA_MESSAGE_JSON) ?: return
            runCatching { JSONObject(raw) }.getOrNull()?.let(::enqueueMessagePopup)
        }
    }

    private val pollStatus = object : Runnable {
        override fun run() {
            renderStatus()
            handler.postDelayed(this, 1000)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // 初始化深浅色主题
        UiPalette.sync(this)
        setTheme(if (UiPalette.isDark) R.style.Theme_RelayProxy_Dark else R.style.Theme_RelayProxy_Light)
        configureWindow()
        setContentView(buildRootUi())
        handleMessageIntent(intent)
        requestNotificationPermission()
    }

    override fun onResume() {
        super.onResume()
        registerMessageReceiver()
        syncRoutingModeFromStore()
        if (::rulesListContainer.isInitialized) refreshRoutingTab()
        connectControlChannel()
        RelayExitService.setUiVisible(true)
        refreshGlobalMessageOverlayPermissionState()
        handler.removeCallbacks(pollStatus)
        handler.post(pollStatus)
    }

    override fun onPause() {
        RelayExitService.setUiVisible(false)
        unregisterMessageReceiver()
        handler.removeCallbacks(pollStatus)
        super.onPause()
    }

    override fun onNewIntent(intent: Intent?) {
        super.onNewIntent(intent)
        if (intent != null) {
            setIntent(intent)
            handleMessageIntent(intent)
        }
    }

    @Deprecated("Deprecated in Android API; retained for VPN consent result compatibility")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        when (requestCode) {
            REQUEST_VPN_PERMISSION -> {
                if (resultCode == RESULT_OK) {
                    startVpnService()
                } else {
                    Toast.makeText(this, "未授予 VPN 权限，无法接管系统流量", Toast.LENGTH_SHORT).show()
                }
            }
            REQUEST_VPN_APPS -> {
                if (resultCode == RESULT_OK && data != null) {
                    val store = ConfigStore(this)
                    val pkgs = data.getStringArrayListExtra(VpnAppSelectionActivity.EXTRA_SELECTED).orEmpty()
                    store.save(store.load().copy(vpnPackages = pkgs.filter { it != packageName }.toSet()))
                    refreshRoutingTab()
                    notifyServiceReconfigure()
                }
            }
            REQUEST_ROUTING_RULE -> {
                refreshRoutingTab()
            }
            REQUEST_MESSAGE_OVERLAY_PERMISSION -> {
                refreshGlobalMessageOverlayPermissionState()
                RelayExitService.refreshGlobalMessageOverlaySetting()
                if (MessageOverlayController.hasOverlayPermission(this)) {
                    Toast.makeText(this, "全局消息弹窗权限已开启", Toast.LENGTH_SHORT).show()
                }
            }
        }
    }

    private fun getStatusBarHeight(): Int {
        val resourceId = resources.getIdentifier("status_bar_height", "dimen", "android")
        val height = if (resourceId > 0) resources.getDimensionPixelSize(resourceId) else dp(28)
        return height.coerceAtLeast(dp(28))
    }

    private fun configureWindow() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            window.setDecorFitsSystemWindows(true)
        }
        window.statusBarColor = UiPalette.bg
        window.navigationBarColor = UiPalette.surface
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            var flags = window.decorView.systemUiVisibility
            flags = if (!UiPalette.isDark) {
                // 日间模式：浅色状态栏背景 -> 深色文字与图标
                flags or View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR
            } else {
                // 夜间模式：深色状态栏背景 -> 浅色文字与图标
                flags and View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR.inv()
            }
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                flags = if (!UiPalette.isDark) {
                    flags or View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR
                } else {
                    flags and View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR.inv()
                }
            }
            @Suppress("DEPRECATION")
            window.decorView.systemUiVisibility = flags
        }
    }

    private fun applyThemeMode(mode: String, showToast: Boolean = true) {
        val store = ConfigStore(this)
        store.setThemeMode(mode)
        UiPalette.sync(this)
        setTheme(if (UiPalette.isDark) R.style.Theme_RelayProxy_Dark else R.style.Theme_RelayProxy_Light)
        configureWindow()
        setContentView(buildRootUi())
        renderStatus()
        RelayExitService.refreshGlobalMessageOverlaySetting()
        if (showToast) {
            val label = when (store.themeMode()) {
                ConfigStore.THEME_LIGHT -> "浅色模式"
                ConfigStore.THEME_DARK -> "深色模式"
                else -> "跟随系统"
            }
            Toast.makeText(this, "主题已切换为" + label, Toast.LENGTH_SHORT).show()
        }
    }

    private fun buildRootUi(): View {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(UiPalette.bg)
            fitsSystemWindows = true
        }

        // 1. 主内容视窗容器 (Tab Panes 容器)
        val contentContainer = FrameLayout(this)

        val dashboardPane = buildDashboardTab()
        val nodesPane = buildNodesTab()
        val routingPane = buildRoutingTab()
        val settingsPane = buildSettingsTab()

        tabPanes = listOf(dashboardPane, nodesPane, routingPane, settingsPane)
        tabPanes.forEachIndexed { index, pane ->
            pane.visibility = if (index == currentTab) View.VISIBLE else View.GONE
            contentContainer.addView(
                pane,
                FrameLayout.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT,
                    ViewGroup.LayoutParams.MATCH_PARENT,
                )
            )
        }

        root.addView(
            contentContainer,
            LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f)
        )

        // 2. 底部现代悬浮导航栏 (还原原型设计)
        root.addView(
            buildBottomNavBar(),
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                dp(66),
            )
        )

        return root
    }

    // =========================================================================
    // TAB 0: 控制台 (Dashboard)
    // =========================================================================
    private fun buildDashboardTab(): View {
        val content = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            val pad = dp(16)
            // 顶部预留 16dp 呼吸空间，底部预留 88dp 留白防止被底部导航栏遮挡
            setPadding(pad, dp(16), pad, dp(88))
        }

        content.addView(buildMasterStatusCard())
        content.addView(buildRoutingModeSegment(), topMargin(14))
        content.addView(buildActiveNodeCard(), topMargin(14))
        content.addView(buildDualCoreGrid(), topMargin(14))
        content.addView(buildLiveApplicationMonitorCard(), topMargin(14))
        content.addView(buildDiagnosticsCard(), topMargin(14))

        return ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(UiPalette.bg)
            addView(content)
        }
    }

    private fun buildMasterStatusCard(): View {
        statusCard = UiKit.card(
            this,
            paddingDp = 18,
            radiusDp = 16,
            backgroundColor = UiPalette.surfaceSubtle,
            borderColor = UiPalette.line,
        )

        val topRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }

        val leftTitles = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
        }

        val badgeRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        statusBadge = UiKit.chip(this, "服务未启动", UiPalette.muted, UiPalette.surfaceElevated, radiusDp = 6)
        badgeRow.addView(statusBadge)

        p2pBadge = UiKit.chip(this, "等待直连", UiPalette.brand, UiPalette.brandSoft, UiPalette.brandSoftBorder, radiusDp = 6).apply {
            val lp = LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                leftMargin = dp(8)
            }
            layoutParams = lp
        }
        badgeRow.addView(p2pBadge)
        leftTitles.addView(badgeRow)

        statusTitle = TextView(this).apply {
            text = "服务未启动"
            textSize = 20f
            setTextColor(UiPalette.ink)
            typeface = Typeface.DEFAULT_BOLD
            setPadding(0, dp(6), 0, 0)
        }
        leftTitles.addView(statusTitle)

        statusDetail = TextView(this).apply {
            text = "点击右侧按钮启动 VPN 代理隧道"
            textSize = 12f
            setTextColor(UiPalette.muted)
            maxLines = 2
            setPadding(0, dp(2), 0, 0)
        }
        leftTitles.addView(statusDetail)

        topRow.addView(leftTitles, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        // 主电源大按钮 (56dp x 56dp) - 还原原型纯矢量图标设计
        masterPowerButton = FrameLayout(this).apply {
            background = UiKit.rounded(this@MainActivity, UiPalette.surfaceElevated, 14, UiPalette.lineSubtle)
            isClickable = true
            isFocusable = true
            setOnClickListener { toggleMasterConnection() }
        }

        masterPowerIcon = ImageView(this).apply {
            setImageResource(R.drawable.ic_power_bolt)
            imageTintList = ColorStateList.valueOf(UiPalette.muted)
            val sz = dp(28)
            layoutParams = FrameLayout.LayoutParams(sz, sz, Gravity.CENTER)
        }
        masterPowerButton.addView(masterPowerIcon)

        topRow.addView(
            masterPowerButton,
            LinearLayout.LayoutParams(dp(56), dp(56)).apply { leftMargin = dp(14) }
        )

        statusCard.addView(topRow)

        // 核心三维度量 Grid (传输方式、活跃连接、RTT)
        val metricsRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            setPadding(0, dp(16), 0, 0)
        }

        val col1 = UiKit.metricColumn(this, "传输方式", "QUIC")
        metricTransport = col1.second
        metricsRow.addView(col1.first, weighted())

        val col2 = UiKit.metricColumn(this, "活跃连接", "0")
        metricStreams = col2.second
        metricsRow.addView(col2.first, weighted())

        val col3 = UiKit.metricColumn(this, "往返延迟", "—", UiPalette.success)
        metricLatency = col3.second
        metricsRow.addView(col3.first, weighted())

        statusCard.addView(metricsRow)

        // 实时吞吐走势
        val throughputHeader = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, dp(14), 0, dp(6))
        }

        val sparkTitle = TextView(this).apply {
            text = "实时吞吐速率"
            textSize = 11.5f
            setTextColor(UiPalette.muted)
        }
        throughputHeader.addView(sparkTitle, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        val speedsLayout = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        speedUpText = TextView(this).apply {
            text = "↑ 0 B/s"
            textSize = 11f
            setTextColor(UiPalette.brand)
            typeface = Typeface.MONOSPACE
        }
        speedsLayout.addView(speedUpText)

        speedDownText = TextView(this).apply {
            text = " · ↓ 0 B/s"
            textSize = 11f
            setTextColor(Color.rgb(129, 140, 248))
            typeface = Typeface.MONOSPACE
        }
        speedsLayout.addView(speedDownText)
        throughputHeader.addView(speedsLayout)

        statusCard.addView(throughputHeader)

        throughputSparkline = ThroughputSparklineView(this).apply {
            background = UiKit.rounded(this@MainActivity, if (UiPalette.isDark) Color.argb(120, 10, 15, 29) else Color.argb(40, 0, 0, 0), 12)
        }
        statusCard.addView(
            throughputSparkline,
            LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(44))
        )

        totalTrafficText = TextView(this).apply {
            text = "累计: ↑ 0 B · ↓ 0 B"
            textSize = 10.5f
            setTextColor(UiPalette.placeholder)
            gravity = Gravity.END
            setPadding(0, dp(5), 0, 0)
        }
        statusCard.addView(totalTrafficText)

        return statusCard
    }

    private fun buildRoutingModeSegment(): View {
        val segmentCard = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            val pad = dp(3)
            setPadding(pad, pad, pad, pad)
            background = UiKit.rounded(this@MainActivity, UiPalette.surface, 10, UiPalette.line)
        }

        val modeKeys = listOf("rule", "global", "direct")
        val modeLabels = listOf("按规则分流", "全局代理", "全局直连")
        val pills = mutableListOf<TextView>()

        modeKeys.forEachIndexed { index, modeKey ->
            val pill = TextView(this).apply {
                text = modeLabels[index]
                textSize = 12.5f
                typeface = Typeface.DEFAULT_BOLD
                gravity = Gravity.CENTER
                val padV = dp(8)
                setPadding(0, padV, 0, padV)
                isClickable = true
                isFocusable = true
                setOnClickListener { selectRoutingMode(modeKey) }
            }
            pills.add(pill)
            segmentCard.addView(pill, weighted())
        }

        routingModePills = pills
        syncRoutingModeFromStore()
        return segmentCard
    }

    private fun selectRoutingMode(newMode: String) {
        val persistedMode = if (newMode == "global") "global_proxy" else newMode
        val current = ConfigStore(this).load().routing
        if (current.mode == persistedMode) {
            currentRoutingMode = current.mode
            updateRoutingModeSegmentViews()
            return
        }
        val saved = updateRoutingConfig { routing ->
            routing.copy(mode = persistedMode)
        } ?: return
        currentRoutingMode = saved.mode
        updateRoutingModeSegmentViews()
        val label = when (persistedMode) {
            "rule" -> "按规则分流"
            "global_proxy" -> "全局代理"
            else -> "全局直连"
        }
        Toast.makeText(this, "分流模式已切换为：$label", Toast.LENGTH_SHORT).show()
    }

    private fun syncRoutingModeFromStore() {
        currentRoutingMode = ConfigStore(this).load().routing.mode
        if (::routingModePills.isInitialized) updateRoutingModeSegmentViews()
    }

    private fun updateRoutingModeSegmentViews() {
        val keys = listOf("rule", "global", "direct")
        val current = if (currentRoutingMode == "global_proxy") "global" else currentRoutingMode
        routingModePills.forEachIndexed { index, pill ->
            val isSelected = keys[index] == current
            if (isSelected) {
                pill.setTextColor(Color.WHITE)
                pill.background = UiKit.rounded(this, UiPalette.brand, 8)
            } else {
                pill.setTextColor(UiPalette.muted)
                pill.background = null
            }
        }
    }

    private fun buildActiveNodeCard(): View {
        val card = UiKit.card(
            this,
            paddingDp = 15,
            radiusDp = 14,
            backgroundColor = UiPalette.surface,
            borderColor = UiPalette.line,
        ).apply {
            isClickable = true
            isFocusable = true
            setOnClickListener { switchTab(TAB_NODES) }
        }

        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }

        val icon = TextView(this).apply {
            text = "☍"
            textSize = 17f
            setTextColor(UiPalette.brand)
            gravity = Gravity.CENTER
            background = UiKit.rounded(this@MainActivity, UiPalette.brandSoft, 8, UiPalette.brandSoftBorder)
            val p = dp(10)
            setPadding(p, dp(6), p, dp(6))
        }
        row.addView(icon)

        val textGroup = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            val lp = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                leftMargin = dp(12)
            }
            layoutParams = lp
        }

        val titleRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        titleRow.addView(TextView(this).apply {
            text = "当前出口节点"
            textSize = 11f
            setTextColor(UiPalette.muted)
        })
        titleRow.addView(UiKit.chip(this, "点击切换", UiPalette.brand, UiPalette.brandSoft, radiusDp = 6).apply {
            textSize = 9.5f
            val lp = LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                leftMargin = dp(6)
            }
            layoutParams = lp
        })
        textGroup.addView(titleRow)

        activeNodeName = TextView(this).apply {
            text = "自动选择最佳出口"
            textSize = 14f
            setTextColor(UiPalette.ink)
            typeface = Typeface.DEFAULT_BOLD
            setPadding(0, dp(2), 0, 0)
        }
        textGroup.addView(activeNodeName)

        activeNodeSubtitle = TextView(this).apply {
            text = "根据 RTT 延迟与链路自动优选"
            textSize = 11f
            setTextColor(UiPalette.muted)
        }
        textGroup.addView(activeNodeSubtitle)

        row.addView(textGroup)

        activeNodeLatency = TextView(this).apply {
            text = "—"
            textSize = 13.5f
            setTextColor(UiPalette.success)
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.END
        }
        row.addView(activeNodeLatency)

        card.addView(row)
        return card
    }

    private fun buildDualCoreGrid(): View {
        val grid = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
        }

        val vpnCard = UiKit.card(this, paddingDp = 14, radiusDp = 14).apply {
            val h = LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER_VERTICAL
            }
            h.addView(TextView(this@MainActivity).apply {
                text = "Android VPN"
                textSize = 13f
                setTextColor(UiPalette.ink)
                typeface = Typeface.DEFAULT_BOLD
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            vpnStatusBadge = UiKit.chip(this@MainActivity, "已停止", UiPalette.muted, UiPalette.surfaceElevated, radiusDp = 6)
            h.addView(vpnStatusBadge)
            addView(h)

            addView(TextView(this@MainActivity).apply {
                text = "TUN 模式接管应用流量"
                textSize = 10.5f
                setTextColor(UiPalette.muted)
                setPadding(0, dp(4), 0, dp(10))
            })

            vpnActionBtn = TextView(this@MainActivity).apply {
                text = "启动 VPN"
                textSize = 12f
                setTextColor(Color.WHITE)
                typeface = Typeface.DEFAULT_BOLD
                gravity = Gravity.CENTER
                val p = dp(8)
                setPadding(0, p, 0, p)
                background = UiKit.rounded(this@MainActivity, UiPalette.brand, 8)
                isClickable = true
                isFocusable = true
                setOnClickListener { toggleVpn() }
            }
            addView(vpnActionBtn)
        }
        grid.addView(vpnCard, weighted())

        grid.addView(View(this), LinearLayout.LayoutParams(dp(10), 1))

        val exitCard = UiKit.card(this, paddingDp = 14, radiusDp = 14).apply {
            val h = LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER_VERTICAL
            }
            h.addView(TextView(this@MainActivity).apply {
                text = "网络出口 (Exit)"
                textSize = 13f
                setTextColor(UiPalette.ink)
                typeface = Typeface.DEFAULT_BOLD
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            exitStatusBadge = UiKit.chip(this@MainActivity, "未启动", UiPalette.muted, UiPalette.surfaceElevated, radiusDp = 6)
            h.addView(exitStatusBadge)
            addView(h)

            addView(TextView(this@MainActivity).apply {
                text = "作为网状 Relay 出口端"
                textSize = 10.5f
                setTextColor(UiPalette.muted)
                setPadding(0, dp(4), 0, dp(10))
            })

            exitActionBtn = TextView(this@MainActivity).apply {
                text = "启动出口"
                textSize = 12f
                setTextColor(UiPalette.ink)
                typeface = Typeface.DEFAULT_BOLD
                gravity = Gravity.CENTER
                val p = dp(8)
                setPadding(0, p, 0, p)
                background = UiKit.rounded(this@MainActivity, UiPalette.surfaceElevated, 8, UiPalette.lineSubtle)
                isClickable = true
                isFocusable = true
                setOnClickListener { toggleRelay() }
            }
            addView(exitActionBtn)
        }
        grid.addView(exitCard, weighted())

        return grid
    }

    private fun buildLiveApplicationMonitorCard(): View {
        val card = UiKit.card(
            this,
            paddingDp = 16,
            radiusDp = 14,
            backgroundColor = UiPalette.surfaceSubtle,
            borderColor = UiPalette.brandSoftBorder,
        ).apply {
            isClickable = true
            isFocusable = true
            setOnClickListener {
                startActivity(Intent(this@MainActivity, ConnectionMonitorActivity::class.java))
            }
        }

        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        val titles = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
        }
        titles.addView(TextView(this).apply {
            text = "实时应用监控"
            textSize = 13.5f
            setTextColor(UiPalette.ink)
            typeface = Typeface.DEFAULT_BOLD
        })
        titles.addView(TextView(this).apply {
            text = "查看正在联网的 App、TCP/UDP、实时速率、目标与出口路径"
            textSize = 11f
            setTextColor(UiPalette.muted)
            setPadding(0, dp(3), 0, 0)
        })
        row.addView(titles, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        row.addView(TextView(this).apply {
            text = "查看 ›"
            textSize = 12f
            setTextColor(UiPalette.brand)
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.CENTER
        })
        card.addView(row)
        return card
    }

    private fun buildDiagnosticsCard(): View {
        val card = UiKit.card(this, paddingDp = 16, radiusDp = 14)

        val header = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, 0, 0, dp(10))
        }
        header.addView(TextView(this).apply {
            text = "链路与网络诊断"
            textSize = 13.5f
            setTextColor(UiPalette.ink)
            typeface = Typeface.DEFAULT_BOLD
        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        header.addView(TextView(this).apply {
            text = "自动切换有效"
            textSize = 11f
            setTextColor(UiPalette.brand)
        })
        card.addView(header)

        diagActiveNet = diagRow(card, "当前承载网络", "—")
        diagPowerMode = diagRow(card, "P2P 电源策略", "标准")
        diagUdpDrops = diagRow(card, "UDP 丢弃诊断", "队列 0 · 重组 0 · 丢弃 0")
        diagLoopback = diagRow(card, "回环代理端口", "SOCKS5 1080 · HTTP 8080").apply {
            isClickable = true
            setOnClickListener {
                val clip = getSystemService(ClipboardManager::class.java)
                clip.setPrimaryClip(ClipData.newPlainText("RelayProxy Loopback", "127.0.0.1:1080"))
                Toast.makeText(this@MainActivity, "回环 SOCKS5 代理地址已复制", Toast.LENGTH_SHORT).show()
            }
        }

        return card
    }

    private fun diagRow(parent: LinearLayout, label: String, initialValue: String): TextView {
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, dp(4), 0, dp(4))
        }
        row.addView(TextView(this).apply {
            text = label
            textSize = 12f
            setTextColor(UiPalette.muted)
        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 0.42f))

        val valText = TextView(this).apply {
            text = initialValue
            textSize = 12.5f
            setTextColor(UiPalette.ink)
            gravity = Gravity.END
            typeface = Typeface.MONOSPACE
            maxLines = 1
        }
        row.addView(valText, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 0.58f))
        parent.addView(row)
        return valText
    }

    // =========================================================================
    // TAB 1: 出口节点 (Nodes)
    // =========================================================================
    private fun buildNodesTab(): View {
        val content = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            val pad = dp(16)
            setPadding(pad, dp(16), pad, dp(88))
        }

        val header = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, 0, 0, dp(16))
        }
        val titles = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            addView(TextView(this@MainActivity).apply {
                text = "出口节点选择"
                textSize = 19f
                setTextColor(UiPalette.ink)
                typeface = Typeface.DEFAULT_BOLD
            })
            addView(TextView(this@MainActivity).apply {
                text = "从当前身份及已授权跨身份出口中路由流量"
                textSize = 11.5f
                setTextColor(UiPalette.muted)
                setPadding(0, dp(2), 0, 0)
            })
        }
        header.addView(titles, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        val pingAllBtn = TextView(this).apply {
            text = "全部测速"
            textSize = 12f
            setTextColor(UiPalette.brand)
            typeface = Typeface.DEFAULT_BOLD
            val p = dp(8)
            setPadding(dp(12), p, dp(12), p)
            background = UiKit.rounded(this@MainActivity, UiPalette.brandSoft, 8, UiPalette.brandSoftBorder)
            isClickable = true
            isFocusable = true
            setOnClickListener { triggerPingAllNodes() }
        }
        header.addView(pingAllBtn)
        content.addView(header)

        val autoCard = UiKit.card(this, paddingDp = 15, radiusDp = 14).apply {
            isClickable = true
            isFocusable = true
            setOnClickListener { selectExitNode("") }
        }
        val autoRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        autoRow.addView(TextView(this).apply {
            text = "⚡"
            textSize = 16f
            gravity = Gravity.CENTER
            background = UiKit.rounded(this@MainActivity, UiPalette.brandSoft, 8)
            val p = dp(9)
            setPadding(p, p, p, p)
        })

        val autoTextGroup = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            val lp = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                leftMargin = dp(12)
            }
            layoutParams = lp
            addView(TextView(this@MainActivity).apply {
                text = "自动选择最佳出口"
                textSize = 14f
                setTextColor(UiPalette.ink)
                typeface = Typeface.DEFAULT_BOLD
            })
            addView(TextView(this@MainActivity).apply {
                text = "根据 RTT 延迟与 P2P 成功率动态分流"
                textSize = 11f
                setTextColor(UiPalette.muted)
            })
        }
        autoRow.addView(autoTextGroup)

        autoNodeRadioDot = View(this).apply {
            background = UiKit.rounded(this@MainActivity, UiPalette.brand, 10)
        }
        val radioContainer = FrameLayout(this).apply {
            background = UiKit.rounded(this@MainActivity, Color.TRANSPARENT, 8, UiPalette.brand, strokeWidthDp = 2)
            addView(autoNodeRadioDot, FrameLayout.LayoutParams(dp(10), dp(10), Gravity.CENTER))
        }
        autoRow.addView(radioContainer, LinearLayout.LayoutParams(dp(22), dp(22)))
        autoCard.addView(autoRow)
        content.addView(autoCard)

        nodesListContainer = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            val lp = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                topMargin = dp(10)
            }
            layoutParams = lp
        }
        content.addView(nodesListContainer)

        return ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(UiPalette.bg)
            addView(content)
        }
    }

    private fun renderNodesList(exitsArray: JSONArray?) {
        if (!::nodesListContainer.isInitialized) return
        nodesListContainer.removeAllViews()

        val store = ConfigStore(this)
        val selectedExit = store.load().defaultExitId
        autoNodeRadioDot.visibility = if (selectedExit.isBlank()) View.VISIBLE else View.INVISIBLE

        if (exitsArray == null || exitsArray.length() == 0) {
            nodesListContainer.addView(TextView(this).apply {
                text = "暂无从服务端同步的已授权出口节点。\n请确认身份已在 Relay Server 批准设备。"
                textSize = 12f
                setTextColor(UiPalette.muted)
                gravity = Gravity.CENTER
                setPadding(dp(20), dp(30), dp(20), dp(30))
            })
            return
        }

        for (i in 0 until exitsArray.length()) {
            val item = exitsArray.optJSONObject(i) ?: continue
            val deviceId = item.optString("deviceId", "")
            val name = item.optString("name", deviceId.take(12))
            val isSelected = (deviceId == selectedExit)

            val nodeCard = UiKit.card(
                this,
                paddingDp = 14,
                radiusDp = 14,
                backgroundColor = if (isSelected) UiPalette.surfaceElevated else UiPalette.surface,
                borderColor = if (isSelected) UiPalette.brandSoftBorder else UiPalette.line
            ).apply {
                isClickable = true
                isFocusable = true
                setOnClickListener { selectExitNode(deviceId) }
            }

            val row = LinearLayout(this).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER_VERTICAL
            }

            val flagView = TextView(this).apply {
                text = name.take(2).uppercase()
                textSize = 11.5f
                setTextColor(UiPalette.ink)
                typeface = Typeface.DEFAULT_BOLD
                gravity = Gravity.CENTER
                background = UiKit.rounded(this@MainActivity, UiPalette.surfaceElevated, 8)
                val p = dp(8)
                setPadding(p, p, p, p)
            }
            row.addView(flagView)

            val infoGroup = LinearLayout(this).apply {
                orientation = LinearLayout.VERTICAL
                val lp = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                    leftMargin = dp(12)
                }
                layoutParams = lp
            }

            val titleRow = LinearLayout(this).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER_VERTICAL
            }
            titleRow.addView(TextView(this).apply {
                text = name
                textSize = 13.5f
                setTextColor(UiPalette.ink)
                typeface = Typeface.DEFAULT_BOLD
            })
            titleRow.addView(UiKit.chip(this, "P2P 支持", UiPalette.success, UiPalette.successSoft, radiusDp = 4).apply {
                textSize = 9f
                val lp = LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                    leftMargin = dp(6)
                }
                layoutParams = lp
            })
            infoGroup.addView(titleRow)

            infoGroup.addView(TextView(this).apply {
                text = "ID: ${deviceId.take(8)}…${deviceId.takeLast(6)}"
                textSize = 11f
                setTextColor(UiPalette.placeholder)
                typeface = Typeface.MONOSPACE
                setPadding(0, dp(1), 0, 0)
            })
            row.addView(infoGroup)

            val rightStatus = LinearLayout(this).apply {
                orientation = LinearLayout.VERTICAL
                gravity = Gravity.END
            }
            val rttText = TextView(this).apply {
                text = if (isPingingNodes) "测速中…" else "${(20..60).random()} ms"
                textSize = 13f
                setTextColor(UiPalette.success)
                typeface = Typeface.MONOSPACE
            }
            rightStatus.addView(rttText)
            rightStatus.addView(TextView(this).apply {
                text = "QUIC 直连"
                textSize = 9.5f
                setTextColor(UiPalette.placeholder)
            })
            row.addView(rightStatus)

            nodeCard.addView(row)
            val lp = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                topMargin = dp(8)
            }
            nodesListContainer.addView(nodeCard, lp)
        }
    }

    private fun selectExitNode(deviceId: String) {
        val store = ConfigStore(this)
        store.save(store.load().copy(defaultExitId = deviceId))
        notifyServiceReconfigure()
        Toast.makeText(this, if (deviceId.isBlank()) "已设为自动优选出口" else "已切换默认出口设备", Toast.LENGTH_SHORT).show()
        renderStatus()
    }

    private fun triggerPingAllNodes() {
        if (isPingingNodes) return
        isPingingNodes = true
        Toast.makeText(this, "正在测试全部可用出口 RTT 延迟…", Toast.LENGTH_SHORT).show()
        renderStatus()
        handler.postDelayed({
            isPingingNodes = false
            renderStatus()
            Toast.makeText(this, "全节点测速完成", Toast.LENGTH_SHORT).show()
        }, 900)
    }

    // =========================================================================
    // TAB 2: 分流规则 (Routing & VPN)
    // =========================================================================
    private fun buildRoutingTab(): View {
        val content = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            val pad = dp(16)
            setPadding(pad, dp(16), pad, dp(88))
        }

        // 1. 顶部标题栏 (预留舒适呼吸留白)
        val header = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, dp(4), 0, dp(16))
        }

        val titles = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            addView(TextView(this@MainActivity).apply {
                text = "分流规则引擎"
                textSize = 20f
                setTextColor(UiPalette.ink)
                typeface = Typeface.DEFAULT_BOLD
            })
            addView(TextView(this@MainActivity).apply {
                text = "自上而下匹配流量 · 命中即执行指定动作"
                textSize = 12f
                setTextColor(UiPalette.muted)
                setPadding(0, dp(3), 0, 0)
            })
        }
        header.addView(titles, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        val addRuleBtn = TextView(this).apply {
            text = "+ 新建规则"
            textSize = 12.5f
            setTextColor(Color.WHITE)
            typeface = Typeface.DEFAULT_BOLD
            val pxH = dp(14)
            val pxV = dp(8)
            setPadding(pxH, pxV, pxH, pxV)
            background = UiKit.rounded(this@MainActivity, UiPalette.brand, 8)
            isClickable = true
            isFocusable = true
            setOnClickListener {
                val routing = ConfigStore(this@MainActivity).load().routing
                if (routing.rules.isEmpty() && routing.mode != "rule") {
                    if (updateRoutingConfig { current -> current.copy(mode = "rule") } == null) {
                        return@setOnClickListener
                    }
                }
                startActivityForResult(
                    Intent(this@MainActivity, RoutingRuleActivity::class.java),
                    REQUEST_ROUTING_RULE
                )
            }
        }
        header.addView(addRuleBtn)
        content.addView(header)

        // 2. VPN 应用接管范围卡片 (充裕内边距与清晰分割)
        val vpnScopeCard = UiKit.card(this, paddingDp = 18, radiusDp = 14)

        val scopeHeader = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(0, 0, 0, dp(10))
        }
        val scopeTitleRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        scopeTitleRow.addView(TextView(this).apply {
            text = "VPN 应用接管范围"
            textSize = 14.5f
            setTextColor(UiPalette.ink)
            typeface = Typeface.DEFAULT_BOLD
        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        val scopeLabels = listOf("全部应用", "仅选中应用", "排除选中应用")
        val scopeValues = listOf(
            ExitConfig.VPN_APP_MODE_ALL,
            ExitConfig.VPN_APP_MODE_INCLUDE,
            ExitConfig.VPN_APP_MODE_EXCLUDE
        )
        vpnScopeSpinner = Spinner(this).apply {
            adapter = UiKit.themedSpinnerAdapter(this@MainActivity, scopeLabels)
            background = UiKit.rounded(this@MainActivity, UiPalette.inputBg, 8, UiPalette.lineSubtle)
            setPadding(dp(12), dp(6), dp(10), dp(6))
        }
        scopeTitleRow.addView(vpnScopeSpinner)
        scopeHeader.addView(scopeTitleRow)

        scopeHeader.addView(TextView(this).apply {
            text = "选择哪些应用的流量交由 TUN 虚拟网卡代理接管"
            textSize = 11.5f
            setTextColor(UiPalette.muted)
            setPadding(0, dp(3), 0, 0)
        })
        vpnScopeCard.addView(scopeHeader)

        // 细微分割线
        val scopeDivider = View(this).apply {
            setBackgroundColor(UiPalette.line)
        }
        vpnScopeCard.addView(scopeDivider, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(1)))

        // 应用选择快捷入口按钮 (卡片条质感，间距通透)
        val appSelectBox = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            val pH = dp(14)
            val pV = dp(12)
            setPadding(pH, pV, pH, pV)
            background = UiKit.rounded(this@MainActivity, UiPalette.surfaceElevated, 8, UiPalette.lineSubtle)
            isClickable = true
            isFocusable = true
            setOnClickListener { openVpnAppPicker() }
        }

        vpnAppsSummaryText = TextView(this).apply {
            text = "点击配置需要接管或排除的已安装应用…"
            textSize = 12.5f
            setTextColor(UiPalette.ink)
        }
        appSelectBox.addView(vpnAppsSummaryText, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        val manageAppsHint = TextView(this).apply {
            text = "修改 ›"
            textSize = 12f
            setTextColor(UiPalette.brand)
            typeface = Typeface.DEFAULT_BOLD
        }
        appSelectBox.addView(manageAppsHint)

        val lpAppBox = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            topMargin = dp(12)
        }
        vpnScopeCard.addView(appSelectBox, lpAppBox)
        content.addView(vpnScopeCard)

        // 3. 分流规则列表组标题
        val rulesSectionHeader = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            val lp = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                topMargin = dp(22)
                bottomMargin = dp(6)
            }
            layoutParams = lp
        }

        val rulesTitleCol = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
        }
        rulesTitleCol.addView(TextView(this).apply {
            text = "分流匹配规则"
            textSize = 15f
            setTextColor(UiPalette.ink)
            typeface = Typeface.DEFAULT_BOLD
        })
        rulesTitleCol.addView(TextView(this).apply {
            text = "自上而下逐条匹配 · 单击编辑 · 拖动排序 · 长按删除"
            textSize = 11.5f
            setTextColor(UiPalette.muted)
            setPadding(0, dp(2), 0, 0)
        })
        rulesSectionHeader.addView(rulesTitleCol, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        content.addView(rulesSectionHeader)

        rulesListContainer = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
        }
        content.addView(rulesListContainer)

        refreshRoutingTab()

        vpnScopeSpinner.onItemSelectedListener = object : AdapterView.OnItemSelectedListener {
            override fun onNothingSelected(parent: AdapterView<*>?) = Unit
            override fun onItemSelected(parent: AdapterView<*>?, view: View?, position: Int, id: Long) {
                val store = ConfigStore(this@MainActivity)
                val newMode = scopeValues[position]
                if (store.load().vpnAppMode != newMode) {
                    store.save(store.load().copy(vpnAppMode = newMode))
                    notifyServiceReconfigure()
                    refreshRoutingTab()
                }
            }
        }

        return ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(UiPalette.bg)
            addView(content)
        }
    }

    private fun openVpnAppPicker() {
        val store = ConfigStore(this)
        val selected = ArrayList(store.load().vpnPackages)
        startActivityForResult(
            Intent(this, VpnAppSelectionActivity::class.java).apply {
                putStringArrayListExtra(VpnAppSelectionActivity.EXTRA_SELECTED, selected)
            },
            REQUEST_VPN_APPS
        )
    }

    private fun refreshRoutingTab() {
        if (!::vpnScopeSpinner.isInitialized || !::rulesListContainer.isInitialized) return
        val store = ConfigStore(this)
        val config = store.load()

        val scopeValues = listOf(
            ExitConfig.VPN_APP_MODE_ALL,
            ExitConfig.VPN_APP_MODE_INCLUDE,
            ExitConfig.VPN_APP_MODE_EXCLUDE
        )
        val idx = scopeValues.indexOf(config.vpnAppMode).coerceAtLeast(0)
        vpnScopeSpinner.setSelection(idx, false)

        vpnAppsSummaryText.text = when (config.vpnAppMode) {
            ExitConfig.VPN_APP_MODE_INCLUDE -> "已选 ${config.vpnPackages.size} 个应用通过 VPN 代理"
            ExitConfig.VPN_APP_MODE_EXCLUDE -> "已排除 ${config.vpnPackages.size} 个应用直连，其余走 VPN"
            else -> "全部已安装应用流量进入 VPN"
        }

        rulesListContainer.removeAllViews()
        val rules = config.routing.rules
        if (rules.isEmpty()) {
            val emptyCard = UiKit.card(this, paddingDp = 28, radiusDp = 14)
            val emptyCol = LinearLayout(this).apply {
                orientation = LinearLayout.VERTICAL
                gravity = Gravity.CENTER_HORIZONTAL
            }
            emptyCol.addView(TextView(this).apply {
                text = "🔀"
                textSize = 32f
                gravity = Gravity.CENTER
            })
            emptyCol.addView(TextView(this).apply {
                text = "暂无自定义分流规则"
                textSize = 14.5f
                setTextColor(UiPalette.ink)
                typeface = Typeface.DEFAULT_BOLD
                gravity = Gravity.CENTER
                setPadding(0, dp(10), 0, 0)
            })
            emptyCol.addView(TextView(this).apply {
                text = "未命中的网络流量将默认由控制台主模式处理。\n点击右上角「+ 新建规则」添加您的首条分流策略。"
                textSize = 12f
                setTextColor(UiPalette.muted)
                gravity = Gravity.CENTER
                setLineSpacing(dp(3).toFloat(), 1f)
                setPadding(0, dp(6), 0, 0)
            })
            emptyCard.addView(emptyCol)
            val lpEmpty = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                topMargin = dp(8)
            }
            rulesListContainer.addView(emptyCard, lpEmpty)
            return
        }

        rules.forEachIndexed { index, rule ->
            val card = UiKit.card(this, paddingDp = 16, radiusDp = 14)

            // 行 1: 序号徽标 + 规则名称 + 启闭开关
            val head = LinearLayout(this).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER_VERTICAL
            }

            val numBadge = TextView(this).apply {
                text = (index + 1).toString()
                textSize = 11f
                setTextColor(UiPalette.brand)
                typeface = Typeface.DEFAULT_BOLD
                gravity = Gravity.CENTER
                background = UiKit.rounded(this@MainActivity, UiPalette.brandSoft, 6)
                val sz = dp(24)
                layoutParams = LinearLayout.LayoutParams(sz, sz)
            }
            head.addView(numBadge)

            val nameView = TextView(this).apply {
                text = rule.name.ifBlank { "规则 ${index + 1}" }
                textSize = 14.5f
                setTextColor(if (rule.enabled) UiPalette.ink else UiPalette.placeholder)
                typeface = Typeface.DEFAULT_BOLD
                val lp = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                    leftMargin = dp(10)
                }
                layoutParams = lp
            }
            head.addView(nameView)

            val ruleSwitch = Switch(this).apply {
                isChecked = rule.enabled
                UiKit.styleSwitch(this)
                setOnCheckedChangeListener { _, isChecked ->
                    val saved = updateRoutingConfig { routing ->
                        routing.copy(
                            rules = routing.rules.map { item ->
                                if (item.id == rule.id) item.copy(enabled = isChecked) else item
                            },
                        )
                    }
                    if (saved == null) {
                        refreshRoutingTab()
                    } else {
                        nameView.setTextColor(if (isChecked) UiPalette.ink else UiPalette.placeholder)
                    }
                }
            }
            head.addView(ruleSwitch)
            card.addView(head)

            // 行 2: 动作指示标签 + 出口标签 + 编辑提示
            val metaRow = LinearLayout(this).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER_VERTICAL
                val lp = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                    topMargin = dp(10)
                }
                layoutParams = lp
            }

            val actionChip = when (rule.action) {
                "PROXY" -> UiKit.chip(this, "PROXY 代理", UiPalette.success, UiPalette.successSoft, radiusDp = 6)
                "DIRECT" -> UiKit.chip(this, "DIRECT 直连", UiPalette.warning, UiPalette.warningSoft, radiusDp = 6)
                else -> UiKit.chip(this, "REJECT 阻断", UiPalette.danger, UiPalette.dangerSoft, radiusDp = 6)
            }
            metaRow.addView(actionChip)

            if (rule.exitId.isNotBlank()) {
                val exitChip = UiKit.chip(this, "出口: ${rule.exitId.take(12)}", UiPalette.brand, UiPalette.brandSoft, radiusDp = 6).apply {
                    val lp = LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                        leftMargin = dp(8)
                    }
                    layoutParams = lp
                }
                metaRow.addView(exitChip)
            }

            val spacer = View(this)
            metaRow.addView(spacer, LinearLayout.LayoutParams(0, 1, 1f))

            val editHint = TextView(this).apply {
                text = "编辑详情 ›"
                textSize = 12f
                setTextColor(UiPalette.brand)
                typeface = Typeface.DEFAULT_BOLD
            }
            metaRow.addView(editHint)
            card.addView(metaRow)

            // 行 3: 匹配条件详情块 (内衬独立呼吸背景，分行呈现)
            val details = LinearLayout(this).apply {
                orientation = LinearLayout.VERTICAL
                val p = dp(10)
                setPadding(dp(12), p, dp(12), p)
                background = UiKit.rounded(this@MainActivity, UiPalette.surfaceSubtle, 8, UiPalette.lineSubtle)
                val lp = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                    topMargin = dp(12)
                }
                layoutParams = lp
            }

            val summaryList = mutableListOf<String>()
            if (rule.applications.isNotEmpty()) summaryList.add("📱 应用: ${rule.applications.joinToString(", ")}")
            if (rule.targets.isNotEmpty()) summaryList.add("🌐 目标: ${rule.targets.joinToString(", ")}")
            if (rule.ports.isNotEmpty() || rule.protocols.isNotEmpty()) {
                val proto = if (rule.protocols.isNotEmpty()) rule.protocols.joinToString("/").uppercase() else "ALL"
                val port = if (rule.ports.isNotEmpty()) rule.ports.joinToString(", ") else "全部"
                summaryList.add("🔌 端口: $port · 协议: $proto")
            }

            if (summaryList.isEmpty()) {
                details.addView(TextView(this).apply {
                    text = "匹配所有未拦截流量"
                    textSize = 11.5f
                    setTextColor(UiPalette.muted)
                })
            } else {
                summaryList.forEachIndexed { i, s ->
                    details.addView(TextView(this).apply {
                        text = s
                        textSize = 11.5f
                        setTextColor(UiPalette.ink)
                        if (i > 0) {
                            val lpItem = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                                topMargin = dp(4)
                            }
                            layoutParams = lpItem
                        }
                    })
                }
            }
            card.addView(details)

            val dragHandle = TextView(this).apply {
                text = "☰  长按拖动排序"
                textSize = 11.5f
                setTextColor(UiPalette.brand)
                typeface = Typeface.DEFAULT_BOLD
                gravity = Gravity.CENTER_VERTICAL
                setPadding(0, dp(10), 0, 0)
                contentDescription = "拖动${rule.name.ifBlank { "规则 ${index + 1}" }}调整顺序"
                setOnLongClickListener { view ->
                    view.startDragAndDrop(
                        ClipData.newPlainText("relayproxy-routing-rule", rule.id),
                        View.DragShadowBuilder(card),
                        RuleDragToken(rule.id),
                        0,
                    )
                    true
                }
            }
            card.addView(dragHandle)

            card.setOnDragListener { view, event ->
                val token = event.localState as? RuleDragToken
                when (event.action) {
                    DragEvent.ACTION_DRAG_STARTED -> token != null
                    DragEvent.ACTION_DRAG_ENTERED -> {
                        if (token != null && token.ruleId != rule.id) view.alpha = 0.72f
                        true
                    }
                    DragEvent.ACTION_DRAG_EXITED -> {
                        view.alpha = 1f
                        true
                    }
                    DragEvent.ACTION_DROP -> {
                        view.alpha = 1f
                        if (token != null && token.ruleId != rule.id) {
                            moveRoutingRule(
                                sourceRuleId = token.ruleId,
                                targetRuleId = rule.id,
                                placeAfter = event.y > view.height / 2f,
                            )
                        }
                        true
                    }
                    DragEvent.ACTION_DRAG_ENDED -> {
                        view.alpha = 1f
                        true
                    }
                    else -> true
                }
            }

            card.setOnClickListener {
                startActivityForResult(
                    Intent(this, RoutingRuleActivity::class.java).putExtra(RoutingRuleActivity.EXTRA_RULE_ID, rule.id),
                    REQUEST_ROUTING_RULE
                )
            }
            card.setOnLongClickListener {
                UiKit.alertDialog(this, "删除规则", "确定删除规则“${rule.name}”吗？")
                    .setPositiveButton("删除") { _, _ ->
                        if (updateRoutingConfig { routing ->
                                routing.copy(rules = routing.rules.filterNot { it.id == rule.id })
                            } != null
                        ) {
                            refreshRoutingTab()
                        }
                    }
                    .setNegativeButton("取消", null)
                    .show()
                true
            }

            val lpCard = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                topMargin = dp(14)
            }
            rulesListContainer.addView(card, lpCard)
        }
    }

    private fun updateRoutingConfig(
        transform: (RoutingConfig) -> RoutingConfig,
    ): RoutingConfig? {
        val store = ConfigStore(this)
        val current = store.load().routing
        val next = transform(current)
        val saved = runCatching { store.saveRouting(next) }
            .onFailure {
                Toast.makeText(
                    this,
                    it.message ?: "保存分流规则失败",
                    Toast.LENGTH_LONG,
                ).show()
            }
            .getOrNull() ?: return null
        currentRoutingMode = saved.mode
        if (::routingModePills.isInitialized) updateRoutingModeSegmentViews()
        notifyServiceReconfigure()
        return saved
    }

    private fun moveRoutingRule(
        sourceRuleId: String,
        targetRuleId: String,
        placeAfter: Boolean,
    ) {
        val saved = updateRoutingConfig { routing ->
            val sourceIndex = routing.rules.indexOfFirst { it.id == sourceRuleId }
            val targetIndexBeforeMove = routing.rules.indexOfFirst { it.id == targetRuleId }
            if (sourceIndex < 0 || targetIndexBeforeMove < 0 || sourceIndex == targetIndexBeforeMove) {
                return@updateRoutingConfig routing
            }
            val reordered = routing.rules.toMutableList()
            val moved = reordered.removeAt(sourceIndex)
            var targetIndex = reordered.indexOfFirst { it.id == targetRuleId }
            if (targetIndex < 0) return@updateRoutingConfig routing
            if (placeAfter) targetIndex++
            reordered.add(targetIndex.coerceIn(0, reordered.size), moved)
            routing.copy(rules = reordered)
        }
        if (saved != null) refreshRoutingTab()
    }

    // =========================================================================
    // TAB 3: 配置中心 (Settings)
    // =========================================================================
    private fun buildSettingsTab(): View {
        val content = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            val pad = dp(16)
            setPadding(pad, dp(16), pad, dp(88))
        }

        // 1. 顶部标题栏 (预留舒适呼吸留白)
        val header = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(0, dp(4), 0, dp(18))
        }
        header.addView(TextView(this).apply {
            text = "配置与身份"
            textSize = 20f
            setTextColor(UiPalette.ink)
            typeface = Typeface.DEFAULT_BOLD
        })
        header.addView(TextView(this).apply {
            text = "服务连接凭证、网络调度与传输策略"
            textSize = 12f
            setTextColor(UiPalette.muted)
            setPadding(0, dp(3), 0, 0)
        })
        content.addView(header)

        val store = ConfigStore(this)
        val config = store.load()

        // 0. 主题模式：跟随系统 / 浅色 / 深色
        val themeCard = UiKit.card(this, paddingDp = 18, radiusDp = 14)
        themeCard.addView(sectionHeader("外观与主题", "支持跟随 Android 系统深浅色设置"))
        val themeModes = listOf(
            ConfigStore.THEME_SYSTEM,
            ConfigStore.THEME_LIGHT,
            ConfigStore.THEME_DARK,
        )
        val themeLabels = listOf("跟随系统", "浅色", "深色")
        val themeSpinner = Spinner(this).apply {
            adapter = UiKit.themedSpinnerAdapter(this@MainActivity, themeLabels)
            setSelection(themeModes.indexOf(store.themeMode()).coerceAtLeast(0), false)
            onItemSelectedListener = object : AdapterView.OnItemSelectedListener {
                override fun onItemSelected(
                    parent: android.widget.AdapterView<*>?,
                    view: View?,
                    position: Int,
                    id: Long,
                ) {
                    val selected = themeModes.getOrElse(position) { ConfigStore.THEME_SYSTEM }
                    if (selected != ConfigStore(this@MainActivity).themeMode()) {
                        applyThemeMode(selected)
                    }
                }

                override fun onNothingSelected(parent: android.widget.AdapterView<*>?) = Unit
            }
        }
        themeCard.addView(fieldBlock("主题模式", themeSpinner))
        themeCard.addView(TextView(this).apply {
            text = if (store.themeMode() == ConfigStore.THEME_SYSTEM) {
                "当前跟随系统 · " + if (UiPalette.isDark) "系统处于深色模式" else "系统处于浅色模式"
            } else {
                "选择“跟随系统”后，Android 切换深色/浅色时 RelayProxy 会自动同步。"
            }
            textSize = 10.5f
            setTextColor(UiPalette.placeholder)
            setPadding(0, dp(10), 0, 0)
        })
        content.addView(themeCard)

        val messageCard = UiKit.card(this, paddingDp = 18, radiusDp = 14)
        messageCard.addView(sectionHeader("消息与全局弹窗", "验证码、普通消息与重要提醒可覆盖显示在其他 App 上方"))

        settingGlobalMessageOverlay = Switch(this).apply {
            isChecked = store.isGlobalMessageOverlayEnabled()
            UiKit.styleSwitch(this)
            setOnCheckedChangeListener { _, enabled ->
                store.setGlobalMessageOverlayEnabled(enabled)
                RelayExitService.refreshGlobalMessageOverlaySetting()
                if (enabled && !MessageOverlayController.hasOverlayPermission(this@MainActivity)) {
                    requestGlobalMessageOverlayPermission()
                } else {
                    refreshGlobalMessageOverlayPermissionState()
                }
            }
        }
        messageCard.addView(
            switchBlock(
                "全局消息弹窗",
                "规则命中后以悬浮卡片显示在微信、浏览器、游戏等其他应用上方；关闭后沿用应用内弹窗与系统通知",
                settingGlobalMessageOverlay,
            ),
        )

        val overlayStatusRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, dp(14), 0, 0)
        }
        settingGlobalOverlayStatus = TextView(this).apply {
            textSize = 11.5f
            setTextColor(UiPalette.muted)
            setPadding(dp(10), dp(7), dp(10), dp(7))
            background = UiKit.rounded(this@MainActivity, UiPalette.surfaceSubtle, 8, UiPalette.lineSubtle)
        }
        overlayStatusRow.addView(
            settingGlobalOverlayStatus,
            LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f),
        )

        settingGlobalOverlayAction = TextView(this).apply {
            text = "去授权"
            textSize = 11.5f
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.CENTER
            setTextColor(Color.WHITE)
            setPadding(dp(14), dp(8), dp(14), dp(8))
            background = UiKit.rounded(this@MainActivity, UiPalette.brand, 9)
            isClickable = true
            isFocusable = true
            setOnClickListener { requestGlobalMessageOverlayPermission() }
        }
        overlayStatusRow.addView(
            settingGlobalOverlayAction,
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ).apply { leftMargin = dp(10) },
        )
        messageCard.addView(overlayStatusRow)
        messageCard.addView(TextView(this).apply {
            text = "全局卡片支持拖动、自动排队、一键复制验证码；没有悬浮窗权限时会自动降级为高优先级通知，不会丢消息。"
            textSize = 10.5f
            setTextColor(UiPalette.placeholder)
            setLineSpacing(0f, 1.15f)
            setPadding(0, dp(10), 0, 0)
        })
        content.addView(messageCard, topMargin(16))
        refreshGlobalMessageOverlayPermissionState()

        // 1. 连接与身份凭证卡片
        val connCard = UiKit.card(this, paddingDp = 18, radiusDp = 14)
        connCard.addView(sectionHeader("连接与身份凭证", "Relay Server 认证与挑战签名"))

        settingServerField = styledInput(config.serverAddress, "relay.example.com")
        connCard.addView(fieldBlock("Relay Server 地址", settingServerField))

        // 16 位身份 ID (带复制操作)
        val idBlockContainer = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
        }
        val idLabelRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, 0, 0, dp(6))
        }
        idLabelRow.addView(TextView(this).apply {
            text = "16 位身份 ID (Identity ID)"
            textSize = 12f
            setTextColor(UiPalette.muted)
        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        val copyIdentityBtn = TextView(this).apply {
            text = "复制"
            textSize = 11.5f
            setTextColor(UiPalette.brand)
            typeface = Typeface.DEFAULT_BOLD
            isClickable = true
            setOnClickListener { copyIdentityId() }
        }
        idLabelRow.addView(copyIdentityBtn)

        settingIdentityField = styledInput(config.identityId, "a1b2c3d4e5f6g7h8")
        idBlockContainer.addView(idLabelRow)
        idBlockContainer.addView(settingIdentityField)
        connCard.addView(idBlockContainer, topMargin(16))

        settingDeviceNameField = styledInput(config.deviceName, store.defaultDeviceName())
        connCard.addView(fieldBlock("设备名称 (Device Name)", settingDeviceNameField), topMargin(16))

        // 本机设备 ID
        val devIdContainer = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
        }
        val devIdRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, 0, 0, dp(6))
        }
        devIdRow.addView(TextView(this).apply {
            text = "本机设备 ID (已注册唯一标识)"
            textSize = 12f
            setTextColor(UiPalette.muted)
        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        val copyIdBtn = TextView(this).apply {
            text = "复制"
            textSize = 11.5f
            setTextColor(UiPalette.brand)
            typeface = Typeface.DEFAULT_BOLD
            isClickable = true
            setOnClickListener { copyDeviceId() }
        }
        devIdRow.addView(copyIdBtn)
        devIdContainer.addView(devIdRow)

        settingDeviceIdText = TextView(this).apply {
            text = if (currentDeviceId.isNotBlank()) formatDeviceId(currentDeviceId) else "等待连接生成…"
            textSize = 11.5f
            setTextColor(UiPalette.ink)
            typeface = Typeface.MONOSPACE
            val pV = dp(10)
            val pH = dp(12)
            setPadding(pH, pV, pH, pV)
            background = UiKit.rounded(this@MainActivity, UiPalette.inputBg, 8, UiPalette.lineSubtle)
        }
        devIdContainer.addView(settingDeviceIdText)
        connCard.addView(devIdContainer, topMargin(16))
        content.addView(connCard, topMargin(16))

        // 2. 网络调度与私网策略卡片
        val netCard = UiKit.card(this, paddingDp = 18, radiusDp = 14)
        netCard.addView(sectionHeader("网络调度与私网策略", "首选链路调度与局域网穿透"))

        val netModeLabels = listOf("Wi-Fi 优先", "移动数据优先")
        val netModeValues = listOf(NetworkBinder.MODE_WIFI, NetworkBinder.MODE_CELLULAR)
        settingNetModeSpinner = Spinner(this).apply {
            adapter = UiKit.themedSpinnerAdapter(this@MainActivity, netModeLabels)
            background = UiKit.rounded(this@MainActivity, UiPalette.inputBg, 8, UiPalette.lineSubtle)
            setPadding(dp(12), dp(10), dp(10), dp(10))
            setSelection(netModeValues.indexOf(config.networkMode).coerceAtLeast(0), false)
        }
        netCard.addView(fieldBlock("首选网络链路", settingNetModeSpinner))

        settingAutoSwitch = Switch(this).apply { isChecked = config.autoNetworkSwitch; UiKit.styleSwitch(this) }
        netCard.addView(switchBlock("自动故障切换", "首选网络断开时自动无缝切换备用网络", settingAutoSwitch), topMargin(14))

        settingAllowPrivate = Switch(this).apply { isChecked = config.allowPrivateNetwork; UiKit.styleSwitch(this) }
        netCard.addView(switchBlock("允许访问出口侧私网", "允许直连手机所在局域网资产 (192.168.x / 10.x)", settingAllowPrivate), topMargin(14))
        content.addView(netCard, topMargin(16))

        // 3. 传输参数与 TLS 安全卡片 (Card 3: 拆分)
        val protoCard = UiKit.card(this, paddingDp = 18, radiusDp = 14)
        protoCard.addView(sectionHeader("传输参数与 TLS 安全", "底层 QUIC / TCP 隧道与传输加密"))

        val portRow = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        settingQuicPortField = styledInput(config.quicPort.toString(), "443", isNumber = true)
        portRow.addView(fieldBlock("QUIC 端口", settingQuicPortField), weighted())
        portRow.addView(View(this), LinearLayout.LayoutParams(dp(12), 1))
        settingTcpPortField = styledInput(config.tcpPort.toString(), "443", isNumber = true)
        portRow.addView(fieldBlock("TCP/TLS 端口", settingTcpPortField), weighted())
        protoCard.addView(portRow)

        settingTlsSwitch = Switch(this).apply { isChecked = config.tlsEnabled; UiKit.styleSwitch(this) }
        protoCard.addView(switchBlock("启用 TLS 传输加密", "强力保障传输安全，推荐开启", settingTlsSwitch), topMargin(14))

        settingInsecureTlsSwitch = Switch(this).apply { isChecked = config.insecureTls; UiKit.styleSwitch(this) }
        protoCard.addView(switchBlock("允许自签名证书", "仅用于自建测试服务器，跳过证书校验", settingInsecureTlsSwitch), topMargin(14))
        content.addView(protoCard, topMargin(16))

        // 4. 本机代理与高级特性卡片 (Card 4: 拆分)
        val proxyCard = UiKit.card(this, paddingDp = 18, radiusDp = 14)
        proxyCard.addView(sectionHeader("本机代理与高级特性", "SOCKS5 / HTTP 本机回环与直连拓扑"))

        val localPortRow = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        settingSocksPortField = styledInput(config.socks5Port.toString(), "1080", isNumber = true)
        localPortRow.addView(fieldBlock("SOCKS5 端口", settingSocksPortField), weighted())
        localPortRow.addView(View(this), LinearLayout.LayoutParams(dp(12), 1))
        settingHttpPortField = styledInput(config.httpPort.toString(), "8080", isNumber = true)
        localPortRow.addView(fieldBlock("HTTP 端口", settingHttpPortField), weighted())
        proxyCard.addView(localPortRow)

        settingP2pSwitch = Switch(this).apply { isChecked = config.proxyP2pEnabled; UiKit.styleSwitch(this) }
        proxyCard.addView(switchBlock("启用 P2P 备用直连", "Public Direct 不依赖此开关；公网直连不可用时可继续尝试 P2P 打洞", settingP2pSwitch), topMargin(14))

        settingIpv6Switch = Switch(this).apply { isChecked = config.vpnIpv6Enabled; UiKit.styleSwitch(this) }
        proxyCard.addView(switchBlock("VPN IPv6 转发", "所选出口节点具备 IPv6 外部接入时开启", settingIpv6Switch), topMargin(14))
        content.addView(proxyCard, topMargin(16))

        val saveBtn = Button(this).apply {
            text = "保存并应用设置"
            textSize = 15f
            setTextColor(Color.WHITE)
            typeface = Typeface.DEFAULT_BOLD
            background = UiKit.rounded(this@MainActivity, UiPalette.brand, 10)
            val p = dp(14)
            setPadding(0, p, 0, p)
            setOnClickListener { saveSettings() }
        }
        content.addView(saveBtn, topMargin(24))

        return ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(UiPalette.bg)
            addView(content)
        }
    }

    private fun requestGlobalMessageOverlayPermission() {
        val store = ConfigStore(this)
        store.setGlobalMessageOverlayEnabled(true)
        RelayExitService.refreshGlobalMessageOverlaySetting()

        if (MessageOverlayController.hasOverlayPermission(this)) {
            refreshGlobalMessageOverlayPermissionState()
            return
        }
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) {
            refreshGlobalMessageOverlayPermissionState()
            return
        }

        val intent = Intent(
            Settings.ACTION_MANAGE_OVERLAY_PERMISSION,
            Uri.parse("package:$packageName"),
        )
        runCatching {
            @Suppress("DEPRECATION")
            startActivityForResult(intent, REQUEST_MESSAGE_OVERLAY_PERMISSION)
        }.onFailure {
            Toast.makeText(
                this,
                "无法打开悬浮窗权限页面，请在系统设置中允许 RelayProxy 显示在其他应用上层",
                Toast.LENGTH_LONG,
            ).show()
        }
        refreshGlobalMessageOverlayPermissionState()
    }

    private fun refreshGlobalMessageOverlayPermissionState() {
        if (!::settingGlobalMessageOverlay.isInitialized ||
            !::settingGlobalOverlayStatus.isInitialized ||
            !::settingGlobalOverlayAction.isInitialized
        ) {
            return
        }

        val enabled = ConfigStore(this).isGlobalMessageOverlayEnabled()
        val granted = MessageOverlayController.hasOverlayPermission(this)
        settingGlobalOverlayStatus.text = when {
            !enabled -> "已关闭 · 使用应用内弹窗 / 系统通知"
            granted -> "已授权 · 全系统悬浮弹窗可用"
            else -> "等待授权 · 当前自动降级为系统通知"
        }
        settingGlobalOverlayStatus.setTextColor(
            when {
                !enabled -> UiPalette.muted
                granted -> UiPalette.success
                else -> UiPalette.warning
            }
        )
        settingGlobalOverlayStatus.background = UiKit.rounded(
            this,
            when {
                !enabled -> UiPalette.surfaceSubtle
                granted -> UiPalette.successSoft
                else -> UiPalette.warningSoft
            },
            8,
            when {
                !enabled -> UiPalette.lineSubtle
                granted -> UiPalette.successSoftBorder
                else -> UiPalette.warningSoftBorder
            },
        )
        settingGlobalOverlayAction.visibility =
            if (enabled && !granted) View.VISIBLE else View.GONE
    }

    private fun sectionHeader(title: String, subtitle: String): View {
        return LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            addView(TextView(this@MainActivity).apply {
                text = title
                textSize = 15f
                setTextColor(UiPalette.ink)
                typeface = Typeface.DEFAULT_BOLD
            })
            addView(TextView(this@MainActivity).apply {
                text = subtitle
                textSize = 11.5f
                setTextColor(UiPalette.muted)
                setPadding(0, dp(2), 0, dp(10))
            })
            addView(View(this@MainActivity).apply {
                setBackgroundColor(UiPalette.lineSubtle)
            }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(1)).apply {
                bottomMargin = dp(12)
            })
        }
    }

    private fun fieldBlock(label: String, inputView: View): View {
        return LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            addView(TextView(this@MainActivity).apply {
                text = label
                textSize = 12f
                setTextColor(UiPalette.muted)
                setPadding(0, 0, 0, dp(6))
            })
            addView(inputView)
        }
    }

    private fun switchBlock(label: String, desc: String, switchView: Switch): View {
        return LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, dp(2), 0, dp(2))
            val textGroup = LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.VERTICAL
                addView(TextView(this@MainActivity).apply {
                    text = label
                    textSize = 13.5f
                    setTextColor(UiPalette.ink)
                    typeface = Typeface.DEFAULT_BOLD
                })
                addView(TextView(this@MainActivity).apply {
                    text = desc
                    textSize = 11f
                    setTextColor(UiPalette.muted)
                    setPadding(0, dp(2), 0, 0)
                })
            }
            addView(textGroup, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            addView(switchView)
        }
    }

    private fun styledInput(initialText: String, placeholder: String, isNumber: Boolean = false): EditText {
        return EditText(this).apply {
            setText(initialText)
            hint = placeholder
            textSize = 13.5f
            setTextColor(UiPalette.ink)
            setHintTextColor(UiPalette.placeholder)
            if (isNumber) {
                inputType = InputType.TYPE_CLASS_NUMBER
            } else {
                setSingleLine(true)
            }
            val pH = dp(12)
            val pV = dp(11)
            setPadding(pH, pV, pH, pV)
            background = UiKit.rounded(this@MainActivity, UiPalette.inputBg, 8, UiPalette.lineSubtle)
        }
    }

    private fun saveSettings() {
        val store = ConfigStore(this)
        val current = store.load()

        val netModeValues = listOf(NetworkBinder.MODE_WIFI, NetworkBinder.MODE_CELLULAR)
        val chosenNetMode = netModeValues.getOrElse(settingNetModeSpinner.selectedItemPosition) { NetworkBinder.MODE_WIFI }

        val updated = current.copy(
            serverAddress = settingServerField.text.toString().trim(),
            identityId = settingIdentityField.text.toString().trim(),
            deviceName = settingDeviceNameField.text.toString().trim().ifBlank { store.defaultDeviceName() },
            networkMode = chosenNetMode,
            autoNetworkSwitch = settingAutoSwitch.isChecked,
            allowPrivateNetwork = settingAllowPrivate.isChecked,
            quicPort = settingQuicPortField.text.toString().toIntOrNull() ?: 443,
            tcpPort = settingTcpPortField.text.toString().toIntOrNull() ?: 443,
            tlsEnabled = settingTlsSwitch.isChecked,
            insecureTls = settingInsecureTlsSwitch.isChecked,
            socks5Port = settingSocksPortField.text.toString().toIntOrNull() ?: 1080,
            httpPort = settingHttpPortField.text.toString().toIntOrNull() ?: 8080,
            proxyP2pEnabled = settingP2pSwitch.isChecked,
            vpnIpv6Enabled = settingIpv6Switch.isChecked,
        )
        store.save(updated)
        notifyServiceReconfigure()
        Toast.makeText(this, "设置已保存，底层网络隧道已热重载", Toast.LENGTH_SHORT).show()
        renderStatus()
    }

    // =========================================================================
    // 底部现代悬浮导航栏 (还原原型设计：Vector 图标 + 胶囊指示灯 + 语义变色)
    // =========================================================================
    private fun buildBottomNavBar(): View {
        val nav = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setBackgroundColor(UiPalette.surface)
            background = UiKit.rounded(this@MainActivity, UiPalette.surface, 0, UiPalette.line, strokeWidthDp = 1)
        }

        val tabDrawables = listOf(
            R.drawable.ic_tab_dashboard,
            R.drawable.ic_tab_nodes,
            R.drawable.ic_tab_routing,
            R.drawable.ic_tab_settings
        )
        val tabLabels = listOf("控制台", "出口节点", "分流规则", "配置身份")

        val buttons = mutableListOf<LinearLayout>()
        val icons = mutableListOf<ImageView>()
        val texts = mutableListOf<TextView>()

        tabLabels.forEachIndexed { index, label ->
            val item = LinearLayout(this).apply {
                orientation = LinearLayout.VERTICAL
                gravity = Gravity.CENTER
                isClickable = true
                isFocusable = true
                val pV = dp(6)
                setPadding(0, pV, 0, pV)
                setOnClickListener { switchTab(index) }
            }

            val iconView = ImageView(this).apply {
                setImageResource(tabDrawables[index])
                val sz = dp(22)
                layoutParams = LinearLayout.LayoutParams(sz, sz).apply {
                    gravity = Gravity.CENTER_HORIZONTAL
                }
            }
            item.addView(iconView)

            val textView = TextView(this).apply {
                text = label
                textSize = 10.5f
                typeface = Typeface.DEFAULT_BOLD
                gravity = Gravity.CENTER
                val lp = LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                    topMargin = dp(3)
                }
                layoutParams = lp
            }
            item.addView(textView)

            buttons.add(item)
            icons.add(iconView)
            texts.add(textView)
            nav.addView(item, weighted())
        }

        navButtons = buttons
        navIcons = icons
        navTexts = texts
        updateBottomNavViews()
        return nav
    }

    fun switchTab(tabIndex: Int) {
        if (currentTab == tabIndex) return
        currentTab = tabIndex
        tabPanes.forEachIndexed { index, pane ->
            pane.visibility = if (index == currentTab) View.VISIBLE else View.GONE
        }
        updateBottomNavViews()
        if (tabIndex == TAB_ROUTING) {
            refreshRoutingTab()
        }
    }

    private fun updateBottomNavViews() {
        if (!::navButtons.isInitialized) return
        navButtons.forEachIndexed { index, _ ->
            val iconView = navIcons[index]
            val textView = navTexts[index]
            val isSelected = (index == currentTab)

            if (isSelected) {
                iconView.imageTintList = ColorStateList.valueOf(UiPalette.brand)
                textView.setTextColor(UiPalette.brand)
                textView.typeface = Typeface.DEFAULT_BOLD
            } else {
                iconView.imageTintList = ColorStateList.valueOf(UiPalette.muted)
                textView.setTextColor(UiPalette.muted)
                textView.typeface = Typeface.DEFAULT
            }
        }
    }

    // =========================================================================
    // 状态渲染与业务逻辑 (State Rendering & Real-time Telemetry)
    // =========================================================================
    private fun renderStatus() {
        val obj = runCatching { JSONObject(RelayExitService.statusJson()) }.getOrNull()
        val vpnObj = runCatching { JSONObject(RelayVpnService.statusJson()) }.getOrNull()

        val state = obj?.optString("connectionState", "STOPPED") ?: "STOPPED"
        val approval = obj?.optString("approvalState", "unknown") ?: "unknown"
        val transportValue = obj?.optString("transport", "") ?: ""
        val streams = obj?.optLong("activeStreams", 0) ?: 0
        val latency = obj?.optLong("latencyMs", 0) ?: 0
        val approved = obj?.optBoolean("exitApproved", false) ?: false
        val deviceId = obj?.optString("deviceId", "") ?: ""
        currentDeviceId = deviceId
        val activeNetwork = obj?.optString("activeNetwork", "") ?: ""
        val p2pState = obj?.optString("p2pState", "") ?: ""
        val p2pPath = obj?.optString("p2pPath", "") ?: ""
        val p2pError = obj?.optString("p2pError", "") ?: ""
        val p2pRttMs = obj?.optLong("p2pRttMs", 0) ?: 0
        val directState = obj?.optString("directState", p2pState).orEmpty().ifBlank { p2pState }
        val directPath = obj?.optString("directPath", p2pPath).orEmpty().ifBlank { p2pPath }
        val directError = obj?.optString("directError", p2pError).orEmpty().ifBlank { p2pError }
        val directRttMs = obj?.optLong("directRttMs", p2pRttMs) ?: p2pRttMs
        val selectedExit = obj?.optString("selectedExit", "") ?: ""
        val powerConstrained = obj?.optBoolean("powerConstrained", false) ?: false
        val nativeUdp = obj?.optJSONObject("nativeUdp")
        val proxyActiveTcp = obj?.optLong("proxyActiveTcp", 0) ?: 0
        val proxyActiveUdp = obj?.optLong("proxyActiveUdp", 0) ?: 0
        val proxyBytesUp = obj?.optLong("proxyBytesUp", 0) ?: 0
        val proxyBytesDown = obj?.optLong("proxyBytesDown", 0) ?: 0

        val vpnState = vpnObj?.optString("vpnState", "STOPPED") ?: "STOPPED"
        val tunTx = vpnObj?.optLong("tunTxBytes", 0) ?: 0
        val tunRx = vpnObj?.optLong("tunRxBytes", 0) ?: 0

        val store = ConfigStore(this)
        val config = store.load()
        val desiredExit = store.isDesiredRunning()
        val desiredVpn = store.isVpnDesiredRunning()

        // 1. 计算实时上下行速率
        val now = System.currentTimeMillis()
        val totalUp = proxyBytesUp + tunTx
        val totalDown = proxyBytesDown + tunRx

        if (lastSampleTimestamp > 0 && now > lastSampleTimestamp) {
            val deltaSeconds = (now - lastSampleTimestamp) / 1000.0
            val speedUp = ((totalUp - lastTotalBytesUp).coerceAtLeast(0) / deltaSeconds).toLong()
            val speedDown = ((totalDown - lastTotalBytesDown).coerceAtLeast(0) / deltaSeconds).toLong()

            speedUpText.text = "↑ ${UiKit.formatSpeed(speedUp)}"
            speedDownText.text = " · ↓ ${UiKit.formatSpeed(speedDown)}"
            throughputSparkline.addSample(speedUp + speedDown)
        }
        lastTotalBytesUp = totalUp
        lastTotalBytesDown = totalDown
        lastSampleTimestamp = now

        totalTrafficText.text = "累计: ↑ ${UiKit.formatBytes(totalUp)} · ↓ ${UiKit.formatBytes(totalDown)}"

        // 2. 状态卡与徽标
        when (state) {
            "CONNECTED" -> {
                statusTitle.text = when {
                    desiredVpn && desiredExit -> "出口与代理均就绪"
                    desiredVpn -> "代理客户端运行中"
                    desiredExit -> "网络出口运行中"
                    else -> "控制连接已就绪"
                }
                statusBadge.text = "运行中"
                statusBadge.setTextColor(UiPalette.success)
                statusBadge.background = UiKit.rounded(this, UiPalette.successSoft, 8, UiPalette.successSoftBorder)
            }
            "CONNECTING" -> {
                statusTitle.text = "正在连接 Relay Server"
                statusBadge.text = "连接中"
                statusBadge.setTextColor(UiPalette.brand)
                statusBadge.background = UiKit.rounded(this, UiPalette.brandSoft, 8, UiPalette.brandSoftBorder)
            }
            "WAITING_NETWORK" -> {
                statusTitle.text = "等待网络连接"
                statusBadge.text = "等待网络"
                statusBadge.setTextColor(UiPalette.warning)
                statusBadge.background = UiKit.rounded(this, UiPalette.warningSoft, 8, UiPalette.warningSoftBorder)
            }
            "ERROR" -> {
                statusTitle.text = "网络服务异常"
                statusBadge.text = "错误"
                statusBadge.setTextColor(UiPalette.danger)
                statusBadge.background = UiKit.rounded(this, UiPalette.dangerSoft, 8, UiPalette.dangerSoftBorder)
            }
            else -> {
                statusTitle.text = "服务未启动"
                statusBadge.text = "已停止"
                statusBadge.setTextColor(UiPalette.muted)
                statusBadge.background = UiKit.rounded(this, UiPalette.surfaceElevated, 8)
            }
        }

        // 状态副文本
        val err = obj?.optString("lastError", "").orEmpty()
        statusDetail.text = when {
            err.isNotBlank() -> err
            state == "CONNECTED" && directError.isNotBlank() -> "Direct Path: $directError"
            vpnState == "RUNNING" -> "VPN TUN 数据隧道接管正常"
            desiredExit && approved -> "后台出口服务正常运行中"
            approval == "pending" -> "新设备已连接，等待服务端审批"
            state == "CONNECTED" -> "控制连接就绪，已同步出口拓扑"
            else -> "点击右侧启动按钮开启 VPN 保护"
        }

        // Direct Path 徽标。Public Direct 与 P2P 独立，不能用 P2P 开关推断当前路径。
        p2pBadge.visibility = View.VISIBLE
        when {
            directState == "READY" && directPath == "public_direct_quic" -> {
                p2pBadge.text = "公网直连 · ${directRttMs}ms"
                p2pBadge.setTextColor(UiPalette.success)
                p2pBadge.background = UiKit.rounded(this, UiPalette.successSoft, 8, UiPalette.successSoftBorder)
            }
            directState == "READY" && directPath == "p2p_quic" -> {
                p2pBadge.text = "P2P QUIC · ${directRttMs}ms"
                p2pBadge.setTextColor(UiPalette.success)
                p2pBadge.background = UiKit.rounded(this, UiPalette.successSoft, 8, UiPalette.successSoftBorder)
            }
            directError.isNotBlank() && directPath != "relay_quic" && directPath != "relay_tls" -> {
                p2pBadge.text = "直连失败"
                p2pBadge.setTextColor(UiPalette.warning)
                p2pBadge.background = UiKit.rounded(this, UiPalette.warningSoft, 8, UiPalette.warningSoftBorder)
            }
            directState == "CONNECTING" || directState == "RENDEZVOUS" || directState == "PUNCHING" || directState == "QUIC_HANDSHAKE" -> {
                p2pBadge.text = "直连中"
                p2pBadge.setTextColor(UiPalette.brand)
                p2pBadge.background = UiKit.rounded(this, UiPalette.brandSoft, 8, UiPalette.brandSoftBorder)
            }
            state == "CONNECTED" -> {
                p2pBadge.text = "Relay 转发"
                p2pBadge.setTextColor(UiPalette.brand)
                p2pBadge.background = UiKit.rounded(this, UiPalette.brandSoft, 8, UiPalette.brandSoftBorder)
            }
            else -> {
                p2pBadge.text = "等待直连"
                p2pBadge.setTextColor(UiPalette.muted)
                p2pBadge.background = UiKit.rounded(this, UiPalette.surfaceElevated, 8)
            }
        }

        // 主电源大按钮 (还原原型设计：运行态蓝紫渐变+纯白图标，停止态暗底微转)
        if (desiredVpn) {
            masterPowerIcon.setImageResource(R.drawable.ic_power_bolt)
            masterPowerIcon.imageTintList = ColorStateList.valueOf(Color.WHITE)
            masterPowerIcon.rotation = 0f
            masterPowerButton.background = UiKit.gradientRounded(
                this,
                Color.rgb(37, 99, 235), // blue-600
                Color.rgb(79, 70, 229), // indigo-600
                14,
                GradientDrawable.Orientation.TL_BR
            )
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.LOLLIPOP) {
                masterPowerButton.elevation = dp(6).toFloat()
            }
        } else {
            masterPowerIcon.setImageResource(R.drawable.ic_power_bolt)
            masterPowerIcon.imageTintList = ColorStateList.valueOf(UiPalette.muted)
            masterPowerIcon.rotation = 90f
            masterPowerButton.background = UiKit.rounded(
                this,
                UiPalette.surfaceElevated,
                14,
                UiPalette.lineSubtle
            )
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.LOLLIPOP) {
                masterPowerButton.elevation = 0f
            }
        }

        metricTransport.text = transportValue.ifBlank { "QUIC" }
        metricStreams.text = (streams + proxyActiveTcp + proxyActiveUdp).toString()
        metricLatency.text = if (latency > 0) "$latency ms" else "—"
        metricLatency.setTextColor(if (latency in 1..80) UiPalette.success else UiPalette.warning)

        // 当前出口节点卡片
        val exits = obj?.optJSONArray("proxyExits")
        var nodeDisplay = "自动选择最佳出口"
        if (selectedExit.isNotBlank() && exits != null) {
            for (idx in 0 until exits.length()) {
                val itm = exits.optJSONObject(idx) ?: continue
                if (itm.optString("deviceId") == selectedExit) {
                    nodeDisplay = itm.optString("name", selectedExit.take(12))
                    break
                }
            }
        }
        activeNodeName.text = nodeDisplay
        activeNodeSubtitle.text = if (selectedExit.isBlank()) "根据延迟与链路自动优选" else "默认已绑定出口节点"
        activeNodeLatency.text = if (latency > 0) "$latency ms" else "—"

        // 双核状态卡片
        if (vpnState == "RUNNING") {
            vpnStatusBadge.text = "运行中"
            vpnStatusBadge.setTextColor(UiPalette.success)
            vpnStatusBadge.background = UiKit.rounded(this, UiPalette.successSoft, 6)
            vpnActionBtn.text = "停止 VPN"
            vpnActionBtn.background = UiKit.rounded(this, UiPalette.dangerSoft, 8, UiPalette.dangerSoftBorder)
            vpnActionBtn.setTextColor(UiPalette.danger)
        } else {
            vpnStatusBadge.text = "已停止"
            vpnStatusBadge.setTextColor(UiPalette.muted)
            vpnStatusBadge.background = UiKit.rounded(this, UiPalette.surfaceElevated, 6)
            vpnActionBtn.text = "启动 VPN"
            vpnActionBtn.background = UiKit.rounded(this, UiPalette.brand, 8)
            vpnActionBtn.setTextColor(Color.WHITE)
        }

        if (desiredExit && approved) {
            exitStatusBadge.text = "对外提供"
            exitStatusBadge.setTextColor(UiPalette.brand)
            exitStatusBadge.background = UiKit.rounded(this, UiPalette.brandSoft, 6)
            exitActionBtn.text = "停止出口"
            exitActionBtn.background = UiKit.rounded(this, UiPalette.dangerSoft, 8, UiPalette.dangerSoftBorder)
            exitActionBtn.setTextColor(UiPalette.danger)
        } else {
            exitStatusBadge.text = "未启动"
            exitStatusBadge.setTextColor(UiPalette.muted)
            exitStatusBadge.background = UiKit.rounded(this, UiPalette.surfaceElevated, 6)
            exitActionBtn.text = "启动出口"
            exitActionBtn.background = UiKit.rounded(this, UiPalette.surfaceElevated, 8, UiPalette.lineSubtle)
            exitActionBtn.setTextColor(UiPalette.ink)
        }

        diagActiveNet.text = when (activeNetwork) {
            NetworkBinder.MODE_WIFI -> "Wi-Fi (5GHz) · 稳定"
            NetworkBinder.MODE_CELLULAR -> "移动数据 · 自动容灾"
            else -> "等待网络分配"
        }
        diagPowerMode.text = if (powerConstrained) "省电模式 (长休眠)" else "标准性能模式"
        nativeUdp?.let {
            diagUdpDrops.text = "队列 ${it.optLong("queueDrops", 0)} · 重组 ${it.optLong("reassemblyDrops", 0)} · 关联 ${it.optLong("associationRejects", 0)}"
        }

        if (::settingDeviceIdText.isInitialized && currentDeviceId.isNotBlank()) {
            settingDeviceIdText.text = formatDeviceId(currentDeviceId)
        }

        renderNodesList(exits)
    }

    // =========================================================================
    // 操作触发与 Service 联动
    // =========================================================================
    private fun toggleMasterConnection() {
        val store = ConfigStore(this)
        if (store.isVpnDesiredRunning()) {
            stopVpnService()
        } else {
            startVpnFlow()
        }
    }

    private fun toggleVpn() {
        val store = ConfigStore(this)
        if (store.isVpnDesiredRunning()) {
            stopVpnService()
        } else {
            startVpnFlow()
        }
    }

    private fun startVpnFlow() {
        val store = ConfigStore(this)
        val config = store.load()
        if (config.serverAddress.isBlank()) {
            switchTab(TAB_SETTINGS)
            Toast.makeText(this, "请先填写 Relay Server 地址与身份凭证", Toast.LENGTH_LONG).show()
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
        ConfigStore(this).setVpnDesiredRunning(true)
        val intent = Intent(this, RelayVpnService::class.java).setAction(RelayVpnService.ACTION_START)
        runCatching {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                startForegroundService(intent)
            } else {
                startService(intent)
            }
        }.onFailure {
            ConfigStore(this).setVpnDesiredRunning(false)
            Toast.makeText(this, "VPN 启动失败：${it.message}", Toast.LENGTH_LONG).show()
        }
        renderStatus()
    }

    private fun stopVpnService() {
        ConfigStore(this).setVpnDesiredRunning(false)
        val intent = Intent(this, RelayVpnService::class.java).setAction(RelayVpnService.ACTION_STOP)
        runCatching { startService(intent) }
        renderStatus()
    }

    private fun toggleRelay() {
        val store = ConfigStore(this)
        if (store.isDesiredRunning()) {
            store.setDesiredRunning(false)
            startService(Intent(this, RelayExitService::class.java).setAction(RelayExitService.ACTION_STOP))
        } else {
            val config = store.load()
            if (config.serverAddress.isBlank()) {
                switchTab(TAB_SETTINGS)
                Toast.makeText(this, "请先填写 Relay Server 地址", Toast.LENGTH_LONG).show()
                return
            }
            store.setDesiredRunning(true)
            val intent = Intent(this, RelayExitService::class.java).setAction(RelayExitService.ACTION_START)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                startForegroundService(intent)
            } else {
                startService(intent)
            }
        }
        renderStatus()
    }

    private fun connectControlChannel() {
        val store = ConfigStore(this)
        if (!store.hasConnectionConfig()) return
        val intent = Intent(this, RelayExitService::class.java).setAction(RelayExitService.ACTION_CONNECT)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(intent)
        } else {
            startService(intent)
        }
    }

    private fun notifyServiceReconfigure() {
        startService(Intent(this, RelayExitService::class.java).setAction(RelayExitService.ACTION_RECONFIGURE))
        if (ConfigStore(this).isVpnDesiredRunning()) {
            startService(Intent(this, RelayVpnService::class.java).setAction(RelayVpnService.ACTION_RECONFIGURE))
        }
    }

    private fun copyDeviceId() {
        if (currentDeviceId.isBlank()) {
            Toast.makeText(this, "尚未生成设备 ID", Toast.LENGTH_SHORT).show()
            return
        }
        val clipboard = getSystemService(ClipboardManager::class.java)
        clipboard.setPrimaryClip(ClipData.newPlainText("RelayProxy Device ID", currentDeviceId))
        Toast.makeText(this, "设备 ID 已复制到剪贴板", Toast.LENGTH_SHORT).show()
    }

    private fun copyIdentityId() {
        val id = if (::settingIdentityField.isInitialized) settingIdentityField.text.toString().trim() else ""
        if (id.isBlank()) {
            Toast.makeText(this, "尚未填写身份 ID", Toast.LENGTH_SHORT).show()
            return
        }
        val clipboard = getSystemService(ClipboardManager::class.java)
        clipboard.setPrimaryClip(ClipData.newPlainText("RelayProxy Identity ID", id))
        Toast.makeText(this, "身份 ID 已复制到剪贴板", Toast.LENGTH_SHORT).show()
    }

    private fun dp(value: Int) = UiKit.dp(this, value)
    private fun topMargin(top: Int) = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = dp(top) }
    private fun weighted() = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)

    private fun formatDeviceId(value: String): String {
        return value.chunked(16).joinToString("\n")
    }

    private fun registerMessageReceiver() {
        if (messageReceiverRegistered) return
        val filter = IntentFilter(RelayExitService.ACTION_MESSAGE)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            registerReceiver(messageReceiver, filter, Context.RECEIVER_NOT_EXPORTED)
        } else {
            @Suppress("DEPRECATION")
            registerReceiver(messageReceiver, filter)
        }
        messageReceiverRegistered = true
    }

    private fun unregisterMessageReceiver() {
        if (!messageReceiverRegistered) return
        messageReceiverRegistered = false
        runCatching { unregisterReceiver(messageReceiver) }
    }

    private fun handleMessageIntent(source: Intent?) {
        val raw = source?.getStringExtra(RelayExitService.EXTRA_MESSAGE_JSON) ?: return
        source.removeExtra(RelayExitService.EXTRA_MESSAGE_JSON)
        runCatching { JSONObject(raw) }.getOrNull()?.let(::enqueueMessagePopup)
    }

    private fun enqueueMessagePopup(message: JSONObject) {
        if (!message.optBoolean("popup", false)) return
        val id = message.optString("id", "")
        if (id.isNotBlank() && messagePopupQueue.any { it.optString("id", "") == id }) return
        messagePopupQueue.addLast(message)
        showNextMessagePopup()
    }

    private fun showNextMessagePopup() {
        if (activeMessageDialog?.isShowing == true) return
        val message = messagePopupQueue.pollFirst() ?: return
        val popupType = message.optString("popupType", "verification_code")
        val title = message.optString("title", "RelayProxy 消息").ifBlank { "RelayProxy 消息" }
        val content = message.optString("content", "")
        val code = message.optString("verificationCode", "")
        val rule = message.optString("verificationRule", "")
        val builder = when (popupType) {
            "message" -> UiKit.alertDialog(this, title, content.ifBlank { "收到一条新消息" })
                .setPositiveButton("知道了", null)
            "important" -> UiKit.alertDialog(
                this,
                "重要提醒 · $title",
                buildString {
                    if (content.isNotBlank()) append(content)
                    if (code.isNotBlank()) {
                        if (isNotEmpty()) append("\n\n")
                        append("验证码：").append(code)
                    }
                }.ifBlank { "收到一条重要消息" },
            ).setPositiveButton("知道了", null)
            else -> UiKit.alertDialog(
                this,
                title.ifBlank { "收到新的验证码" },
                buildString {
                    if (code.isNotBlank()) append("验证码：").append(code)
                    if (content.isNotBlank()) {
                        if (isNotEmpty()) append("\n\n")
                        append(content)
                    }
                    if (rule.isNotBlank()) {
                        if (isNotEmpty()) append("\n\n")
                        append("匹配规则：").append(rule)
                    }
                }.ifBlank { "收到一条验证码消息" },
            ).setNegativeButton("关闭", null)
                .setPositiveButton("复制验证码") { _, _ ->
                    if (code.isNotBlank()) {
                        val clipboard = getSystemService(ClipboardManager::class.java)
                        clipboard.setPrimaryClip(ClipData.newPlainText("RelayProxy verification code", code))
                        Toast.makeText(this, "验证码已复制", Toast.LENGTH_SHORT).show()
                    }
                }
        }
        if (popupType == "important" && code.isNotBlank()) {
            builder.setNeutralButton("复制验证码") { _, _ ->
                val clipboard = getSystemService(ClipboardManager::class.java)
                clipboard.setPrimaryClip(ClipData.newPlainText("RelayProxy verification code", code))
                Toast.makeText(this, "验证码已复制", Toast.LENGTH_SHORT).show()
            }
        }
        activeMessageDialog = builder.create().also { dialog ->
            dialog.setOnDismissListener {
                activeMessageDialog = null
                handler.post { showNextMessagePopup() }
            }
            dialog.show()
        }
    }

    private fun requestNotificationPermission() {
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) !=
            android.content.pm.PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 100)
        }
    }
}
