package com.relayproxy.android

import android.app.Activity
import android.content.Intent
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView

class SettingsHomeActivity : Activity() {
    private val backgroundColor get() = UiPalette.bg
    private val surfaceColor get() = UiPalette.surface
    private val inkColor get() = UiPalette.ink
    private val mutedColor get() = UiPalette.muted
    private val lineColor get() = UiPalette.line

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        UiPalette.isDark = ConfigStore(this).isDarkTheme()
        setTheme(if (UiPalette.isDark) R.style.Theme_RelayProxy_Dark else R.style.Theme_RelayProxy_Light)
        configureWindow()
    }

    override fun onResume() {
        super.onResume()
        setContentView(buildUi())
    }

    private fun configureWindow() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            window.setDecorFitsSystemWindows(true)
        }
        window.statusBarColor = backgroundColor
        window.navigationBarColor = backgroundColor
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
        val config = ConfigStore(this).load()
        val content = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(20), dp(24), dp(20), dp(36))
            setBackgroundColor(backgroundColor)
        }
        content.addView(TextView(this).apply {
            text = "功能设置目录"
            textSize = 22f
            setTextColor(inkColor)
            typeface = Typeface.DEFAULT_BOLD
        })
        content.addView(TextView(this).apply {
            text = "按功能分别配置，保存一页不会覆盖其他页面的内容。"
            textSize = 12.5f
            setTextColor(mutedColor)
            setPadding(0, dp(4), 0, dp(14))
        })

        content.addView(item(
            "连接与身份",
            listOf(config.serverAddress.ifBlank { "未配置服务器" }, config.deviceName)
                .joinToString(" · "),
        ) { openSection(SettingsActivity.SECTION_CONNECTION) }, margin())
        content.addView(item(
            "出口选择",
            proxyExitSummary(config),
        ) { openSection(SettingsActivity.SECTION_EXIT) }, margin())
        content.addView(item(
            "分流规则",
            routingSummary(config.routing),
        ) { startActivity(Intent(this, RoutingSettingsActivity::class.java)) }, margin())
        content.addView(item(
            "VPN 与应用范围",
            vpnSummary(config),
        ) { openSection(SettingsActivity.SECTION_VPN) }, margin())
        content.addView(item(
            "本机代理",
            localProxySummary(config),
        ) { openSection(SettingsActivity.SECTION_PROXY) }, margin())
        content.addView(item(
            "网络与连接策略",
            networkSummary(config),
        ) { openSection(SettingsActivity.SECTION_NETWORK) }, margin())

        return ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(backgroundColor)
            addView(content)
        }
    }

    private fun openSection(section: String) {
        startActivity(
            Intent(this, SettingsActivity::class.java)
                .putExtra(SettingsActivity.EXTRA_SECTION, section)
        )
    }

    private fun item(title: String, summary: String, onClick: () -> Unit): View =
        LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(16), dp(15), dp(16), dp(15))
            background = rounded(surfaceColor, 16, lineColor)
            isClickable = true
            isFocusable = true
            setOnClickListener { onClick() }
            addView(TextView(this@SettingsHomeActivity).apply {
                text = "$title  ›"
                textSize = 15f
                setTextColor(inkColor)
                typeface = Typeface.DEFAULT_BOLD
            })
            addView(TextView(this@SettingsHomeActivity).apply {
                text = summary
                textSize = 12f
                setTextColor(mutedColor)
                setPadding(0, dp(4), 0, 0)
            })
        }

    private fun proxyExitSummary(config: ExitConfig): String =
        if (config.defaultExitId.isBlank()) "自动选择可用出口"
        else "${config.defaultExitId.take(12)}…"

    private fun routingSummary(config: RoutingConfig): String = when (config.mode) {
        "rule" -> "按 ${config.rules.size} 条规则依次匹配"
        "direct" -> "全局直连"
        else -> "全局代理"
    }

    private fun vpnSummary(config: ExitConfig): String {
        val scope = when (config.vpnAppMode) {
            ExitConfig.VPN_APP_MODE_INCLUDE -> "仅 ${config.vpnPackages.size} 个应用进入 VPN"
            ExitConfig.VPN_APP_MODE_EXCLUDE -> "排除 ${config.vpnPackages.size} 个应用"
            else -> "全部应用进入 VPN"
        }
        return "$scope · ${if (config.vpnIpv6Enabled) "IPv4/IPv6" else "仅 IPv4"}"
    }

    private fun localProxySummary(config: ExitConfig): String {
        if (!config.clientEnabled) return "未启用"
        return buildList {
            if (config.socks5Enabled) add("SOCKS5 ${config.socks5Port}")
            if (config.httpEnabled) add("HTTP ${config.httpPort}")
        }.joinToString(" · ").ifBlank { "未启用" }
    }

    private fun networkSummary(config: ExitConfig): String {
        val preferred = if (config.networkMode == NetworkBinder.MODE_CELLULAR) "移动数据优先" else "Wi-Fi 优先"
        return if (config.autoNetworkSwitch) "$preferred · 自动切换" else preferred
    }

    private fun rounded(fill: Int, radiusDp: Int, stroke: Int? = null) =
        GradientDrawable().apply {
            shape = GradientDrawable.RECTANGLE
            setColor(fill)
            cornerRadius = dp(radiusDp).toFloat()
            if (stroke != null) setStroke(dp(1), stroke)
        }

    private fun margin() = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        ViewGroup.LayoutParams.WRAP_CONTENT,
    ).apply { topMargin = dp(10) }

    private fun dp(value: Int) = UiKit.dp(this, value)
}
