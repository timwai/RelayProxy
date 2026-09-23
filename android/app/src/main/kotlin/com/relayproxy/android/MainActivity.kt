package com.relayproxy.android

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.InputType
import android.view.ViewGroup
import android.widget.ArrayAdapter
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.Spinner
import android.widget.TextView
import org.json.JSONObject

class MainActivity : Activity() {
    private lateinit var server: EditText
    private lateinit var deviceName: EditText
    private lateinit var quicPort: EditText
    private lateinit var tcpPort: EditText
    private lateinit var transport: Spinner
    private lateinit var tlsEnabled: CheckBox
    private lateinit var insecureTls: CheckBox
    private lateinit var allowPrivate: CheckBox
    private lateinit var cellularOnly: CheckBox
    private lateinit var statusView: TextView

    private val handler = Handler(Looper.getMainLooper())
    private val pollStatus = object : Runnable {
        override fun run() {
            renderStatus()
            handler.postDelayed(this, 1000)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        title = "RelayProxy"
        setContentView(buildUi())
        loadConfig()
        requestNotificationPermission()
    }

    override fun onResume() {
        super.onResume()
        handler.post(pollStatus)
    }

    override fun onPause() {
        handler.removeCallbacks(pollStatus)
        super.onPause()
    }

    private fun buildUi(): ScrollView {
        val density = resources.displayMetrics.density
        fun dp(v: Int) = (v * density).toInt()

        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(20), dp(20), dp(20), dp(32))
        }

        root.addView(TextView(this).apply {
            text = "RelayProxy Android"
            textSize = 26f
        })
        root.addView(TextView(this).apply {
            text = "第一期：把 Android 当前网络作为 RelayProxy 出口。支持 TCP/UDP、自动重连和移动数据固定出口。"
            textSize = 14f
            setPadding(0, dp(6), 0, dp(18))
        })

        server = field("Relay Server 地址（域名或 IP）")
        deviceName = field("设备名称")
        quicPort = numberField("QUIC 端口")
        tcpPort = numberField("TCP/TLS 端口")

        transport = Spinner(this).apply {
            adapter = ArrayAdapter(
                this@MainActivity,
                android.R.layout.simple_spinner_dropdown_item,
                listOf("auto", "quic_only", "tcp_only")
            )
        }

        tlsEnabled = CheckBox(this).apply { text = "启用 TLS（推荐）" }
        insecureTls = CheckBox(this).apply { text = "允许不受信任/自签名证书" }
        cellularOnly = CheckBox(this).apply { text = "仅使用移动数据作为出口" }
        allowPrivate = CheckBox(this).apply { text = "允许访问出口侧局域网/私网" }

        root.addView(server)
        root.addView(deviceName)
        root.addView(label("传输方式"))
        root.addView(transport)
        root.addView(quicPort)
        root.addView(tcpPort)
        root.addView(tlsEnabled)
        root.addView(insecureTls)
        root.addView(cellularOnly)
        root.addView(allowPrivate)

        val start = Button(this).apply {
            text = "启动网络共享"
            setOnClickListener {
                val config = readConfig()
                if (config.serverAddress.isBlank()) {
                    server.error = "必须填写 Server 地址"
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
        val stop = Button(this).apply {
            text = "停止"
            setOnClickListener {
                startService(
                    Intent(this@MainActivity, RelayExitService::class.java)
                        .setAction(RelayExitService.ACTION_STOP)
                )
            }
        }
        root.addView(start)
        root.addView(stop)

        statusView = TextView(this).apply {
            textSize = 14f
            setPadding(0, dp(18), 0, 0)
            setTextIsSelectable(true)
        }
        root.addView(statusView)

        return ScrollView(this).apply { addView(root) }
    }

    private fun field(hintText: String) = EditText(this).apply {
        hint = hintText
        layoutParams = LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT,
            ViewGroup.LayoutParams.WRAP_CONTENT
        )
    }

    private fun numberField(hintText: String) = field(hintText).apply {
        inputType = InputType.TYPE_CLASS_NUMBER
    }

    private fun label(textValue: String) = TextView(this).apply {
        text = textValue
        setPadding(0, 12, 0, 4)
    }

    private fun loadConfig() {
        val cfg = ConfigStore(this).load()
        server.setText(cfg.serverAddress)
        deviceName.setText(cfg.deviceName)
        quicPort.setText(cfg.quicPort.toString())
        tcpPort.setText(cfg.tcpPort.toString())
        val index = listOf("auto", "quic_only", "tcp_only").indexOf(cfg.transportMode)
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
        transportMode = transport.selectedItem?.toString() ?: "auto",
        tlsEnabled = tlsEnabled.isChecked,
        insecureTls = insecureTls.isChecked,
        allowPrivateNetwork = allowPrivate.isChecked,
        cellularOnly = cellularOnly.isChecked,
    )

    private fun renderStatus() {
        val obj = runCatching { JSONObject(RelayExitService.statusJson()) }.getOrNull()
        if (obj == null) {
            statusView.text = "状态：未知"
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

        statusView.text = buildString {
            append("连接：").append(state)
            append("\n审批：").append(approval)
            append("\n出口权限：").append(if (approved) "已授权" else "未授权/等待审批")
            if (deviceId.isNotBlank()) append("\n设备 ID：").append(deviceId)
            if (transportValue.isNotBlank()) append("\n传输：").append(transportValue)
            append("\n活跃连接：").append(streams)
            if (latency > 0) append("\n延迟：").append(latency).append(" ms")
            if (error.isNotBlank()) append("\n错误：").append(error)
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
