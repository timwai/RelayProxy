package com.relayproxy.android

import android.app.Activity
import android.content.Intent
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Bundle
import android.text.InputType
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
import com.relayproxy.core.androidcore.Androidcore
import org.json.JSONObject
import java.util.UUID

class RoutingRuleActivity : Activity() {
    companion object {
        const val EXTRA_RULE_ID = "routingRuleId"
        private const val REQUEST_APPLICATIONS = 3401
    }

    private lateinit var name: EditText
    private lateinit var enabled: Switch
    private lateinit var applicationsSummary: TextView
    private lateinit var targets: EditText
    private lateinit var ports: EditText
    private lateinit var protocol: Spinner
    private lateinit var action: Spinner
    private lateinit var exit: Spinner
    private lateinit var exitContainer: View
    private var exitIds = listOf("")
    private var original: RoutingRuleConfig? = null
    private var routing = RoutingConfig()
    private val selectedApplications = linkedSetOf<String>()

    private val protocolValues = listOf("", "tcp", "udp")
    private val protocolLabels = listOf("全部", "TCP", "UDP")
    private val actionValues = listOf("PROXY", "DIRECT", "REJECT")
    private val actionLabels = listOf("通过 Relay 出口", "本机直连", "拒绝连接")
    private val backgroundColor get() = UiPalette.bg
    private val surfaceColor get() = UiPalette.surface
    private val inputBg get() = UiPalette.inputBg
    private val inkColor get() = UiPalette.ink
    private val mutedColor get() = UiPalette.muted
    private val lineColor get() = UiPalette.line
    private val brandColor get() = UiPalette.brand

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        UiPalette.sync(this)
        setTheme(if (UiPalette.isDark) R.style.Theme_RelayProxy_Dark else R.style.Theme_RelayProxy_Light)
        configureWindow()
        routing = savedInstanceState?.getString("routingSnapshot")?.let(RoutingConfig::fromJson)
            ?: ConfigStore(this).load().routing
        val id = intent.getStringExtra(EXTRA_RULE_ID).orEmpty()
        original = routing.rules.firstOrNull { it.id == id }
        setContentView(buildUi())
        val draft = savedInstanceState?.getString("ruleDraft")?.let(RoutingConfig::fromJson)?.rules?.firstOrNull()
        loadRule(draft ?: original ?: RoutingRuleConfig(id = UUID.randomUUID().toString()))
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putString("routingSnapshot", routing.toJson().toString())
        outState.putString("ruleDraft", RoutingConfig(rules = listOf(readRule())).toJson().toString())
    }

    @Deprecated("Deprecated Android activity result API retained for API 26 compatibility")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode != REQUEST_APPLICATIONS || resultCode != RESULT_OK) return
        selectedApplications.clear()
        selectedApplications += data?.getStringArrayListExtra(VpnAppSelectionActivity.EXTRA_SELECTED)
            .orEmpty()
            .filter { it != packageName }
        updateApplicationsSummary()
    }

    private fun configureWindow() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) window.setDecorFitsSystemWindows(true)
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
            text = "应用、目标、端口和协议同时满足时命中。Telegram 等应用会直接连接 IP；如需代理该应用的全部流量，只选择应用并将目标留空。"
            textSize = 12.5f
            setTextColor(mutedColor)
            setPadding(0, dp(5), 0, dp(12))
        })

        val card = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(18), dp(18), dp(18), dp(18))
            background = rounded(surfaceColor, 16, lineColor)
        }
        name = field("例如：公司内网")
        card.addView(labeled("规则名称", name))
        enabled = Switch(this).apply {
            text = "启用此规则"
            setTextColor(inkColor)
            UiKit.styleSwitch(this)
        }
        card.addView(enabled, topMargin(14))

        applicationsSummary = TextView(this).apply {
            textSize = 12.5f
            setTextColor(inkColor)
            setPadding(dp(12), dp(12), dp(12), dp(12))
            background = rounded(inputBg, 12, lineColor)
            isEnabled = Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q
            alpha = if (isEnabled) 1f else 0.55f
            setOnClickListener {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) openApplicationSelection()
            }
        }
        card.addView(labeled("应用（Android 10+）", applicationsSummary), topMargin(16))

        targets = field("例如：10.0.0.0/8\n*.example.com", multiline = true)
        card.addView(labeled("IP、CIDR 或域名", targets), topMargin(16))
        ports = field("例如：80\n443\n8000-9000", multiline = true)
        card.addView(labeled("端口（留空表示全部）", ports), topMargin(16))
        protocol = spinner(protocolLabels)
        card.addView(labeled("协议", protocol), topMargin(16))
        action = spinner(actionLabels)
        card.addView(labeled("动作", action), topMargin(16))
        exit = spinner(listOf("跟随默认出口"))
        exitContainer = labeled("代理出口", exit)
        card.addView(exitContainer, topMargin(16))
        action.onItemSelectedListener = object : AdapterView.OnItemSelectedListener {
            override fun onNothingSelected(parent: AdapterView<*>?) = Unit

            override fun onItemSelected(
                parent: AdapterView<*>?,
                view: View?,
                position: Int,
                id: Long,
            ) {
                exitContainer.visibility = if (
                    actionValues.getOrElse(position) { "PROXY" } == "PROXY"
                ) View.VISIBLE else View.GONE
            }
        }
        card.addView(TextView(this).apply {
            text = if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) {
                "Android 8/9 支持当前非应用规则；按应用分流需要 Android 10 或更高版本。"
            } else {
                "应用条件仅适用于 VPN 范围内的流量；共享 UID 按应用组匹配。目标留空可匹配该应用的域名和直接 IP 连接。按规则分流且启用应用条件时，无法识别归属的连接会被拒绝。本机 SOCKS5/HTTP 不识别应用。"
            }
            textSize = 11.5f
            setTextColor(mutedColor)
            setLineSpacing(dp(3).toFloat(), 1f)
            setPadding(0, dp(16), 0, 0)
        })
        root.addView(card)
        val scroll = ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(backgroundColor)
            addView(root)
        }
        return LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(backgroundColor)
            addView(
                scroll,
                LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f),
            )
            addView(fixedSaveBar("保存规则") { saveRule() })
        }
    }

    private fun fixedSaveBar(label: String, onSave: () -> Unit) = LinearLayout(this).apply {
        setPadding(dp(20), dp(10), dp(20), dp(16))
        setBackgroundColor(backgroundColor)
        addView(Button(this@RoutingRuleActivity).apply {
            text = label
            setAllCaps(false)
            setTextColor(Color.WHITE)
            setTypeface(typeface, Typeface.BOLD)
            background = rounded(brandColor, 10)
            setOnClickListener { onSave() }
        }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(48)))
    }

    private fun loadRule(rule: RoutingRuleConfig) {
        name.setText(rule.name)
        enabled.isChecked = rule.enabled
        selectedApplications.clear()
        selectedApplications += rule.applications
        updateApplicationsSummary()
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

    private fun openApplicationSelection() {
        val intent = Intent(this, VpnAppSelectionActivity::class.java)
            .putStringArrayListExtra(
                VpnAppSelectionActivity.EXTRA_SELECTED,
                ArrayList(selectedApplications),
            )
            .putExtra(VpnAppSelectionActivity.EXTRA_TITLE, "选择规则应用")
            .putExtra(
                VpnAppSelectionActivity.EXTRA_SUBTITLE,
                "同一 UID 对应多个包名时会作为同一应用组匹配；应用条件只适用于 VPN 流量。",
            )
        @Suppress("DEPRECATION")
        startActivityForResult(intent, REQUEST_APPLICATIONS)
    }

    private fun updateApplicationsSummary() {
        applicationsSummary.text = when {
            Build.VERSION.SDK_INT < Build.VERSION_CODES.Q && selectedApplications.isNotEmpty() ->
                "当前系统不支持此规则中的 ${selectedApplications.size} 个应用条件"
            Build.VERSION.SDK_INT < Build.VERSION_CODES.Q -> "当前系统不支持按应用分流"
            selectedApplications.isEmpty() -> "未选择应用（匹配全部应用）"
            else -> "已选择 ${selectedApplications.size} 个应用\n" +
                selectedApplications.sorted().take(3).joinToString("\n") +
                if (selectedApplications.size > 3) "\n…" else ""
        }
    }

    private fun populateExits(selectedExitId: String) {
        val ids = mutableListOf("")
        val labels = mutableListOf("跟随默认出口")
        val status = runCatching { JSONObject(RelayExitService.statusJson()) }.getOrNull()
        val local = ConfigStore(this).customExitNames()
        val names = ExitDisplayNames.fromStatus(status, local)
        val available = status?.optJSONArray("proxyExits")
        if (available != null) {
            for (index in 0 until available.length()) {
                val item = available.optJSONObject(index) ?: continue
                val id = item.optString("deviceId").trim()
                if (id.isBlank() || id in ids) continue
                ids += id
                val displayName = ExitDisplayNames.label(id, names)
                val identity = item.optString("identityName")
                val state = if (item.optBoolean("online", false)) "在线" else "离线"
                labels += buildList {
                    add(displayName)
                    if (identity.isNotBlank()) add(identity)
                    add(state)
                }.joinToString(" · ")
            }
        }
        ConfigStore(this).load().customExits.forEach { custom ->
            if (custom.id !in ids) {
                ids += custom.id
                labels += "${custom.name} · 本机 ${custom.protocol.uppercase()} · " +
                    if (custom.enabled) "已启用" else "已停用"
            }
        }
        if (selectedExitId.isNotBlank() && selectedExitId !in ids) {
            ids += selectedExitId
            labels += "${ExitDisplayNames.label(selectedExitId, names)} · 当前不可用"
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
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q &&
            enabled.isChecked && selectedApplications.isNotEmpty()
        ) {
            Toast.makeText(this, "Android 8/9 不能启用包含应用条件的规则", Toast.LENGTH_LONG).show()
            return
        }
        val updated = readRule()
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
        val saveError = runCatching {
            routing = ConfigStore(this).saveRouting(next)
        }.exceptionOrNull()
        if (saveError != null) {
            Toast.makeText(this, saveError.message, Toast.LENGTH_LONG).show()
            return
        }
        reconfigureRuntime()
        Toast.makeText(this, "规则已保存", Toast.LENGTH_SHORT).show()
        finish()
    }

    private fun readRule(): RoutingRuleConfig {
        val current = original ?: RoutingRuleConfig(id = UUID.randomUUID().toString())
        val selectedAction = actionValues.getOrElse(action.selectedItemPosition) { "PROXY" }
        return current.copy(
            name = name.text.toString(),
            enabled = enabled.isChecked,
            action = selectedAction,
            exitId = if (selectedAction == "PROXY") exitIds.getOrElse(exit.selectedItemPosition) { "" } else "",
            applications = selectedApplications.sorted(),
            targets = splitValues(targets.text.toString()),
            ports = splitValues(ports.text.toString()),
            protocols = protocolValues.getOrElse(protocol.selectedItemPosition) { "" }
                .takeIf(String::isNotBlank)?.let(::listOf).orEmpty(),
        )
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
        setHintTextColor(UiPalette.placeholder)
        background = rounded(inputBg, 12, lineColor)
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
        adapter = UiKit.themedSpinnerAdapter(this@RoutingRuleActivity, labels)
        minimumHeight = dp(48)
        setPadding(dp(10), 0, dp(8), 0)
        background = rounded(inputBg, 12, lineColor)
    }

    private fun labeled(label: String, child: View) = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL
        addView(TextView(this@RoutingRuleActivity).apply {
            text = label
            textSize = 12f
            setTextColor(mutedColor)
            setPadding(dp(2), 0, 0, dp(6))
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
