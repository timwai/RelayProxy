package com.relayproxy.android

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.res.ColorStateList
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.ArrayAdapter
import android.widget.Button
import android.widget.EditText
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.Spinner
import android.widget.Switch
import android.widget.TextView
import org.json.JSONObject

class MainActivity : Activity() {
    private lateinit var server: EditText
    private lateinit var deviceName: EditText
    private lateinit var quicPort: EditText
    private lateinit var tcpPort: EditText
    private lateinit var transport: Spinner
    private lateinit var tlsEnabled: Switch
    private lateinit var insecureTls: Switch
    private lateinit var allowPrivate: Switch
    private lateinit var cellularOnly: Switch

    private lateinit var statusBadge: TextView
    private lateinit var statusSummary: TextView
    private lateinit var statusTransport: TextView
    private lateinit var statusStreams: TextView
    private lateinit var statusLatency: TextView
    private lateinit var statusView: TextView

    private val transportValues = listOf("auto", "quic_only", "tcp_only")
    private val transportLabels = listOf("自动选择", "仅 QUIC", "仅 TCP/TLS")

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
        loadConfig()
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
        window.statusBarColor = bg
        window.navigationBarColor = bg
        window.decorView.systemUiVisibility =
            View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR or View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR
    }

    private fun buildUi(): ScrollView {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(20), dp(18), dp(20), dp(36))
            setBackgroundColor(bg)
        }

        root.addView(buildHeader())
        root.addView(buildStatusCard(), cardParams(18))
        root.addView(buildConnectionCard(), cardParams(16))
        root.addView(buildPolicyCard(), cardParams(16))
        root.addView(buildActionRow(), cardParams(18))

        root.addView(TextView(this).apply {
            text = "RelayProxy Android · 网络出口节点"
            textSize = 12f
            setTextColor(Color.rgb(148, 163, 184))
            gravity = Gravity.CENTER
            setPadding(0, dp(22), 0, 0)
        })

        return ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(bg)
            addView(root)
        }
    }

    private fun buildHeader(): View {
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }

        val logoFrame = LinearLayout(this).apply {
            gravity = Gravity.CENTER
            background = rounded(surface, 18, line)
            elevation = dp(2).toFloat()
        }
        logoFrame.addView(ImageView(this).apply {
            setImageResource(R.drawable.relayproxy_logo)
            scaleType = ImageView.ScaleType.CENTER_INSIDE
            setPadding(dp(8), dp(8), dp(8), dp(8))
        }, LinearLayout.LayoutParams(dp(54), dp(54)))
        row.addView(logoFrame, LinearLayout.LayoutParams(dp(64), dp(64)))

        row.addView(LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(12), 0, 0, 0)
            addView(TextView(this@MainActivity).apply {
                text = "RelayProxy"
                textSize = 25f
                setTextColor(ink)
                setTypeface(typeface, Typeface.BOLD)
            })
            addView(TextView(this@MainActivity).apply {
                text = "Android 网络出口"
                textSize = 13f
                setTextColor(muted)
                setPadding(0, dp(2), 0, 0)
            })
        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        statusBadge = chip("已停止", muted, Color.rgb(241, 245, 249))
        row.addView(statusBadge)

        return row
    }

    private fun buildStatusCard(): View {
        val card = card(ink)

        val top = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        top.addView(LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            addView(TextView(this@MainActivity).apply {
                text = "出口运行状态"
                textSize = 13f
                setTextColor(Color.rgb(148, 163, 184))
            })
            statusSummary = TextView(this@MainActivity).apply {
                text = "服务未启动"
                textSize = 20f
                setTextColor(Color.WHITE)
                setTypeface(typeface, Typeface.BOLD)
                setPadding(0, dp(4), 0, 0)
            }
            addView(statusSummary)
        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        top.addView(TextView(this).apply {
            text = "EXIT"
            textSize = 11f
            setTextColor(Color.rgb(191, 219, 254))
            setTypeface(typeface, Typeface.BOLD)
            gravity = Gravity.CENTER
            background = rounded(Color.rgb(30, 64, 175), 10)
            setPadding(dp(11), dp(7), dp(11), dp(7))
        })
        card.addView(top)

        val metrics = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            setPadding(0, dp(22), 0, 0)
        }
        val transportMetric = metric("传输")
        statusTransport = transportMetric.second
        metrics.addView(transportMetric.first, weighted())
        val streamsMetric = metric("活跃连接")
        statusStreams = streamsMetric.second
        metrics.addView(streamsMetric.first, weighted())
        val latencyMetric = metric("延迟")
        statusLatency = latencyMetric.second
        metrics.addView(latencyMetric.first, weighted())
        card.addView(metrics)

        statusView = TextView(this).apply {
            text = "等待启动"
            textSize = 12f
            setTextColor(Color.rgb(203, 213, 225))
            setLineSpacing(0f, 1.25f)
            setTextIsSelectable(true)
            setPadding(0, dp(18), 0, 0)
        }
        card.addView(statusView)

        return card
    }

    private fun buildConnectionCard(): View {
        val card = card(surface)
        addSectionHeader(
            card,
            "连接设置",
            "配置 Relay Server 与隧道传输方式。配置会保存在应用私有目录中。"
        )

        server = styledField("relay.example.com")
        card.addView(labeled("Relay Server", server), topMargin(18))

        deviceName = styledField("RelayProxy Android")
        card.addView(labeled("设备名称", deviceName), topMargin(14))

        transport = Spinner(this).apply {
            adapter = ArrayAdapter(
                this@MainActivity,
                android.R.layout.simple_spinner_item,
                transportLabels
            ).also { it.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item) }
            background = rounded(Color.rgb(248, 250, 252), 13, line)
            setPadding(dp(12), 0, dp(10), 0)
            minimumHeight = dp(52)
        }
        card.addView(labeled("传输方式", transport), topMargin(14))

        quicPort = numberField("443")
        tcpPort = numberField("443")
        val ports = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            val q = labeled("QUIC 端口", quicPort)
            addView(q, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            val spacer = View(this@MainActivity)
            addView(spacer, LinearLayout.LayoutParams(dp(12), 1))
            val t = labeled("TCP / TLS 端口", tcpPort)
            addView(t, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        }
        card.addView(ports, topMargin(14))

        return card
    }

    private fun buildPolicyCard(): View {
        val card = card(surface)
        addSectionHeader(
            card,
            "出口策略",
            "控制加密方式、出口网络以及手机侧可访问的目标范围。"
        )

        tlsEnabled = Switch(this)
        insecureTls = Switch(this)
        cellularOnly = Switch(this)
        allowPrivate = Switch(this)

        card.addView(
            switchRow(
                "启用 TLS",
                "推荐开启。保护 Relay Server 与手机出口节点之间的传输。",
                tlsEnabled
            ),
            topMargin(16)
        )
        card.addView(
            divider(),
            topMargin(12)
        )
        card.addView(
            switchRow(
                "允许自签名证书",
                "仅在你信任 Relay Server 且没有正式证书时开启。",
                insecureTls
            ),
            topMargin(12)
        )
        card.addView(
            divider(),
            topMargin(12)
        )
        card.addView(
            switchRow(
                "仅使用移动数据",
                "将 Relay 隧道和出口连接固定到蜂窝网络。",
                cellularOnly
            ),
            topMargin(12)
        )
        card.addView(
            divider(),
            topMargin(12)
        )
        card.addView(
            switchRow(
                "允许访问出口侧私网",
                "关闭时仅允许公网目标；开启后可访问手机所在局域网。",
                allowPrivate
            ),
            topMargin(12)
        )

        return card
    }

    private fun buildActionRow(): View {
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
        }

        val start = Button(this).apply {
            text = "启动网络共享"
            textSize = 15f
            setTextColor(Color.WHITE)
            setTypeface(typeface, Typeface.BOLD)
            setAllCaps(false)
            minimumHeight = dp(54)
            background = rounded(brand, 14)
            setOnClickListener {
                val config = readConfig()
                if (config.serverAddress.isBlank()) {
                    server.error = "必须填写 Server 地址"
                    server.requestFocus()
                    return@setOnClickListener
                }
                ConfigStore(this@MainActivity).save(config)
                val intent = Intent(this@MainActivity, RelayExitService::class.java)
                    .setAction(RelayExitService.ACTION_START)
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                    startForegroundService(intent)
                } else {
                    startService(intent)
                }
            }
        }
        row.addView(start, LinearLayout.LayoutParams(0, dp(54), 1f))

        row.addView(View(this), LinearLayout.LayoutParams(dp(12), 1))

        val stop = Button(this).apply {
            text = "停止"
            textSize = 15f
            setTextColor(danger)
            setTypeface(typeface, Typeface.BOLD)
            setAllCaps(false)
            minimumHeight = dp(54)
            background = rounded(surface, 14, Color.rgb(254, 202, 202))
            setOnClickListener {
                startService(
                    Intent(this@MainActivity, RelayExitService::class.java)
                        .setAction(RelayExitService.ACTION_STOP)
                )
            }
        }
        row.addView(stop, LinearLayout.LayoutParams(0, dp(54), 0.42f))

        return row
    }

    private fun card(fill: Int): LinearLayout = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL
        setPadding(dp(18), dp(18), dp(18), dp(18))
        background = rounded(fill, 20, if (fill == surface) line else null)
        if (fill == surface) elevation = dp(1).toFloat()
    }

    private fun addSectionHeader(parent: LinearLayout, title: String, subtitle: String) {
        parent.addView(TextView(this).apply {
            text = title
            textSize = 18f
            setTextColor(ink)
            setTypeface(typeface, Typeface.BOLD)
        })
        parent.addView(TextView(this).apply {
            text = subtitle
            textSize = 12.5f
            setTextColor(muted)
            setLineSpacing(0f, 1.2f)
            setPadding(0, dp(5), 0, 0)
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
        minimumHeight = dp(52)
    }

    private fun numberField(hintText: String) = styledField(hintText).apply {
        inputType = InputType.TYPE_CLASS_NUMBER
    }

    private fun labeled(labelText: String, child: View): LinearLayout =
        LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            addView(TextView(this@MainActivity).apply {
                text = labelText
                textSize = 12.5f
                setTextColor(Color.rgb(71, 85, 105))
                setTypeface(typeface, Typeface.BOLD)
                setPadding(dp(2), 0, 0, dp(7))
            })
            addView(
                child,
                LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT,
                    ViewGroup.LayoutParams.WRAP_CONTENT
                )
            )
        }

    private fun switchRow(title: String, subtitle: String, control: Switch): LinearLayout =
        LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL

            addView(LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.VERTICAL
                addView(TextView(this@MainActivity).apply {
                    text = title
                    textSize = 14.5f
                    setTextColor(ink)
                    setTypeface(typeface, Typeface.BOLD)
                })
                addView(TextView(this@MainActivity).apply {
                    text = subtitle
                    textSize = 12f
                    setTextColor(muted)
                    setLineSpacing(0f, 1.15f)
                    setPadding(0, dp(3), dp(12), 0)
                })
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

            control.showText = false
            control.thumbTintList = ColorStateList(
                arrayOf(
                    intArrayOf(android.R.attr.state_checked),
                    intArrayOf()
                ),
                intArrayOf(brand, Color.rgb(148, 163, 184))
            )
            control.trackTintList = ColorStateList(
                arrayOf(
                    intArrayOf(android.R.attr.state_checked),
                    intArrayOf()
                ),
                intArrayOf(Color.rgb(147, 197, 253), Color.rgb(226, 232, 240))
            )
            addView(control)
        }

    private fun divider() = View(this).apply {
        setBackgroundColor(Color.rgb(241, 245, 249))
        minimumHeight = dp(1)
    }

    private fun metric(label: String): Pair<LinearLayout, TextView> {
        val value = TextView(this).apply {
            text = "—"
            textSize = 16f
            setTextColor(Color.WHITE)
            setTypeface(typeface, Typeface.BOLD)
            gravity = Gravity.CENTER_HORIZONTAL
        }
        val group = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            addView(TextView(this@MainActivity).apply {
                text = label
                textSize = 11f
                setTextColor(Color.rgb(148, 163, 184))
                gravity = Gravity.CENTER_HORIZONTAL
            })
            addView(value, topMargin(4))
        }
        return group to value
    }

    private fun chip(textValue: String, color: Int, fill: Int) = TextView(this).apply {
        text = textValue
        textSize = 11.5f
        setTextColor(color)
        setTypeface(typeface, Typeface.BOLD)
        gravity = Gravity.CENTER
        background = rounded(fill, 11)
        setPadding(dp(10), dp(7), dp(10), dp(7))
    }

    private fun updateChip(textValue: String, color: Int, fill: Int) {
        statusBadge.text = textValue
        statusBadge.setTextColor(color)
        statusBadge.background = rounded(fill, 11)
    }

    private fun rounded(fill: Int, radiusDp: Int, stroke: Int? = null) =
        GradientDrawable().apply {
            shape = GradientDrawable.RECTANGLE
            setColor(fill)
            cornerRadius = dp(radiusDp).toFloat()
            if (stroke != null) setStroke(dp(1), stroke)
        }

    private fun cardParams(top: Int) = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        ViewGroup.LayoutParams.WRAP_CONTENT
    ).apply { topMargin = dp(top) }

    private fun topMargin(top: Int) = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        ViewGroup.LayoutParams.WRAP_CONTENT
    ).apply { topMargin = dp(top) }

    private fun weighted() = LinearLayout.LayoutParams(
        0,
        ViewGroup.LayoutParams.WRAP_CONTENT,
        1f
    )

    private fun dp(value: Int) = (value * resources.displayMetrics.density + 0.5f).toInt()

    private fun loadConfig() {
        val cfg = ConfigStore(this).load()
        server.setText(cfg.serverAddress)
        deviceName.setText(cfg.deviceName)
        quicPort.setText(cfg.quicPort.toString())
        tcpPort.setText(cfg.tcpPort.toString())
        val index = transportValues.indexOf(cfg.transportMode)
        transport.setSelection(if (index >= 0) index else 0)
        tlsEnabled.isChecked = cfg.tlsEnabled
        insecureTls.isChecked = cfg.insecureTls
        allowPrivate.isChecked = cfg.allowPrivateNetwork
        cellularOnly.isChecked = cfg.cellularOnly
    }

    private fun readConfig(): ExitConfig = ExitConfig(
        serverAddress = server.text.toString().trim(),
        deviceName = deviceName.text.toString().trim().ifBlank { "RelayProxy Android" },
        quicPort = quicPort.text.toString().toIntOrNull() ?: 443,
        tcpPort = tcpPort.text.toString().toIntOrNull() ?: 443,
        transportMode = transportValues.getOrElse(transport.selectedItemPosition) { "auto" },
        tlsEnabled = tlsEnabled.isChecked,
        insecureTls = insecureTls.isChecked,
        allowPrivateNetwork = allowPrivate.isChecked,
        cellularOnly = cellularOnly.isChecked,
    )

    private fun renderStatus() {
        val obj = runCatching { JSONObject(RelayExitService.statusJson()) }.getOrNull()
        if (obj == null) {
            statusSummary.text = "状态不可用"
            updateChip("未知", muted, Color.rgb(241, 245, 249))
            return
        }

        val state = obj.optString("connectionState", "UNKNOWN")
        val approval = obj.optString("approvalState", "unknown")
        val deviceId = obj.optString("deviceId", "")
        val transportValue = obj.optString("transport", "")
        val streams = obj.optLong("activeStreams", 0)
        val latency = obj.optLong("latencyMs", 0)
        val approved = obj.optBoolean("exitApproved", false)
        val error = obj.optString("lastError", "")

        when (state) {
            "CONNECTED" -> {
                statusSummary.text = if (approved) "网络出口已就绪" else "已连接，等待出口授权"
                if (approved) {
                    updateChip("运行中", success, successSoft)
                } else {
                    updateChip("待审批", warning, warningSoft)
                }
            }
            "CONNECTING" -> {
                statusSummary.text = "正在连接 Relay Server"
                updateChip("连接中", brand, brandSoft)
            }
            "WAITING_NETWORK" -> {
                statusSummary.text = "等待可用网络"
                updateChip("等待网络", warning, warningSoft)
            }
            "ERROR" -> {
                statusSummary.text = "出口服务发生错误"
                updateChip("错误", danger, dangerSoft)
            }
            "STOPPED" -> {
                statusSummary.text = "服务未启动"
                updateChip("已停止", muted, Color.rgb(241, 245, 249))
            }
            else -> {
                statusSummary.text = state
                updateChip("状态更新", brand, brandSoft)
            }
        }

        statusTransport.text = transportValue.ifBlank { "—" }
        statusStreams.text = streams.toString()
        statusLatency.text = if (latency > 0) "$latency ms" else "—"

        val approvalText = when (approval) {
            "approved" -> "已批准"
            "pending" -> "等待审批"
            "rejected" -> "已拒绝"
            else -> approval
        }

        statusView.text = buildString {
            append("设备审批：").append(approvalText)
            append("  ·  出口权限：").append(if (approved) "已授权" else "未授权")
            if (deviceId.isNotBlank()) append("\n设备 ID：").append(deviceId)
            if (error.isNotBlank()) append("\n").append(error)
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
