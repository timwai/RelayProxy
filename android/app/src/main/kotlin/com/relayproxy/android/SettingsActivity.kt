package com.relayproxy.android

import android.app.Activity
import android.content.Intent
import android.content.res.ColorStateList
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Bundle
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.ArrayAdapter
import android.widget.AdapterView
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.Spinner
import android.widget.Switch
import android.widget.TextView
import android.widget.Toast

class SettingsActivity : Activity() {
    private lateinit var server: EditText
    private lateinit var identityId: EditText
    private lateinit var deviceName: EditText
    private lateinit var quicPort: EditText
    private lateinit var tcpPort: EditText
    private lateinit var transport: Spinner
    private lateinit var tlsEnabled: Switch
    private lateinit var insecureTls: Switch
    private lateinit var allowPrivate: Switch
    private lateinit var autoNetworkSwitch: Switch
    private lateinit var socks5Enabled: Switch
    private lateinit var httpEnabled: Switch
    private lateinit var proxyP2pEnabled: Switch
    private lateinit var proxyPathMode: Spinner
    private lateinit var defaultExitId: EditText
    private lateinit var exitSelection: Spinner
    private lateinit var socks5Port: EditText
    private lateinit var httpPort: EditText
    private lateinit var vpnAppMode: Spinner
    private lateinit var vpnAppsSummary: TextView
    private lateinit var vpnIpv6Enabled: Switch
    private val selectedVpnPackages = linkedSetOf<String>()
    private val networkModeTabs = mutableListOf<TextView>()
    private var selectedNetworkModeIndex = 0

    private val transportValues = listOf("auto", "quic_only", "tcp_only")
    private val transportLabels = listOf("自动选择", "仅 QUIC", "仅 TCP/TLS")
    private val proxyPathModeValues = listOf(
        ExitConfig.PROXY_PATH_AUTO,
        ExitConfig.PROXY_PATH_DIRECT_ONLY,
        ExitConfig.PROXY_PATH_P2P_ONLY,
        ExitConfig.PROXY_PATH_RELAY_ONLY,
    )
    private val proxyPathModeLabels = listOf(
        "自动 · Public Direct → P2P → Relay",
        "仅直连 · Public Direct → P2P → 失败",
        "仅 P2P · 不使用 Public Direct / Relay",
        "仅 Relay · 不尝试任何直连",
    )
    private val networkModeValues = listOf(
        NetworkBinder.MODE_WIFI,
        NetworkBinder.MODE_CELLULAR,
    )
    private val networkModeLabels = listOf("Wi-Fi 优先", "移动数据优先")
    private val vpnAppModeValues = listOf(
        ExitConfig.VPN_APP_MODE_ALL,
        ExitConfig.VPN_APP_MODE_INCLUDE,
        ExitConfig.VPN_APP_MODE_EXCLUDE,
    )
    private val vpnAppModeLabels = listOf("全部应用", "仅选中应用", "排除选中应用")

    companion object {
        private const val REQUEST_VPN_APPS = 2401
        const val EXTRA_SECTION = "section"
        const val SECTION_CONNECTION = "connection"
        const val SECTION_EXIT = "exit"
        const val SECTION_PROXY = "proxy"
        const val SECTION_VPN = "vpn"
        const val SECTION_NETWORK = "network"
    }

    private val section: String by lazy {
        intent.getStringExtra(EXTRA_SECTION) ?: SECTION_CONNECTION
    }

    private val bg get() = UiPalette.bg
    private val surface get() = UiPalette.surface
    private val surfaceSubtle get() = UiPalette.surfaceSubtle
    private val inputBg get() = UiPalette.inputBg
    private val ink get() = UiPalette.ink
    private val muted get() = UiPalette.muted
    private val line get() = UiPalette.line
    private val brand get() = UiPalette.brand

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        UiPalette.sync(this)
        setTheme(if (UiPalette.isDark) R.style.Theme_RelayProxy_Dark else R.style.Theme_RelayProxy_Light)
        configureWindow()
        setContentView(buildUi())
        loadConfig()
    }

    @Deprecated("Deprecated Android activity result API retained for API 26 compatibility")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode != REQUEST_VPN_APPS || resultCode != RESULT_OK) return
        selectedVpnPackages.clear()
        selectedVpnPackages += data?.getStringArrayListExtra(VpnAppSelectionActivity.EXTRA_SELECTED)
            .orEmpty()
            .filter { it != packageName }
        updateVpnAppsSummary()
    }

    private fun configureWindow() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            window.setDecorFitsSystemWindows(true)
        }
        window.statusBarColor = bg
        window.navigationBarColor = bg
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            var flags = window.decorView.systemUiVisibility
            flags = if (!UiPalette.isDark) {
                flags or View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR
            } else {
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

    private fun buildUi(): View {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(20), dp(30), dp(20), dp(36))
            setBackgroundColor(bg)
        }

        val connection = card()
        addSectionHeader(connection, "连接设置", "配置 Relay Server 和传输参数。")
        server = styledField("relay.example.com")
        connection.addView(labeled("Relay Server", server), topMargin(16))
        identityId = styledField("a1b2c3d4e5f6g7h8")
        connection.addView(labeled("身份 ID", identityId), topMargin(12))
        deviceName = styledField(ConfigStore(this).defaultDeviceName())
        connection.addView(labeled("设备名称", deviceName), topMargin(12))

        transport = Spinner(this).apply {
            adapter = UiKit.themedSpinnerAdapter(this@SettingsActivity, transportLabels)
            background = rounded(inputBg, 13, line)
            setPadding(dp(12), 0, dp(10), 0)
            minimumHeight = dp(50)
        }
        connection.addView(labeled("传输方式", transport), topMargin(12))

        quicPort = numberField("443")
        tcpPort = numberField("443")
        val ports = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            addView(
                labeled("QUIC 端口", quicPort),
                LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
            )
            addView(View(this@SettingsActivity), LinearLayout.LayoutParams(dp(10), 1))
            addView(
                labeled("TCP / TLS 端口", tcpPort),
                LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
            )
        }
        connection.addView(ports, topMargin(12))
        addSectionCard(root, SECTION_CONNECTION, connection)

        val policy = card()
        addSectionHeader(policy, "出口策略", "选择首选出口网络，并配置自动故障切换。")
        tlsEnabled = Switch(this)
        insecureTls = Switch(this)
        allowPrivate = Switch(this)
        autoNetworkSwitch = Switch(this)

        connection.addView(switchRow("启用 TLS", "推荐开启。", tlsEnabled), topMargin(14))
        connection.addView(divider(), topMargin(10))
        connection.addView(switchRow("允许自签名证书", "仅用于可信的自建服务端。", insecureTls), topMargin(10))

        policy.addView(labeled("首选出口网络", buildNetworkModeTabs()), topMargin(12))
        policy.addView(divider(), topMargin(10))
        policy.addView(
            switchRow(
                "自动切换网络",
                "首选网络无互联网时切换到备用网络，恢复后自动切回。",
                autoNetworkSwitch,
            ),
            topMargin(10),
        )
        policy.addView(divider(), topMargin(10))
        policy.addView(switchRow("允许访问出口侧私网", "开启后可访问手机所在局域网。", allowPrivate), topMargin(10))
        addSectionCard(root, SECTION_NETWORK, policy)

        val client = card()
        addSectionHeader(client, "代理客户端", "本机代理只监听回环地址；使用服务端授权的 Relay 出口。")
        socks5Enabled = Switch(this)
        httpEnabled = Switch(this)
        proxyP2pEnabled = Switch(this)
        proxyPathMode = Spinner(this).apply {
            adapter = UiKit.themedSpinnerAdapter(this@SettingsActivity, proxyPathModeLabels)
            background = rounded(inputBg, 13, line)
            setPadding(dp(12), 0, dp(10), 0)
            minimumHeight = dp(50)
        }
        client.addView(labeled("路径模式", proxyPathMode), topMargin(14))
        client.addView(divider(), topMargin(10))
        client.addView(
            switchRow(
                "SOCKS5 代理",
                "独立启用 SOCKS5；同时提供 TCP 与 UDP 转发。关闭 SOCKS5 和 HTTP 即关闭本机代理。",
                socks5Enabled,
            ),
            topMargin(14),
        )
        client.addView(divider(), topMargin(10))
        client.addView(
            switchRow(
                "HTTP / HTTPS 代理",
                "独立启用 HTTP 代理；支持 HTTP 请求与 HTTPS CONNECT。",
                httpEnabled,
            ),
            topMargin(10),
        )
        client.addView(divider(), topMargin(10))
        client.addView(
            switchRow(
                "启用 P2P 备用直连",
                "Public Direct 不依赖此开关；自动/仅直连模式下公网直连不可用时继续尝试 P2P。",
                proxyP2pEnabled,
            ),
            topMargin(10),
        )

        socks5Port = numberField("1080")
        httpPort = numberField("8080")
        val proxyPorts = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            addView(
                labeled("SOCKS5 端口", socks5Port),
                LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f),
            )
            addView(View(this@SettingsActivity), LinearLayout.LayoutParams(dp(10), 1))
            addView(
                labeled("HTTP 端口", httpPort),
                LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f),
            )
        }
        client.addView(proxyPorts, topMargin(12))
        addSectionCard(root, SECTION_PROXY, client)

        val exit = card()
        addSectionHeader(exit, "出口选择", "从当前身份可用及跨身份授权的出口中选择默认设备。")
        defaultExitId = styledField("留空由服务端选择")
        exitSelection = Spinner(this).apply {
            background = rounded(inputBg, 13, line)
            setPadding(dp(12), 0, dp(10), 0)
            minimumHeight = dp(50)
        }
        exit.addView(labeled("已授权出口", exitSelection), topMargin(14))
        exit.addView(labeled("首选出口设备 ID", defaultExitId), topMargin(12))
        addSectionCard(root, SECTION_EXIT, exit)

        val vpn = card()
        addSectionHeader(
            vpn,
            "VPN 范围与 DNS",
            "应用范围只决定哪些流量进入 VPN；代理、直连或拒绝仍由分流规则决定。DNS 使用 Mapped DNS 并交给所选出口解析。",
        )
        vpnAppMode = Spinner(this).apply {
            adapter = UiKit.themedSpinnerAdapter(this@SettingsActivity, vpnAppModeLabels)
            background = rounded(inputBg, 13, line)
            setPadding(dp(12), 0, dp(10), 0)
            minimumHeight = dp(50)
        }
        vpn.addView(labeled("应用范围", vpnAppMode), topMargin(14))
        vpnAppsSummary = TextView(this).apply {
            textSize = 12.5f
            setTextColor(ink)
            setPadding(dp(12), dp(12), dp(12), dp(12))
            background = rounded(inputBg, 13, line)
            setOnClickListener { openVpnAppSelection() }
        }
        vpn.addView(labeled("选择应用（点击编辑）", vpnAppsSummary), topMargin(12))
        vpnIpv6Enabled = Switch(this)
        vpn.addView(
            switchRow(
                "启用 IPv6 转发",
                "默认关闭并由 Android 阻断 IPv6，避免出口不支持 IPv6 时 Telegram 等直连 IP 应用持续选择不可达地址。确认所选出口具备 IPv6 后再开启。",
                vpnIpv6Enabled,
            ),
            topMargin(12),
        )
        vpn.addView(
            TextView(this).apply {
                text = "Mapped DNS 已启用\n系统 DNS 查询会在 VPN 内转换为域名，再由所选出口解析。"
                textSize = 12.5f
                setTextColor(ink)
                setPadding(dp(12), dp(12), dp(12), dp(12))
                background = rounded(inputBg, 13, line)
            },
            topMargin(12),
        )
        addSectionCard(root, SECTION_VPN, vpn)

        val runningStore = ConfigStore(this)
        if (runningStore.isDesiredRunning() || runningStore.isVpnDesiredRunning() || runningStore.load().clientEnabled) {
            root.addView(TextView(this).apply {
                text = "服务正在后台运行。保存后会自动重建连接和 VPN，使新配置生效。"
                textSize = 12f
                setTextColor(muted)
                gravity = Gravity.CENTER
                setPadding(dp(8), dp(12), dp(8), 0)
            })
        }

        val scroll = ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(bg)
            addView(root)
        }
        return LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(bg)
            addView(
                scroll,
                LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f),
            )
            addView(fixedSaveBar("保存设置") { saveAndClose() })
        }
    }

    private fun fixedSaveBar(label: String, onSave: () -> Unit) = LinearLayout(this).apply {
        setPadding(dp(20), dp(10), dp(20), dp(16))
        setBackgroundColor(bg)
        addView(Button(this@SettingsActivity).apply {
            text = label
            textSize = 15f
            setTextColor(Color.WHITE)
            setTypeface(typeface, Typeface.BOLD)
            setAllCaps(false)
            background = rounded(brand, 8)
            setOnClickListener { onSave() }
        }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(52)))
    }

    private fun saveAndClose() {
        val store = ConfigStore(this)
        val current = store.load()
        val config = when (section) {
            SECTION_CONNECTION -> current.copy(
                serverAddress = server.text.toString().trim(),
                identityId = identityId.text.toString().trim(),
                deviceName = deviceName.text.toString().trim().ifBlank { store.defaultDeviceName() },
                quicPort = quicPort.text.toString().toIntOrNull() ?: 443,
                tcpPort = tcpPort.text.toString().toIntOrNull() ?: 443,
                transportMode = transportValues.getOrElse(transport.selectedItemPosition) { "auto" },
                tlsEnabled = tlsEnabled.isChecked,
                insecureTls = insecureTls.isChecked,
            )
            SECTION_NETWORK -> current.copy(
                allowPrivateNetwork = allowPrivate.isChecked,
                networkMode = networkModeValues.getOrElse(selectedNetworkModeIndex) {
                    NetworkBinder.MODE_WIFI
                },
                autoNetworkSwitch = autoNetworkSwitch.isChecked,
            )
            SECTION_PROXY -> current.copy(
                clientEnabled = socks5Enabled.isChecked || httpEnabled.isChecked,
                socks5Enabled = socks5Enabled.isChecked,
                httpEnabled = httpEnabled.isChecked,
                proxyP2pEnabled = proxyP2pEnabled.isChecked,
                proxyPathMode = proxyPathModeValues.getOrElse(proxyPathMode.selectedItemPosition) {
                    ExitConfig.PROXY_PATH_AUTO
                },
                socks5Port = socks5Port.text.toString().toIntOrNull() ?: 1080,
                httpPort = httpPort.text.toString().toIntOrNull() ?: 8080,
            )
            SECTION_EXIT -> current.copy(defaultExitId = defaultExitId.text.toString().trim())
            SECTION_VPN -> current.copy(
                vpnAppMode = vpnAppModeValues.getOrElse(vpnAppMode.selectedItemPosition) {
                    ExitConfig.VPN_APP_MODE_ALL
                },
                vpnPackages = selectedVpnPackages.toSet(),
                vpnIpv6Enabled = vpnIpv6Enabled.isChecked,
            )
            else -> current
        }
        if (section == SECTION_CONNECTION && config.serverAddress.isBlank()) {
            server.error = "必须填写 Server 地址"
            server.requestFocus()
            return
        }
        if (section == SECTION_CONNECTION && !Regex("^(?=.*[a-z])(?=.*[0-9])[a-z0-9]{16}$").matches(config.identityId)) {
            identityId.error = "身份 ID 必须是服务端生成的 16 位小写字母数字组合"
            identityId.requestFocus()
            return
        }
        if (section == SECTION_PROXY && ((config.socks5Enabled && config.socks5Port !in 1..65535) ||
            (config.httpEnabled && config.httpPort !in 1..65535)
        )) {
            Toast.makeText(this, "代理端口必须在 1 到 65535 之间", Toast.LENGTH_SHORT).show()
            return
        }
        if (section == SECTION_PROXY && config.socks5Enabled && config.httpEnabled &&
            config.socks5Port == config.httpPort
        ) {
            Toast.makeText(this, "SOCKS5 与 HTTP 不能使用相同端口", Toast.LENGTH_SHORT).show()
            return
        }
        if (section == SECTION_PROXY &&
            config.proxyPathMode == ExitConfig.PROXY_PATH_P2P_ONLY &&
            !config.proxyP2pEnabled
        ) {
            Toast.makeText(this, "仅 P2P 模式需要启用 P2P 备用直连", Toast.LENGTH_SHORT).show()
            return
        }
        if (section == SECTION_VPN && config.vpnAppMode == ExitConfig.VPN_APP_MODE_INCLUDE && config.vpnPackages.isEmpty()) {
            Toast.makeText(this, "仅选中应用模式至少需要选择一个应用", Toast.LENGTH_SHORT).show()
            return
        }
        val coreWasDesired = store.isDesiredRunning() ||
            store.isVpnDesiredRunning() || current.clientEnabled
        store.save(config)
        applyRunningConfiguration(store, config, coreWasDesired)
        Toast.makeText(this, "设置已保存", Toast.LENGTH_SHORT).show()
        finish()
    }

    private fun addSectionCard(root: LinearLayout, cardSection: String, view: View) {
        if (section == cardSection) root.addView(view, topMargin(14))
    }

    private fun loadConfig() {
        val cfg = ConfigStore(this).load()
        server.setText(cfg.serverAddress)
        identityId.setText(cfg.identityId)
        deviceName.setText(cfg.deviceName)
        quicPort.setText(cfg.quicPort.toString())
        tcpPort.setText(cfg.tcpPort.toString())
        transport.setSelection(transportValues.indexOf(cfg.transportMode).coerceAtLeast(0))
        tlsEnabled.isChecked = cfg.tlsEnabled
        insecureTls.isChecked = cfg.insecureTls
        allowPrivate.isChecked = cfg.allowPrivateNetwork
        autoNetworkSwitch.isChecked = cfg.autoNetworkSwitch
        socks5Enabled.isChecked = cfg.clientEnabled && cfg.socks5Enabled
        httpEnabled.isChecked = cfg.clientEnabled && cfg.httpEnabled
        proxyP2pEnabled.isChecked = cfg.proxyP2pEnabled
        proxyPathMode.setSelection(proxyPathModeValues.indexOf(cfg.proxyPathMode).coerceAtLeast(0))
        defaultExitId.setText(cfg.defaultExitId)
        socks5Port.setText(cfg.socks5Port.toString())
        httpPort.setText(cfg.httpPort.toString())
        vpnAppMode.setSelection(vpnAppModeValues.indexOf(cfg.vpnAppMode).coerceAtLeast(0))
        vpnIpv6Enabled.isChecked = cfg.vpnIpv6Enabled
        selectedVpnPackages.clear()
        selectedVpnPackages += cfg.vpnPackages
        updateVpnAppsSummary()
        populateProxyExits(cfg.defaultExitId)
        selectNetworkMode(networkModeValues.indexOf(cfg.networkMode).coerceAtLeast(0))
    }

    private fun openVpnAppSelection() {
        val intent = Intent(this, VpnAppSelectionActivity::class.java)
            .putStringArrayListExtra(
                VpnAppSelectionActivity.EXTRA_SELECTED,
                ArrayList(selectedVpnPackages),
            )
        @Suppress("DEPRECATION")
        startActivityForResult(intent, REQUEST_VPN_APPS)
    }

    private fun updateVpnAppsSummary() {
        vpnAppsSummary.text = if (selectedVpnPackages.isEmpty()) {
            "尚未选择应用"
        } else {
            "已选择 ${selectedVpnPackages.size} 个应用\n" +
                selectedVpnPackages.sorted().take(3).joinToString("\n") +
                if (selectedVpnPackages.size > 3) "\n…" else ""
        }
    }

    private fun applyRunningConfiguration(
        store: ConfigStore,
        config: ExitConfig,
        coreWasDesired: Boolean,
    ) {
        if (store.hasConnectionConfig(config)) {
            val relay = Intent(this, RelayExitService::class.java)
                .setAction(
                    if (coreWasDesired || store.isDesiredRunning() || store.isVpnDesiredRunning() || config.clientEnabled) {
                        RelayExitService.ACTION_RECONFIGURE
                    } else {
                        RelayExitService.ACTION_CONNECT
                    }
                )
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) startForegroundService(relay)
            else startService(relay)
        }
        if (store.isVpnDesiredRunning() && section != SECTION_EXIT) {
            val vpn = Intent(this, RelayVpnService::class.java)
                .setAction(RelayVpnService.ACTION_RECONFIGURE)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) startForegroundService(vpn)
            else startService(vpn)
        }
    }

    private fun populateProxyExits(selectedExitId: String) {
        val exitIds = mutableListOf("")
        val labels = mutableListOf("自动选择（仅一个可用出口时）")
        val status = runCatching { org.json.JSONObject(RelayExitService.statusJson()) }.getOrNull()
        val available = status?.optJSONArray("proxyExits")
        if (available != null) {
            for (index in 0 until available.length()) {
                val exit = available.optJSONObject(index) ?: continue
                val id = exit.optString("deviceId").trim()
                if (id.isBlank() || id in exitIds) continue
                val name = exit.optString("name").ifBlank { id }
                val online = exit.optBoolean("online", false)
                exitIds += id
                labels += "$name · ${if (online) "在线" else "离线"}"
            }
        }
        if (selectedExitId.isNotBlank() && selectedExitId !in exitIds) {
            exitIds += selectedExitId
            labels += "固定出口 ${selectedExitId.take(12)} · 当前未发现"
        }

        exitSelection.adapter = UiKit.themedSpinnerAdapter(this, labels)
        exitSelection.onItemSelectedListener = object : AdapterView.OnItemSelectedListener {
            override fun onNothingSelected(parent: AdapterView<*>?) = Unit

            override fun onItemSelected(
                parent: AdapterView<*>?,
                view: View?,
                position: Int,
                id: Long,
            ) {
                defaultExitId.setText(exitIds.getOrElse(position) { "" })
            }
        }
        val selectedIndex = exitIds.indexOf(selectedExitId).coerceAtLeast(0)
        exitSelection.setSelection(selectedIndex, false)
    }

    private fun buildNetworkModeTabs(): View {
        networkModeTabs.clear()

        val container = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            setPadding(dp(4), dp(4), dp(4), dp(4))
            background = rounded(surfaceSubtle, 13, line)
        }

        networkModeLabels.forEachIndexed { index, label ->
            val tab = TextView(this).apply {
                text = label
                textSize = 13f
                gravity = Gravity.CENTER
                setTypeface(typeface, Typeface.BOLD)
                setPadding(dp(6), 0, dp(6), 0)
                isClickable = true
                isFocusable = true
                setOnClickListener { selectNetworkMode(index) }
            }
            networkModeTabs += tab
            container.addView(
                tab,
                LinearLayout.LayoutParams(
                    0,
                    dp(42),
                    1f,
                ).apply {
                    if (index > 0) leftMargin = dp(4)
                }
            )
        }

        selectNetworkMode(selectedNetworkModeIndex)
        return container
    }

    private fun selectNetworkMode(index: Int) {
        selectedNetworkModeIndex = index.coerceIn(0, networkModeValues.lastIndex)
        networkModeTabs.forEachIndexed { tabIndex, tab ->
            val selected = tabIndex == selectedNetworkModeIndex
            tab.setTextColor(if (selected) Color.WHITE else muted)
            tab.background = if (selected) {
                rounded(brand, 10)
            } else {
                rounded(Color.TRANSPARENT, 10)
            }
        }
    }

    private fun card() = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL
        setPadding(dp(16), dp(16), dp(16), dp(16))
        background = rounded(surface, 18, line)
        elevation = dp(1).toFloat()
    }

    private fun addSectionHeader(parent: LinearLayout, title: String, subtitle: String) {
        parent.addView(TextView(this).apply {
            text = title
            textSize = 17f
            setTextColor(ink)
            setTypeface(typeface, Typeface.BOLD)
        })
        parent.addView(TextView(this).apply {
            text = subtitle
            textSize = 12f
            setTextColor(muted)
            setPadding(0, dp(4), 0, 0)
        })
    }

    private fun styledField(hintText: String) = EditText(this).apply {
        hint = hintText
        setSingleLine(true)
        textSize = 15f
        setTextColor(ink)
        setHintTextColor(UiPalette.placeholder)
        background = rounded(inputBg, 13, line)
        setPadding(dp(14), 0, dp(14), 0)
        minimumHeight = dp(50)
    }

    private fun numberField(hintText: String) = styledField(hintText).apply {
        inputType = InputType.TYPE_CLASS_NUMBER
    }

    private fun labeled(labelText: String, child: View) = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL
        addView(TextView(this@SettingsActivity).apply {
            text = labelText
            textSize = 12f
            setTextColor(muted)
            setTypeface(typeface, Typeface.BOLD)
            setPadding(dp(2), 0, 0, dp(6))
        })
        addView(
            child,
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT
            )
        )
    }

    private fun switchRow(title: String, subtitle: String, control: Switch) =
        LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            addView(LinearLayout(this@SettingsActivity).apply {
                orientation = LinearLayout.VERTICAL
                addView(TextView(this@SettingsActivity).apply {
                    text = title
                    textSize = 14f
                    setTextColor(ink)
                    setTypeface(typeface, Typeface.BOLD)
                })
                addView(TextView(this@SettingsActivity).apply {
                    text = subtitle
                    textSize = 11.5f
                    setTextColor(muted)
                    setPadding(0, dp(2), dp(10), 0)
                })
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

            control.showText = false
            UiKit.styleSwitch(control)
            addView(control)
        }

    private fun divider() = View(this).apply {
        setBackgroundColor(line)
        minimumHeight = dp(1)
    }

    private fun rounded(fill: Int, radiusDp: Int, stroke: Int? = null) =
        GradientDrawable().apply {
            shape = GradientDrawable.RECTANGLE
            setColor(fill)
            cornerRadius = dp(radiusDp).toFloat()
            if (stroke != null) setStroke(dp(1), stroke)
        }

    private fun topMargin(top: Int) = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        ViewGroup.LayoutParams.WRAP_CONTENT
    ).apply { topMargin = dp(top) }

    private fun dp(value: Int) = (value * resources.displayMetrics.density + 0.5f).toInt()
}
