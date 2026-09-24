package com.relayproxy.android

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
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
    private lateinit var infoApproval: TextView
    private lateinit var infoExitPermission: TextView
    private lateinit var infoUptime: TextView
    private lateinit var infoDeviceId: TextView
    private lateinit var toggleButton: Button

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
        handler.removeCallbacks(pollStatus)
        handler.post(pollStatus)
    }

    override fun onPause() {
        handler.removeCallbacks(pollStatus)
        super.onPause()
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

        val top = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        top.addView(LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            addView(TextView(this@MainActivity).apply {
                text = "出口状态"
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
                startActivity(Intent(this@MainActivity, SettingsActivity::class.java))
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
                    startActivity(Intent(this@MainActivity, SettingsActivity::class.java))
                }
            })
        })

        infoServer = infoRow(card, "Relay Server")
        infoDevice = infoRow(card, "设备名称")
        infoNetworkMode = infoRow(card, "出口网络")
        infoActiveNetwork = infoRow(card, "当前网络")
        infoApproval = infoRow(card, "设备审批")
        infoExitPermission = infoRow(card, "出口权限")
        infoUptime = infoRow(card, "运行时长")
        infoDeviceId = infoRow(card, "设备 ID").apply {
            setTextIsSelectable(true)
        }

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
        return toggleButton
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

    private fun startRelay() {
        val config = ConfigStore(this).load()
        if (config.serverAddress.isBlank()) {
            startActivity(Intent(this, SettingsActivity::class.java))
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
        val deviceId = obj.optString("deviceId", "")
        val uptimeMs = obj.optLong("serviceUptimeMs", 0)
        val error = obj.optString("lastError", "")

        when (state) {
            "CONNECTED" -> {
                statusSummary.text = if (approved) "网络出口已就绪" else "已连接，等待授权"
                if (approved) {
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
        statusStreams.text = streams.toString()
        statusLatency.text = if (latency > 0) "$latency ms" else "—"

        val store = ConfigStore(this)
        val config = store.load()
        infoServer.text = config.serverAddress.ifBlank { "未配置" }
        infoDevice.text = config.deviceName.ifBlank { "RelayProxy Android" }
        infoNetworkMode.text = networkModeLabel(config.networkMode)
        infoActiveNetwork.text = activeNetworkLabel(config.networkMode)
        infoApproval.text = approvalLabel(approval)
        infoExitPermission.text = if (approved) "已授权" else "未授权"
        infoUptime.text = formatDuration(uptimeMs)
        infoDeviceId.text = deviceId.ifBlank { "—" }

        val desiredRunning = store.isDesiredRunning()
        if (desiredRunning) {
            toggleButton.text = "停止"
            toggleButton.setTextColor(danger)
            toggleButton.background = rounded(surface, 14, Color.rgb(254, 202, 202))
        } else {
            toggleButton.text = "启动"
            toggleButton.setTextColor(Color.WHITE)
            toggleButton.background = rounded(brand, 14)
        }

        statusDetail.text = when {
            error.isNotBlank() -> error
            approval == "pending" -> "设备等待服务端审批"
            approval == "rejected" -> "设备审批已拒绝"
            state == "CONNECTED" && approved -> "后台常驻运行中"
            state == "STOPPED" -> "点击启动后可退出 App，服务继续后台运行"
            else -> "审批：$approval"
        }
    }

    private fun networkModeLabel(mode: String): String = when (mode) {
        NetworkBinder.MODE_WIFI -> "仅 Wi-Fi"
        NetworkBinder.MODE_CELLULAR -> "仅移动数据"
        else -> "自动选择"
    }

    private fun activeNetworkLabel(mode: String): String {
        if (mode == NetworkBinder.MODE_WIFI) return "Wi-Fi"
        if (mode == NetworkBinder.MODE_CELLULAR) return "移动数据"

        val connectivity = getSystemService(ConnectivityManager::class.java)
        val network = connectivity.activeNetwork ?: return "未连接"
        val capabilities = connectivity.getNetworkCapabilities(network) ?: return "未知"
        return when {
            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "Wi-Fi"
            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "移动数据"
            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "以太网"
            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_VPN) -> "VPN"
            else -> "其他"
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
