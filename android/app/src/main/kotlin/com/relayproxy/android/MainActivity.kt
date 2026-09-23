package com.relayproxy.android

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
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

    private fun buildUi(): ScrollView {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(16), dp(12), dp(16), dp(28))
            setBackgroundColor(bg)
        }

        root.addView(buildStatusCard())
        root.addView(buildActionRow(), topMargin(14))

        return ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(bg)
            addView(root)
        }
    }

    private fun buildStatusCard(): View {
        val card = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(16), dp(14), dp(16), dp(14))
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
        card.addView(top)

        val metrics = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            setPadding(0, dp(12), 0, 0)
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
            setPadding(0, dp(10), 0, 0)
        }
        card.addView(statusDetail)

        return card
    }

    private fun buildActionRow(): View {
        val column = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
        }

        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
        }

        val start = Button(this).apply {
            text = "启动"
            textSize = 15f
            setTextColor(Color.WHITE)
            setTypeface(typeface, Typeface.BOLD)
            setAllCaps(false)
            background = rounded(brand, 14)
            setOnClickListener { startRelay() }
        }
        row.addView(start, LinearLayout.LayoutParams(0, dp(52), 1f))

        row.addView(View(this), LinearLayout.LayoutParams(dp(10), 1))

        val stop = Button(this).apply {
            text = "停止"
            textSize = 15f
            setTextColor(danger)
            setTypeface(typeface, Typeface.BOLD)
            setAllCaps(false)
            background = rounded(surface, 14, Color.rgb(254, 202, 202))
            setOnClickListener {
                startService(
                    Intent(this@MainActivity, RelayExitService::class.java)
                        .setAction(RelayExitService.ACTION_STOP)
                )
            }
        }
        row.addView(stop, LinearLayout.LayoutParams(0, dp(52), 0.55f))
        column.addView(row)

        val settings = Button(this).apply {
            text = "设置"
            textSize = 13.5f
            setTextColor(Color.rgb(71, 85, 105))
            setTypeface(typeface, Typeface.BOLD)
            setAllCaps(false)
            background = rounded(surface, 13, line)
            setOnClickListener {
                startActivity(Intent(this@MainActivity, SettingsActivity::class.java))
            }
        }
        column.addView(
            settings,
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                dp(46)
            ).apply { topMargin = dp(10) }
        )

        return column
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

        statusDetail.text = when {
            error.isNotBlank() -> error
            approval == "pending" -> "设备等待服务端审批"
            approval == "rejected" -> "设备审批已拒绝"
            state == "CONNECTED" && approved -> "后台常驻运行中"
            state == "STOPPED" -> "点击启动后可退出 App，服务继续后台运行"
            else -> "审批：$approval"
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
