package com.relayproxy.android

import android.app.Activity
import android.app.AlertDialog
import android.content.ClipData
import android.content.Intent
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Bundle
import android.view.DragEvent
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.ArrayAdapter
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.Spinner
import android.widget.Switch
import android.widget.TextView
import android.widget.Toast
import android.widget.EditText
import org.json.JSONObject
import com.relayproxy.core.androidcore.Androidcore

class RoutingSettingsActivity : Activity() {
    private data class RuleDragToken(val ruleId: String)

    private lateinit var mode: Spinner
    private lateinit var defaultAction: Spinner
    private var config = RoutingConfig()

    private val modeValues = listOf("global_proxy", "rule", "direct")
    private val modeLabels = listOf("全局代理", "按规则分流", "全局直连")
    private val actionValues = listOf("PROXY", "DIRECT", "REJECT")
    private val actionLabels = listOf("代理", "直连", "拒绝")
    private val backgroundColor get() = UiPalette.bg
    private val surfaceColor get() = UiPalette.surface
    private val inputBg get() = UiPalette.inputBg
    private val inkColor get() = UiPalette.ink
    private val mutedColor get() = UiPalette.muted
    private val lineColor get() = UiPalette.line
    private val brandColor get() = UiPalette.brand
    private val dangerColor get() = UiPalette.danger

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        UiPalette.sync(this)
        setTheme(if (UiPalette.isDark) R.style.Theme_RelayProxy_Dark else R.style.Theme_RelayProxy_Light)
        configureWindow()
    }

    override fun onResume() {
        super.onResume()
        config = ConfigStore(this).load().routing
        setContentView(buildUi())
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
            text = "分流规则"
            textSize = 23f
            setTextColor(inkColor)
            setTypeface(typeface, Typeface.BOLD)
        })
        root.addView(TextView(this).apply {
            text = "规则从上到下匹配；长按规则的拖动区域可调整顺序。启用、删除和排序会立即保存。"
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

        if (config.rules.isNotEmpty() && config.mode != "rule") {
            root.addView(TextView(this).apply {
                text = if (config.mode == "direct") {
                    "当前是“全局直连”模式，下面的分流规则不会参与匹配。切换到“按规则分流”并保存后才会生效。"
                } else {
                    "当前是“全局代理”模式，下面的分流规则不会参与匹配，所有代理流量会使用默认出口。切换到“按规则分流”并保存后才会生效。"
                }
                textSize = 12.5f
                setTextColor(dangerColor)
                setPadding(dp(14), dp(12), dp(14), dp(12))
                background = rounded(Color.rgb(254, 242, 242), 14, Color.rgb(254, 202, 202))
            }, topMargin(12))
        }

        root.addView(TextView(this).apply {
            text = "规则列表"
            textSize = 16f
            setTextColor(inkColor)
            setTypeface(typeface, Typeface.BOLD)
            setPadding(dp(2), dp(20), 0, dp(3))
        })
        if (config.rules.isEmpty()) {
            root.addView(TextView(this).apply {
                text = "暂无规则。添加第一条规则时会自动切换到“按规则分流”。"
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
            text = "+ 添加规则"
            setAllCaps(false)
            setTextColor(brandColor)
            setTypeface(typeface, Typeface.BOLD)
            background = rounded(if (UiPalette.isDark) UiPalette.brandSoft else Color.WHITE, 8, if (UiPalette.isDark) UiPalette.brandSoftBorder else Color.rgb(191, 219, 254))
            setOnClickListener {
                if (config.rules.isEmpty() &&
                    modeValues.getOrElse(mode.selectedItemPosition) { "global_proxy" } != "rule"
                ) {
                    mode.setSelection(modeValues.indexOf("rule"), false)
                }
                if (!persistPolicy(showToast = false)) return@setOnClickListener
                startActivity(Intent(this@RoutingSettingsActivity, RoutingRuleActivity::class.java))
            }
        }, buttonParams(16))
        root.addView(TextView(this).apply {
            text = "规则订阅"
            textSize = 16f
            setTextColor(inkColor)
            setTypeface(typeface, Typeface.BOLD)
            setPadding(dp(2), dp(22), 0, dp(6))
        })
        root.addView(TextView(this).apply {
            text = "手动规则优先，然后按订阅顺序匹配，最后执行未命中动作。支持 GFWList Base64 和纯域名列表；每 6 小时自动更新，失败时保留缓存。"
            textSize = 12f
            setTextColor(mutedColor)
        })
        config.subscriptions.forEachIndexed { index, sub ->
            root.addView(card().apply {
                addView(TextView(this@RoutingSettingsActivity).apply {
                    text = sub.name + if (sub.enabled) " · 启用" else " · 停用"
                    textSize = 15f
                    setTypeface(typeface, Typeface.BOLD)
                    setTextColor(inkColor)
                })
                addView(TextView(this@RoutingSettingsActivity).apply {
                    text = sub.url + "\n" + actionLabel(sub.action) +
                        if (sub.exitId.isBlank()) " · 跟随默认出口" else " · 指定出口"
                    textSize = 11.5f
                    setTextColor(mutedColor)
                    setPadding(0, dp(8), 0, dp(10))
                })
                val actions = LinearLayout(this@RoutingSettingsActivity).apply {
                    orientation = LinearLayout.HORIZONTAL
                }
                actions.addView(textAction("编辑") { editSubscription(index) }, actionParams())
                actions.addView(textAction(if (sub.enabled) "停用" else "启用") {
                    val base = currentPolicy()
                    val next = base.subscriptions.mapIndexed { i, x ->
                        if (i == index) x.copy(enabled = !x.enabled) else x
                    }
                    if (save(base.copy(subscriptions = next))) renderCurrent()
                }, actionParams())
                actions.addView(textAction("上移") { moveSubscription(index, -1) }, actionParams())
                actions.addView(textAction("下移") { moveSubscription(index, 1) }, actionParams())
                actions.addView(textAction("删除", dangerColor) {
                    UiKit.alertDialog(this@RoutingSettingsActivity, "删除订阅", "确定删除“${sub.name}”？")
                        .setNegativeButton("取消", null)
                        .setPositiveButton("删除") { _, _ ->
                            val base = currentPolicy()
                            if (save(base.copy(subscriptions = base.subscriptions.filterIndexed { i, _ -> i != index }))) renderCurrent()
                        }.show()
                }, actionParams())
                addView(actions)
            }, topMargin(8))
        }
        root.addView(Button(this).apply {
            text = "+ 添加订阅"
            setAllCaps(false)
            setTextColor(brandColor)
            background = rounded(if (UiPalette.isDark) UiPalette.brandSoft else Color.WHITE, 8, lineColor)
            setOnClickListener { editSubscription(-1) }
        }, buttonParams(12))
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
            addView(fixedSaveBar("保存分流设置") { persistPolicy(showToast = true) })
        }
    }

    private fun moveSubscription(index: Int, direction: Int) {
        val base = currentPolicy()
        val other = index + direction
        if (index !in base.subscriptions.indices || other !in base.subscriptions.indices) return
        val next = base.subscriptions.toMutableList()
        val item = next.removeAt(index)
        next.add(other, item)
        if (save(base.copy(subscriptions = next))) renderCurrent()
    }

    private fun editSubscription(index: Int) {
        val current = config.subscriptions.getOrNull(index) ?: RoutingSubscriptionConfig()
        val fields = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(18), dp(12), dp(18), dp(4))
        }
        val name = EditText(this).apply {
            setSingleLine(true)
            setText(current.name)
            hint = "名称"
        }
        val url = EditText(this).apply {
            setSingleLine(true)
            setText(current.url)
            hint = "HTTPS 订阅 URL"
            inputType = android.text.InputType.TYPE_CLASS_TEXT or android.text.InputType.TYPE_TEXT_VARIATION_URI
        }
        val action = spinner(actionLabels)
        action.setSelection(actionValues.indexOf(current.action).coerceAtLeast(0))
        val exitIDs = mutableListOf("")
        val exitLabels = mutableListOf("跟随默认出口")
        val status = runCatching { JSONObject(RelayExitService.statusJson()) }.getOrNull()
        val names = ExitDisplayNames.fromStatus(status, ConfigStore(this).customExitNames())
        status?.optJSONArray("proxyExits")?.let { available ->
            for (i in 0 until available.length()) {
                val item = available.optJSONObject(i) ?: continue
                val id = item.optString("deviceId")
                if (id.isBlank() || id in exitIDs) continue
                exitIDs.add(id)
                exitLabels.add(ExitDisplayNames.label(id, names))
            }
        }
        ConfigStore(this).load().customExits.forEach { item ->
            if (item.id !in exitIDs) {
                exitIDs.add(item.id)
                exitLabels.add(item.name)
            }
        }
        if (current.exitId.isNotBlank() && current.exitId !in exitIDs) {
            exitIDs.add(current.exitId)
            exitLabels.add(ExitDisplayNames.label(current.exitId, names) + "（不可用）")
        }
        val exit = spinner(exitLabels)
        exit.setSelection(exitIDs.indexOf(current.exitId).coerceAtLeast(0))
        val enabled = Switch(this).apply {
            text = "启用此订阅"
            isChecked = current.enabled
            UiKit.styleSwitch(this)
        }
        fields.addView(labeled("订阅名称", name))
        fields.addView(labeled("订阅 URL", url), topMargin(8))
        fields.addView(labeled("匹配动作", action), topMargin(8))
        fields.addView(labeled("指定代理出口（仅 PROXY 使用）", exit), topMargin(8))
        fields.addView(enabled, topMargin(10))
        val dialog = UiKit.alertDialog(this, if (index < 0) "添加规则订阅" else "编辑规则订阅", "仅允许公开 HTTPS 订阅地址")
            .setView(fields)
            .setNegativeButton("取消", null)
            .setPositiveButton("保存", null)
            .create()
        dialog.setOnShowListener {
            dialog.getButton(android.app.AlertDialog.BUTTON_POSITIVE).setOnClickListener {
                val base = currentPolicy()
                val newItem = RoutingSubscriptionConfig(
                    name = name.text.toString().trim(),
                    url = url.text.toString().trim(),
                    enabled = enabled.isChecked,
                    action = actionValues.getOrElse(action.selectedItemPosition) { "PROXY" },
                    exitId = if (actionValues.getOrElse(action.selectedItemPosition) { "PROXY" } == "PROXY")
                        exitIDs.getOrElse(exit.selectedItemPosition) { "" } else "",
                )
                val list = base.subscriptions.toMutableList()
                if (index < 0) list.add(newItem) else list[index] = newItem
                if (list.count { it.url == newItem.url } > 1) {
                    Toast.makeText(this, "订阅地址不能重复", Toast.LENGTH_SHORT).show()
                    return@setOnClickListener
                }
                if (save(base.copy(subscriptions = list))) {
                    dialog.dismiss()
                    renderCurrent()
                }
            }
        }
        dialog.show()
    }

    private fun fixedSaveBar(label: String, onSave: () -> Unit) = LinearLayout(this).apply {
        setPadding(dp(20), dp(10), dp(20), dp(16))
        setBackgroundColor(backgroundColor)
        addView(Button(this@RoutingSettingsActivity).apply {
            text = label
            setAllCaps(false)
            setTextColor(Color.WHITE)
            setTypeface(typeface, Typeface.BOLD)
            background = rounded(brandColor, 8)
            setOnClickListener { onSave() }
        }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(52)))
    }

    private fun ruleCard(index: Int, rule: RoutingRuleConfig): View = card().apply {
        val targetCard = this
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
        heading.addView(Switch(this@RoutingSettingsActivity).apply {
            text = if (rule.enabled) "启用" else "停用"
            textSize = 12f
            setTextColor(if (rule.enabled) brandColor else mutedColor)
            isChecked = rule.enabled
            setOnCheckedChangeListener { _, checked ->
                if (checked != rule.enabled) setRuleEnabled(rule.id, checked)
            }
        })
        addView(heading)
        addView(TextView(this@RoutingSettingsActivity).apply {
            text = ruleSummary(rule)
            textSize = 12f
            setTextColor(mutedColor)
            setPadding(0, dp(6), 0, dp(8))
        })

        addView(TextView(this@RoutingSettingsActivity).apply {
            text = "☰  长按拖动排序"
            textSize = 12f
            gravity = Gravity.CENTER_VERTICAL
            setTextColor(brandColor)
            setPadding(dp(4), dp(8), dp(4), dp(8))
            contentDescription = "拖动${rule.name.ifBlank { "规则 ${index + 1}" }}调整顺序"
            setOnLongClickListener { view ->
                view.startDragAndDrop(
                    ClipData.newPlainText("relayproxy-routing-rule", rule.id),
                    View.DragShadowBuilder(targetCard),
                    RuleDragToken(rule.id),
                    0,
                )
                true
            }
        })

        setOnDragListener { view, event ->
            val token = event.localState as? RuleDragToken
            when (event.action) {
                DragEvent.ACTION_DRAG_STARTED -> token != null
                DragEvent.ACTION_DRAG_ENTERED -> {
                    if (token != null && token.ruleId != rule.id) view.alpha = 0.72f
                    true
                }
                DragEvent.ACTION_DRAG_EXITED -> {
                    view.alpha = 1f
                    true
                }
                DragEvent.ACTION_DROP -> {
                    view.alpha = 1f
                    if (token == null || token.ruleId == rule.id) {
                        true
                    } else {
                        moveRuleByDrag(
                            sourceRuleId = token.ruleId,
                            targetRuleId = rule.id,
                            placeAfter = event.y > view.height / 2f,
                        )
                    }
                }
                DragEvent.ACTION_DRAG_ENDED -> {
                    view.alpha = 1f
                    true
                }
                else -> true
            }
        }

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
        actions.addView(textAction("复制") { duplicateRule(rule.id) }, actionParams())
        actions.addView(textAction("上移") { moveRule(rule.id, -1) }, actionParams())
        actions.addView(textAction("下移") { moveRule(rule.id, 1) }, actionParams())
        actions.addView(textAction("删除", dangerColor) { confirmDelete(rule.id) }, actionParams())
        addView(actions)
    }

    private fun currentPolicy(): RoutingConfig = config.copy(
        mode = if (::mode.isInitialized) {
            modeValues.getOrElse(mode.selectedItemPosition) { config.mode }
        } else {
            config.mode
        },
        defaultAction = if (::defaultAction.isInitialized) {
            actionValues.getOrElse(defaultAction.selectedItemPosition) { config.defaultAction }
        } else {
            config.defaultAction
        },
    )

    private fun persistPolicy(showToast: Boolean): Boolean {
        if (!save(currentPolicy())) return false
        if (showToast) Toast.makeText(this, "分流设置已保存", Toast.LENGTH_SHORT).show()
        return true
    }

    private fun setRuleEnabled(ruleId: String, enabled: Boolean) {
        val base = currentPolicy()
        val rules = base.rules.map { rule ->
            if (rule.id == ruleId) rule.copy(enabled = enabled) else rule
        }
        if (save(base.copy(rules = rules))) renderCurrent()
    }

    private fun duplicateRule(ruleId: String) {
        val base = currentPolicy()
        val index = base.rules.indexOfFirst { it.id == ruleId }
        if (index < 0) return
        val source = base.rules[index]
        val rules = base.rules.toMutableList()
        rules.add(
            index + 1,
            source.copy(
                id = java.util.UUID.randomUUID().toString(),
                name = "${source.name} 副本",
            ),
        )
        if (save(base.copy(rules = rules))) renderCurrent()
    }

    private fun moveRule(ruleId: String, offset: Int) {
        val base = currentPolicy()
        val index = base.rules.indexOfFirst { it.id == ruleId }
        val target = index + offset
        if (index !in base.rules.indices || target !in base.rules.indices) return
        val rules = base.rules.toMutableList()
        val item = rules.removeAt(index)
        rules.add(target, item)
        if (save(base.copy(rules = rules))) renderCurrent()
    }

    private fun moveRuleByDrag(
        sourceRuleId: String,
        targetRuleId: String,
        placeAfter: Boolean,
    ): Boolean {
        val base = currentPolicy()
        val sourceIndex = base.rules.indexOfFirst { it.id == sourceRuleId }
        val originalTargetIndex = base.rules.indexOfFirst { it.id == targetRuleId }
        if (sourceIndex < 0 || originalTargetIndex < 0 || sourceIndex == originalTargetIndex) return true

        val rules = base.rules.toMutableList()
        val moved = rules.removeAt(sourceIndex)
        var targetIndex = rules.indexOfFirst { it.id == targetRuleId }
        if (targetIndex < 0) return true
        if (placeAfter) targetIndex++
        rules.add(targetIndex.coerceIn(0, rules.size), moved)
        if (rules == base.rules) return true
        if (save(base.copy(rules = rules))) renderCurrent()
        return true
    }

    private fun confirmDelete(ruleId: String) {
        val rule = config.rules.firstOrNull { it.id == ruleId } ?: return
        UiKit.alertDialog(this, "删除规则", "确定删除“${rule.name}”吗？")
            .setNegativeButton("取消", null)
            .setPositiveButton("删除") { _, _ ->
                val base = currentPolicy()
                val rules = base.rules.filterNot { it.id == ruleId }
                if (save(base.copy(rules = rules))) renderCurrent()
            }
            .show()
    }

    private fun save(next: RoutingConfig): Boolean {
        val error = runCatching {
            require(
                Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q ||
                    next.rules.none { it.enabled && it.applications.isNotEmpty() }
            ) { "Android 8/9 不能启用包含应用条件的规则" }
            Androidcore.validateRoutingConfig(next.toJson(forCore = true).toString())
            config = ConfigStore(this).saveRouting(next)
        }.exceptionOrNull()
        if (error != null) {
            Toast.makeText(this, error.message ?: "保存规则失败", Toast.LENGTH_LONG).show()
            return false
        }
        reconfigureRuntime()
        return true
    }

    private fun renderCurrent() {
        setContentView(buildUi())
    }

    private fun reconfigureRuntime() {
        val store = ConfigStore(this)
        val current = store.load()
        if (store.isDesiredRunning() || store.isVpnDesiredRunning() || current.clientEnabled) {
            val relay = Intent(this, RelayExitService::class.java)
                .setAction(RelayExitService.ACTION_RECONFIGURE)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                startForegroundService(relay)
            } else {
                startService(relay)
            }
        }
        if (store.isVpnDesiredRunning()) {
            val vpn = Intent(this, RelayVpnService::class.java)
                .setAction(RelayVpnService.ACTION_RECONFIGURE)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                startForegroundService(vpn)
            } else {
                startService(vpn)
            }
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
        adapter = UiKit.themedSpinnerAdapter(this@RoutingSettingsActivity, labels)
        minimumHeight = dp(48)
        setPadding(dp(10), 0, dp(8), 0)
        background = rounded(inputBg, 12, lineColor)
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
