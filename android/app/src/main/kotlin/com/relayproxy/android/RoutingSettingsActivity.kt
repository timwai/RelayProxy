package com.relayproxy.android

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Bundle
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.ArrayAdapter
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.Spinner
import android.widget.TextView
import android.widget.Toast
import com.relayproxy.core.androidcore.Androidcore

class RoutingSettingsActivity : Activity() {
    private lateinit var mode: Spinner
    private lateinit var defaultAction: Spinner
    private var config = RoutingConfig()
    private var draft: RoutingConfig? = null

    private val modeValues = listOf("global_proxy", "rule", "direct")
    private val modeLabels = listOf("全局代理", "按规则分流", "全局直连")
    private val actionValues = listOf("PROXY", "DIRECT", "REJECT")
    private val actionLabels = listOf("代理", "直连", "拒绝")
    private val backgroundColor = Color.rgb(246, 248, 252)
    private val surfaceColor = Color.WHITE
    private val inkColor = Color.rgb(15, 23, 42)
    private val mutedColor = Color.rgb(100, 116, 139)
    private val lineColor = Color.rgb(226, 232, 240)
    private val brandColor = Color.rgb(37, 99, 235)
    private val dangerColor = Color.rgb(220, 38, 38)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        configureWindow()
        draft = savedInstanceState?.getString("routingDraft")?.let(RoutingConfig::fromJson)
    }

    override fun onResume() {
        super.onResume()
        config = draft ?: ConfigStore(this).load().routing
        setContentView(buildUi())
    }

    override fun onPause() {
        captureDraft()
        super.onPause()
    }

    override fun onSaveInstanceState(outState: Bundle) {
        captureDraft()
        draft?.let { outState.putString("routingDraft", it.toJson().toString()) }
        super.onSaveInstanceState(outState)
    }

    private fun captureDraft() {
        if (!::mode.isInitialized) return
        val current = config.copy(
            mode = modeValues.getOrElse(mode.selectedItemPosition) { "global_proxy" },
            defaultAction = actionValues.getOrElse(defaultAction.selectedItemPosition) { "PROXY" },
        )
        if (current != config) draft = current
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
            text = "分流规则"
            textSize = 23f
            setTextColor(inkColor)
            setTypeface(typeface, Typeface.BOLD)
        })
        root.addView(TextView(this).apply {
            text = "规则从上到下匹配；同一字段内任意值命中，不同字段需同时命中。"
            textSize = 12.5f
            setTextColor(mutedColor)
            setPadding(0, dp(5), 0, dp(12))
        })

        val policy = card()
        mode = spinner(modeLabels)
        defaultAction = spinner(actionLabels)
        policy.addView(labeled("模式", mode))
        policy.addView(labeled("未命中时", defaultAction), topMargin(12))
        mode.setSelection(modeValues.indexOf(config.mode).coerceAtLeast(0), false)
        defaultAction.setSelection(actionValues.indexOf(config.defaultAction).coerceAtLeast(0), false)
        root.addView(policy)

        root.addView(TextView(this).apply {
            text = "规则列表"
            textSize = 16f
            setTextColor(inkColor)
            setTypeface(typeface, Typeface.BOLD)
            setPadding(dp(2), dp(20), 0, dp(3))
        })
        if (config.rules.isEmpty()) {
            root.addView(TextView(this).apply {
                text = "暂无规则。全局代理模式保持现有行为；切换到按规则分流后，可添加多条规则。"
                textSize = 12.5f
                setTextColor(mutedColor)
                setPadding(dp(14), dp(14), dp(14), dp(14))
                background = rounded(surfaceColor, 14, lineColor)
            }, topMargin(8))
        } else {
            config.rules.forEachIndexed { index, rule ->
                root.addView(ruleCard(index, rule), topMargin(8))
            }
        }

        root.addView(Button(this).apply {
            text = "添加规则"
            setAllCaps(false)
            setTextColor(brandColor)
            background = rounded(Color.WHITE, 14, Color.rgb(191, 219, 254))
            setOnClickListener {
                if (!persistPolicy(showToast = false)) return@setOnClickListener
                startActivity(Intent(this@RoutingSettingsActivity, RoutingRuleActivity::class.java))
            }
        }, buttonParams(16))
        root.addView(Button(this).apply {
            text = "保存分流设置"
            setAllCaps(false)
            setTextColor(Color.WHITE)
            setTypeface(typeface, Typeface.BOLD)
            background = rounded(brandColor, 14)
            setOnClickListener { persistPolicy(showToast = true) }
        }, buttonParams(10))

        return ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(backgroundColor)
            addView(root)
        }
    }

    private fun ruleCard(index: Int, rule: RoutingRuleConfig): View = card().apply {
        val heading = LinearLayout(this@RoutingSettingsActivity).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        heading.addView(TextView(this@RoutingSettingsActivity).apply {
            text = rule.name.ifBlank { "规则 ${index + 1}" }
            textSize = 15f
            setTextColor(if (rule.enabled) inkColor else mutedColor)
            setTypeface(typeface, Typeface.BOLD)
        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        heading.addView(TextView(this@RoutingSettingsActivity).apply {
            text = if (rule.enabled) actionLabel(rule.action) else "已停用"
            textSize = 12f
            setTextColor(if (rule.action == "REJECT") dangerColor else brandColor)
        })
        addView(heading)
        addView(TextView(this@RoutingSettingsActivity).apply {
            text = ruleSummary(rule)
            textSize = 12f
            setTextColor(mutedColor)
            setPadding(0, dp(6), 0, dp(10))
        })

        val actions = LinearLayout(this@RoutingSettingsActivity).apply {
            orientation = LinearLayout.HORIZONTAL
        }
        actions.addView(textAction("编辑") {
            if (!persistPolicy(showToast = false)) return@textAction
            startActivity(
                Intent(this@RoutingSettingsActivity, RoutingRuleActivity::class.java)
                    .putExtra(RoutingRuleActivity.EXTRA_RULE_ID, rule.id)
            )
        }, actionParams())
        actions.addView(textAction("复制") { duplicateRule(index) }, actionParams())
        actions.addView(textAction("上移") { moveRule(index, -1) }, actionParams())
        actions.addView(textAction("下移") { moveRule(index, 1) }, actionParams())
        actions.addView(textAction("删除", dangerColor) { confirmDelete(index) }, actionParams())
        addView(actions)
    }

    private fun persistPolicy(showToast: Boolean): Boolean {
        val updated = config.copy(
            mode = modeValues.getOrElse(mode.selectedItemPosition) { "global_proxy" },
            defaultAction = actionValues.getOrElse(defaultAction.selectedItemPosition) { "PROXY" },
        )
        if (!save(updated)) return false
        if (showToast) Toast.makeText(this, "分流设置已保存", Toast.LENGTH_SHORT).show()
        return true
    }

    private fun duplicateRule(index: Int) {
        val source = config.rules.getOrNull(index) ?: return
        val rules = config.rules.toMutableList()
        rules.add(index + 1, source.copy(id = java.util.UUID.randomUUID().toString(), name = "${source.name} 副本"))
        if (save(config.copy(rules = rules))) recreate()
    }

    private fun moveRule(index: Int, offset: Int) {
        val target = index + offset
        if (index !in config.rules.indices || target !in config.rules.indices) return
        val rules = config.rules.toMutableList()
        val item = rules.removeAt(index)
        rules.add(target, item)
        if (save(config.copy(rules = rules))) recreate()
    }

    private fun confirmDelete(index: Int) {
        val rule = config.rules.getOrNull(index) ?: return
        AlertDialog.Builder(this)
            .setTitle("删除规则")
            .setMessage("确定删除“${rule.name}”吗？")
            .setNegativeButton("取消", null)
            .setPositiveButton("删除") { _, _ ->
                if (save(config.copy(rules = config.rules.filterIndexed { i, _ -> i != index }))) recreate()
            }
            .show()
    }

    private fun save(updated: RoutingConfig): Boolean {
        val next = updated.copy(
            mode = modeValues.getOrElse(mode.selectedItemPosition) { "global_proxy" },
            defaultAction = actionValues.getOrElse(defaultAction.selectedItemPosition) { "PROXY" },
        )
        val error = runCatching {
            require(Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q ||
                next.rules.none { it.enabled && it.applications.isNotEmpty() }
            ) { "Android 8/9 不能启用包含应用条件的规则" }
            Androidcore.validateRoutingConfig(next.toJson(forCore = true).toString())
            config = ConfigStore(this).saveRouting(next)
            draft = null
        }.exceptionOrNull()
        if (error != null) {
            Toast.makeText(this, error.message ?: "保存规则失败", Toast.LENGTH_LONG).show()
            return false
        }
        reconfigureRuntime()
        return true
    }

    private fun reconfigureRuntime() {
        val store = ConfigStore(this)
        val current = store.load()
        if (store.isDesiredRunning() || store.isVpnDesiredRunning() || current.clientEnabled) {
            val relay = Intent(this, RelayExitService::class.java).setAction(RelayExitService.ACTION_RECONFIGURE)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) startForegroundService(relay) else startService(relay)
        }
    }

    private fun ruleSummary(rule: RoutingRuleConfig): String = buildList {
        if (rule.applications.isNotEmpty()) add("应用 ${rule.applications.size} 个")
        if (rule.targets.isNotEmpty()) add("目标 ${rule.targets.joinToString(", ")}")
        if (rule.ports.isNotEmpty()) add("端口 ${rule.ports.joinToString(", ")}")
        if (rule.protocols.isNotEmpty()) add(rule.protocols.joinToString("/").uppercase())
        if (rule.action == "PROXY") {
            add(if (rule.exitId.isBlank()) "跟随默认出口" else "出口 ${rule.exitId.take(12)}")
        }
    }.joinToString(" · ").ifBlank { "匹配全部流量" }

    private fun actionLabel(action: String): String = when (action) {
        "DIRECT" -> "直连"
        "REJECT" -> "拒绝"
        else -> "代理"
    }

    private fun card() = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL
        setPadding(dp(16), dp(16), dp(16), dp(16))
        background = rounded(surfaceColor, 16, lineColor)
    }

    private fun spinner(labels: List<String>) = Spinner(this).apply {
        adapter = ArrayAdapter(this@RoutingSettingsActivity, android.R.layout.simple_spinner_item, labels)
            .also { it.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item) }
        minimumHeight = dp(48)
        setPadding(dp(10), 0, dp(8), 0)
        background = rounded(Color.rgb(248, 250, 252), 12, lineColor)
    }

    private fun labeled(label: String, child: View) = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL
        addView(TextView(this@RoutingSettingsActivity).apply {
            text = label
            textSize = 12f
            setTextColor(mutedColor)
            setPadding(dp(2), 0, 0, dp(5))
        })
        addView(child)
    }

    private fun textAction(label: String, color: Int = brandColor, click: () -> Unit) = TextView(this).apply {
        text = label
        textSize = 12f
        gravity = Gravity.CENTER
        setTextColor(color)
        setOnClickListener { click() }
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

    private fun buttonParams(top: Int) = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        dp(50),
    ).apply { topMargin = dp(top) }

    private fun actionParams() = LinearLayout.LayoutParams(0, dp(36), 1f)

    private fun dp(value: Int) = (value * resources.displayMetrics.density + 0.5f).toInt()
}
