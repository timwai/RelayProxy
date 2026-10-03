package com.relayproxy.android

import android.app.Activity
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Bundle
import android.text.InputType
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
import com.relayproxy.core.androidcore.Androidcore
import org.json.JSONObject
import java.util.UUID

class RoutingRuleActivity : Activity() {
    companion object {
        const val EXTRA_RULE_ID = "routingRuleId"
    }

    private lateinit var name: EditText
    private lateinit var enabled: Switch
    private lateinit var targets: EditText
    private lateinit var ports: EditText
    private lateinit var protocol: Spinner
    private lateinit var action: Spinner
    private lateinit var exit: Spinner
    private var exitIds = listOf("")
    private var original: RoutingRuleConfig? = null
    private var routing = RoutingConfig()

    private val protocolValues = listOf("", "tcp", "udp")
    private val protocolLabels = listOf("全部", "TCP", "UDP")
    private val actionValues = listOf("PROXY", "DIRECT", "REJECT")
    private val actionLabels = listOf("通过 Relay 出口", "本机直连", "拒绝连接")
    private val backgroundColor = Color.rgb(246, 248, 252)
    private val surfaceColor = Color.WHITE
    private val inkColor = Color.rgb(15, 23, 42)
    private val mutedColor = Color.rgb(100, 116, 139)
    private val lineColor = Color.rgb(226, 232, 240)
    private val brandColor = Color.rgb(37, 99, 235)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        configureWindow()
        routing = ConfigStore(this).load().routing
        val id = intent.getStringExtra(EXTRA_RULE_ID).orEmpty()
        original = routing.rules.firstOrNull { it.id == id }
        setContentView(buildUi())
        loadRule(original ?: RoutingRuleConfig(id = UUID.randomUUID().toString()))
    }

    private fun configureWindow() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) window.setDecorFitsSystemWindows(true)
        window.statusBarColor = backgroundColor
        window.navigationBarColor = backgroundColor
        @Suppress("DEPRECATION")
        window.decorView.systemUiVisibility =
            View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR or View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR
    }

    private fun buildUi(): View {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(20), dp(30), dp(20), dp(36))
            setBackgroundColor(backgroundColor)
        }
        root.addView(TextView(this).apply {
            text = if (original == null) "添加规则" else "编辑规则"
            textSize = 23f
            setTextColor(inkColor)
            setTypeface(typeface, Typeface.BOLD)
        })
        root.addView(TextView(this).apply {
            text = "目标、端口和协议同时满足时命中；每行可填写一个目标或端口范围。"
            textSize = 12.5f
            setTextColor(mutedColor)
            setPadding(0, dp(5), 0, dp(12))
        })

        val card = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(16), dp(16), dp(16), dp(16))
            background = rounded(surfaceColor, 16, lineColor)
        }
        name = field("例如：公司内网")
        card.addView(labeled("规则名称", name))
        enabled = Switch(this).apply { text = "启用此规则" }
        card.addView(enabled, topMargin(10))

        targets = field("例如：10.0.0.0/8\n*.example.com", multiline = true)
        card.addView(labeled("IP、CIDR 或域名", targets), topMargin(12))
        ports = field("例如：80\n443\n8000-9000", multiline = true)
        card.addView(labeled("端口（留空表示全部）", ports), topMargin(12))
        protocol = spinner(protocolLabels)
        card.addView(labeled("协议", protocol), topMargin(12))
        action = spinner(actionLabels)
        card.addView(labeled("动作", action), topMargin(12))
        exit = spinner(listOf("跟随默认出口"))
        card.addView(labeled("代理出口", exit), topMargin(12))
        card.addView(TextView(this).apply {
            text = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                "应用条件将在 VPN 原始流归属链路接通后开放；当前页面保存 IP、域名、端口与协议规则。"
            } else {
                "Android 8/9 支持当前非应用规则；按应用分流需要 Android 10 或更高版本。"
            }
            textSize = 11.5f
            setTextColor(mutedColor)
            setPadding(0, dp(14), 0, 0)
        })
        root.addView(card)
        root.addView(Button(this).apply {
            text = "保存规则"
            setAllCaps(false)
            setTextColor(Color.WHITE)
            setTypeface(typeface, Typeface.BOLD)
            background = rounded(brandColor, 14)
            setOnClickListener { saveRule() }
        }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(52)).apply {
            topMargin = dp(16)
        })

        return ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(backgroundColor)
            addView(root)
        }
    }

    private fun loadRule(rule: RoutingRuleConfig) {
        name.setText(rule.name)
        enabled.isChecked = rule.enabled
        targets.setText(rule.targets.joinToString("\n"))
        ports.setText(rule.ports.joinToString("\n"))
        val protocolValue = when {
            rule.protocols.size == 1 -> rule.protocols.first().lowercase()
            else -> ""
        }
        protocol.setSelection(protocolValues.indexOf(protocolValue).coerceAtLeast(0), false)
        action.setSelection(actionValues.indexOf(rule.action).coerceAtLeast(0), false)
        populateExits(rule.exitId)
    }

    private fun populateExits(selectedExitId: String) {
        val ids = mutableListOf("")
        val labels = mutableListOf("跟随默认出口")
        val status = runCatching { JSONObject(RelayExitService.statusJson()) }.getOrNull()
        val available = status?.optJSONArray("proxyExits")
        if (available != null) {
            for (index in 0 until available.length()) {
                val item = available.optJSONObject(index) ?: continue
                val id = item.optString("deviceId").trim()
                if (id.isBlank() || id in ids) continue
                ids += id
                val displayName = item.optString("name").ifBlank { id.take(12) }
                val identity = item.optString("identityName")
                val state = if (item.optBoolean("online", false)) "在线" else "离线"
                labels += buildList {
                    add(displayName)
                    if (identity.isNotBlank()) add(identity)
                    add(state)
                }.joinToString(" · ")
            }
        }
        if (selectedExitId.isNotBlank() && selectedExitId !in ids) {
            ids += selectedExitId
            labels += "${selectedExitId.take(12)} · 当前不可用"
        }
        exitIds = ids
        exit.adapter = ArrayAdapter(this, android.R.layout.simple_spinner_item, labels).also {
            it.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item)
        }
        exit.setSelection(ids.indexOf(selectedExitId).coerceAtLeast(0), false)
    }

    private fun saveRule() {
        val ruleName = name.text.toString().trim()
        if (ruleName.isBlank()) {
            name.error = "请填写规则名称"
            name.requestFocus()
            return
        }
        val targetValues = splitValues(targets.text.toString())
        if (targetValues.any { it.contains('/') && it.substringAfter('/').toIntOrNull() == null }) {
            targets.error = "CIDR 前缀格式不正确"
            targets.requestFocus()
            return
        }
        val portValues = splitValues(ports.text.toString())
        if (portValues.any { !validPort(it) }) {
            ports.error = "端口应为 1-65535、范围或 *"
            ports.requestFocus()
            return
        }
        val selectedAction = actionValues.getOrElse(action.selectedItemPosition) { "PROXY" }
        val current = original ?: RoutingRuleConfig(id = UUID.randomUUID().toString())
        val updated = current.copy(
            name = ruleName,
            enabled = enabled.isChecked,
            action = selectedAction,
            exitId = if (selectedAction == "PROXY") exitIds.getOrElse(exit.selectedItemPosition) { "" } else "",
            targets = targetValues,
            ports = portValues,
            protocols = protocolValues.getOrElse(protocol.selectedItemPosition) { "" }
                .takeIf(String::isNotBlank)?.let(::listOf).orEmpty(),
        )
        val rules = routing.rules.toMutableList()
        val index = rules.indexOfFirst { it.id == updated.id }
        if (index >= 0) rules[index] = updated else rules += updated
        val next = routing.copy(rules = rules)
        val validationError = runCatching {
            Androidcore.validateRoutingConfig(next.toJson(forCore = true).toString())
        }.exceptionOrNull()
        if (validationError != null) {
            Toast.makeText(
                this,
                validationError.message ?: "规则格式不正确",
                Toast.LENGTH_LONG,
            ).show()
            return
        }
        ConfigStore(this).saveRouting(next)
        reconfigureRuntime()
        Toast.makeText(this, "规则已保存", Toast.LENGTH_SHORT).show()
        finish()
    }

    private fun reconfigureRuntime() {
        val store = ConfigStore(this)
        val current = store.load()
        if (store.isDesiredRunning() || store.isVpnDesiredRunning() || current.clientEnabled) {
            val relay = android.content.Intent(this, RelayExitService::class.java)
                .setAction(RelayExitService.ACTION_RECONFIGURE)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) startForegroundService(relay)
            else startService(relay)
        }
        if (store.isVpnDesiredRunning()) {
            val vpn = android.content.Intent(this, RelayVpnService::class.java)
                .setAction(RelayVpnService.ACTION_RECONFIGURE)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) startForegroundService(vpn)
            else startService(vpn)
        }
    }

    private fun splitValues(raw: String): List<String> = raw
        .split(',', ';', '\n', '\r', '\t', ' ')
        .map(String::trim)
        .filter(String::isNotBlank)
        .distinct()

    private fun validPort(value: String): Boolean {
        if (value == "*") return true
        val parts = value.split('-')
        if (parts.size !in 1..2) return false
        val numbers = parts.map { it.toIntOrNull() ?: return false }
        return numbers.all { it in 1..65535 } && (numbers.size == 1 || numbers[0] <= numbers[1])
    }

    private fun field(hintText: String, multiline: Boolean = false) = EditText(this).apply {
        hint = hintText
        textSize = 14f
        setTextColor(inkColor)
        setHintTextColor(Color.rgb(148, 163, 184))
        background = rounded(Color.rgb(248, 250, 252), 12, lineColor)
        setPadding(dp(12), dp(if (multiline) 10 else 0), dp(12), dp(if (multiline) 10 else 0))
        if (multiline) {
            minLines = 3
            gravity = android.view.Gravity.TOP
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE
        } else {
            setSingleLine(true)
            minimumHeight = dp(48)
        }
    }

    private fun spinner(labels: List<String>) = Spinner(this).apply {
        adapter = ArrayAdapter(this@RoutingRuleActivity, android.R.layout.simple_spinner_item, labels).also {
            it.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item)
        }
        minimumHeight = dp(48)
        setPadding(dp(10), 0, dp(8), 0)
        background = rounded(Color.rgb(248, 250, 252), 12, lineColor)
    }

    private fun labeled(label: String, child: View) = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL
        addView(TextView(this@RoutingRuleActivity).apply {
            text = label
            textSize = 12f
            setTextColor(mutedColor)
            setPadding(dp(2), 0, 0, dp(5))
        })
        addView(child)
    }

    private fun rounded(fill: Int, radiusDp: Int, stroke: Int? = null) = GradientDrawable().apply {
        shape = GradientDrawable.RECTANGLE
        setColor(fill)
        cornerRadius = dp(radiusDp).toFloat()
        if (stroke != null) setStroke(dp(1), stroke)
    }

    private fun topMargin(value: Int) = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        ViewGroup.LayoutParams.WRAP_CONTENT,
    ).apply { topMargin = dp(value) }

    private fun dp(value: Int) = (value * resources.displayMetrics.density + 0.5f).toInt()
}
