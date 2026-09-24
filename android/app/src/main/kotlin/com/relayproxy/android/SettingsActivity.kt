package com.relayproxy.android

import android.app.Activity
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
    private lateinit var deviceName: EditText
    private lateinit var quicPort: EditText
    private lateinit var tcpPort: EditText
    private lateinit var transport: Spinner
    private lateinit var tlsEnabled: Switch
    private lateinit var insecureTls: Switch
    private lateinit var allowPrivate: Switch
    private val networkModeTabs = mutableListOf<TextView>()
    private var selectedNetworkModeIndex = 0

    private val transportValues = listOf("auto", "quic_only", "tcp_only")
    private val transportLabels = listOf("自动选择", "仅 QUIC", "仅 TCP/TLS")
    private val networkModeValues = listOf(
        NetworkBinder.MODE_AUTO,
        NetworkBinder.MODE_CELLULAR,
        NetworkBinder.MODE_WIFI,
    )
    private val networkModeLabels = listOf("自动选择", "仅移动数据", "仅 Wi-Fi")

    private val bg = Color.rgb(246, 248, 252)
    private val surface = Color.WHITE
    private val ink = Color.rgb(15, 23, 42)
    private val muted = Color.rgb(100, 116, 139)
    private val line = Color.rgb(226, 232, 240)
    private val brand = Color.rgb(37, 99, 235)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        configureWindow()
        setContentView(buildUi())
        loadConfig()
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

    private fun buildUi(): ScrollView {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(20), dp(30), dp(20), dp(36))
            setBackgroundColor(bg)
        }

        val connection = card()
        addSectionHeader(connection, "连接设置", "配置 Relay Server 和传输参数。")
        server = styledField("relay.example.com")
        connection.addView(labeled("Relay Server", server), topMargin(16))
        deviceName = styledField(ConfigStore(this).defaultDeviceName())
        connection.addView(labeled("设备名称", deviceName), topMargin(12))

        transport = Spinner(this).apply {
            adapter = ArrayAdapter(
                this@SettingsActivity,
                android.R.layout.simple_spinner_item,
                transportLabels
            ).also { it.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item) }
            background = rounded(Color.rgb(248, 250, 252), 13, line)
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
        root.addView(connection, topMargin(14))

        val policy = card()
        addSectionHeader(policy, "出口策略", "控制加密、出口网络和私网访问范围。")
        tlsEnabled = Switch(this)
        insecureTls = Switch(this)
        allowPrivate = Switch(this)

        policy.addView(switchRow("启用 TLS", "推荐开启。", tlsEnabled), topMargin(14))
        policy.addView(divider(), topMargin(10))
        policy.addView(switchRow("允许自签名证书", "仅用于可信的自建服务端。", insecureTls), topMargin(10))
        policy.addView(divider(), topMargin(10))

        policy.addView(labeled("出口网络", buildNetworkModeTabs()), topMargin(12))
        policy.addView(divider(), topMargin(10))
        policy.addView(switchRow("允许访问出口侧私网", "开启后可访问手机所在局域网。", allowPrivate), topMargin(10))
        root.addView(policy, topMargin(14))

        root.addView(Button(this).apply {
            text = "保存设置"
            textSize = 15f
            setTextColor(Color.WHITE)
            setTypeface(typeface, Typeface.BOLD)
            setAllCaps(false)
            background = rounded(brand, 14)
            setOnClickListener { saveAndClose() }
        }, LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT,
            dp(52)
        ).apply { topMargin = dp(16) })

        if (ConfigStore(this).isDesiredRunning()) {
            root.addView(TextView(this).apply {
                text = "服务正在后台运行。修改设置后请在首页停止并重新启动，使新配置生效。"
                textSize = 12f
                setTextColor(muted)
                gravity = Gravity.CENTER
                setPadding(dp(8), dp(12), dp(8), 0)
            })
        }

        return ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(bg)
            addView(root)
        }
    }

    private fun saveAndClose() {
        val config = ExitConfig(
            serverAddress = server.text.toString().trim(),
            deviceName = deviceName.text.toString().trim().ifBlank {
                ConfigStore(this).defaultDeviceName()
            },
            quicPort = quicPort.text.toString().toIntOrNull() ?: 443,
            tcpPort = tcpPort.text.toString().toIntOrNull() ?: 443,
            transportMode = transportValues.getOrElse(transport.selectedItemPosition) { "auto" },
            tlsEnabled = tlsEnabled.isChecked,
            insecureTls = insecureTls.isChecked,
            allowPrivateNetwork = allowPrivate.isChecked,
            networkMode = networkModeValues.getOrElse(selectedNetworkModeIndex) {
                NetworkBinder.MODE_AUTO
            },
        )
        if (config.serverAddress.isBlank()) {
            server.error = "必须填写 Server 地址"
            server.requestFocus()
            return
        }
        ConfigStore(this).save(config)
        Toast.makeText(this, "设置已保存", Toast.LENGTH_SHORT).show()
        finish()
    }

    private fun loadConfig() {
        val cfg = ConfigStore(this).load()
        server.setText(cfg.serverAddress)
        deviceName.setText(cfg.deviceName)
        quicPort.setText(cfg.quicPort.toString())
        tcpPort.setText(cfg.tcpPort.toString())
        transport.setSelection(transportValues.indexOf(cfg.transportMode).coerceAtLeast(0))
        tlsEnabled.isChecked = cfg.tlsEnabled
        insecureTls.isChecked = cfg.insecureTls
        allowPrivate.isChecked = cfg.allowPrivateNetwork
        selectNetworkMode(networkModeValues.indexOf(cfg.networkMode).coerceAtLeast(0))
    }

    private fun buildNetworkModeTabs(): View {
        networkModeTabs.clear()

        val container = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            setPadding(dp(4), dp(4), dp(4), dp(4))
            background = rounded(Color.rgb(241, 245, 249), 13, line)
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
            tab.setTextColor(if (selected) Color.WHITE else Color.rgb(71, 85, 105))
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
        setHintTextColor(Color.rgb(148, 163, 184))
        background = rounded(Color.rgb(248, 250, 252), 13, line)
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
            setTextColor(Color.rgb(71, 85, 105))
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
            control.thumbTintList = ColorStateList(
                arrayOf(intArrayOf(android.R.attr.state_checked), intArrayOf()),
                intArrayOf(brand, Color.rgb(148, 163, 184))
            )
            control.trackTintList = ColorStateList(
                arrayOf(intArrayOf(android.R.attr.state_checked), intArrayOf()),
                intArrayOf(Color.rgb(147, 197, 253), Color.rgb(226, 232, 240))
            )
            addView(control)
        }

    private fun divider() = View(this).apply {
        setBackgroundColor(Color.rgb(241, 245, 249))
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
