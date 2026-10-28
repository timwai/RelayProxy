package com.relayproxy.android

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.graphics.Typeface
import android.os.Build
import android.os.Bundle
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import org.json.JSONObject

/**
 * 设备分组管理：与「分流规则」页共用同一套卡片 / 文本操作 / 对话框样式。
 * 分组按成员顺序故障切换，页面只负责编辑成员与顺序，运行时选择由核心完成。
 */
class DeviceGroupsActivity : Activity() {
    private lateinit var policy: RoutingConfig
    private val store get() = ConfigStore(this)

    private val backgroundColor get() = UiPalette.bg
    private val surfaceColor get() = UiPalette.surface
    private val inkColor get() = UiPalette.ink
    private val mutedColor get() = UiPalette.muted
    private val lineColor get() = UiPalette.line
    private val brandColor get() = UiPalette.brand
    private val dangerColor get() = UiPalette.danger

    private data class Inventory(
        val names: Map<String, String>,
        val online: Map<String, Boolean>,
        val devices: List<String>,
    )

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        UiPalette.sync(this)
        setTheme(if (UiPalette.isDark) R.style.Theme_RelayProxy_Dark else R.style.Theme_RelayProxy_Light)
        configureWindow()
    }

    override fun onResume() {
        super.onResume()
        render()
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

    // ---------------------------------------------------------------- 数据

    private fun inventory(): Inventory {
        val status = runCatching { JSONObject(RelayExitService.statusJson()) }.getOrNull()
        val names = ExitDisplayNames.fromStatus(status, store.customExitNames())
        val online = mutableMapOf<String, Boolean>()
        val devices = mutableListOf<String>()
        val exits = status?.optJSONArray("proxyExits")
        if (exits != null) {
            for (i in 0 until exits.length()) {
                val item = exits.optJSONObject(i) ?: continue
                val id = item.optString("deviceId").trim()
                if (id.isBlank()) continue
                devices.add(id)
                online[id] = item.optBoolean("online", true)
            }
        }
        return Inventory(names, online, devices)
    }

    /** 被默认出口或任一分流规则引用的分组不可删除。 */
    private fun referencedBy(groupId: String): List<String> = buildList {
        if (store.load().defaultExitId == groupId) add("默认出口")
        policy.rules.filter { it.exitId == groupId }.forEach { add("规则「${it.name.ifBlank { "未命名" }}」") }
    }

    private fun notifyRuntime() {
        val current = store.load()
        if (store.isDesiredRunning() || store.isVpnDesiredRunning() || current.clientEnabled) {
            val i = Intent(this, RelayExitService::class.java).setAction(RelayExitService.ACTION_RECONFIGURE)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) startForegroundService(i) else startService(i)
        }
        if (store.isVpnDesiredRunning()) {
            val i = Intent(this, RelayVpnService::class.java).setAction(RelayVpnService.ACTION_RECONFIGURE)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) startForegroundService(i) else startService(i)
        }
    }

    private fun save(groups: List<DeviceGroupConfig>): Boolean {
        val error = runCatching {
            policy = store.saveRouting(policy.copy(deviceGroups = groups))
        }.exceptionOrNull()
        if (error != null) {
            Toast.makeText(this, error.message ?: "保存分组失败", Toast.LENGTH_LONG).show()
            return false
        }
        notifyRuntime()
        render()
        return true
    }

    // ---------------------------------------------------------------- 页面

    private fun render() {
        policy = store.load().routing
        val inv = inventory()
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(20), dp(30), dp(20), dp(36))
            setBackgroundColor(backgroundColor)
        }
        root.addView(TextView(this).apply {
            text = "设备分组"
            textSize = 23f
            setTextColor(inkColor)
            setTypeface(typeface, Typeface.BOLD)
        })
        root.addView(TextView(this).apply {
            text = "按成员顺序选择首个在线且已授权的出口，连接失败时依次尝试下一台。已有连接不迁移；主设备恢复后，新连接优先回到主设备。分组可作为默认出口或分流规则的出口。"
            textSize = 12.5f
            setTextColor(mutedColor)
            setPadding(0, dp(5), 0, dp(12))
        })

        root.addView(TextView(this).apply {
            text = "分组列表"
            textSize = 16f
            setTextColor(inkColor)
            setTypeface(typeface, Typeface.BOLD)
            setPadding(dp(2), dp(8), 0, dp(3))
        })
        if (policy.deviceGroups.isEmpty()) {
            root.addView(TextView(this).apply {
                text = "暂无分组。新建分组后，可在「网络出口」或分流规则中将其选为出口。"
                textSize = 12.5f
                setTextColor(mutedColor)
                setPadding(dp(14), dp(14), dp(14), dp(14))
                background = rounded(surfaceColor, 14, lineColor)
            }, topMargin(8))
        } else {
            policy.deviceGroups.forEach { group -> root.addView(groupCard(group, inv), topMargin(8)) }
        }

        root.addView(TextView(this).apply {
            text = "+ 新增分组"
            textSize = 14f
            gravity = Gravity.CENTER
            setTextColor(brandColor)
            setTypeface(typeface, Typeface.BOLD)
            background = rounded(UiPalette.brandSoft, 12, UiPalette.brandSoftBorder)
            isClickable = true
            isFocusable = true
            setOnClickListener { edit(DeviceGroupConfig(), inv) }
        }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(48)).apply { topMargin = dp(14) })

        setContentView(ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(backgroundColor)
            addView(root)
        })
    }

    private fun groupCard(group: DeviceGroupConfig, inv: Inventory): View = card().apply {
        val onlineCount = group.members.count { inv.online[it] == true }
        val usable = onlineCount > 0
        val refs = referencedBy(group.id)

        val heading = LinearLayout(this@DeviceGroupsActivity).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        heading.addView(TextView(this@DeviceGroupsActivity).apply {
            text = group.name.ifBlank { "未命名分组" }
            textSize = 15f
            setTextColor(inkColor)
            setTypeface(typeface, Typeface.BOLD)
        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        heading.addView(
            if (usable) {
                UiKit.chip(this@DeviceGroupsActivity, "可用 $onlineCount/${group.members.size}", UiPalette.success, UiPalette.successSoft, UiPalette.successSoftBorder)
            } else {
                UiKit.chip(this@DeviceGroupsActivity, "无在线成员", UiPalette.warning, UiPalette.warningSoft, UiPalette.warningSoftBorder)
            },
        )
        addView(heading)

        addView(TextView(this@DeviceGroupsActivity).apply {
            text = if (group.members.isEmpty()) {
                "尚未添加成员"
            } else {
                group.members.joinToString("  →  ") { ExitDisplayNames.label(it, inv.names) + if (inv.online[it] == false) "（离线）" else "" }
            }
            textSize = 12f
            setTextColor(mutedColor)
            setPadding(0, dp(6), 0, 0)
        })
        if (refs.isNotEmpty()) {
            addView(TextView(this@DeviceGroupsActivity).apply {
                text = "被引用：" + refs.joinToString("、")
                textSize = 11f
                setTextColor(UiPalette.placeholder)
                setPadding(0, dp(4), 0, 0)
            })
        }

        val actions = LinearLayout(this@DeviceGroupsActivity).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.END
            setPadding(0, dp(8), 0, 0)
        }
        actions.addView(textAction("编辑") { edit(group, inv) })
        actions.addView(
            textAction("删除", if (refs.isEmpty()) dangerColor else UiPalette.placeholder) {
                if (refs.isNotEmpty()) {
                    Toast.makeText(this@DeviceGroupsActivity, "分组正在被 ${refs.joinToString("、")} 引用，请先解除引用", Toast.LENGTH_LONG).show()
                } else {
                    confirmDelete(group)
                }
            },
        )
        addView(actions)
    }

    private fun confirmDelete(group: DeviceGroupConfig) {
        UiKit.alertDialog(this, "删除分组", "确定删除“${group.name.ifBlank { "未命名分组" }}”吗？")
            .setNegativeButton("取消", null)
            .setPositiveButton("删除") { _, _ -> save(policy.deviceGroups.filterNot { it.id == group.id }) }
            .show()
    }

    // ---------------------------------------------------------------- 编辑对话框

    private fun edit(original: DeviceGroupConfig, inv: Inventory) {
        val members = original.members.toMutableList()
        val creating = policy.deviceGroups.none { it.id == original.id }

        val form = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(20), dp(12), dp(20), dp(4))
        }
        val name = field("如：家庭主备").apply { setText(original.name) }
        form.addView(labeled("分组名称", name))
        form.addView(TextView(this).apply {
            text = "同名分组不区分大小写，最多 64 个字符。"
            textSize = 11f
            setTextColor(UiPalette.placeholder)
            setPadding(dp(2), dp(4), 0, 0)
        })

        form.addView(TextView(this).apply {
            text = "成员与优先级"
            textSize = 12f
            setTextColor(mutedColor)
            setPadding(dp(2), dp(14), 0, dp(5))
        })
        val rows = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            background = rounded(UiPalette.inputBg, 12, lineColor)
            setPadding(dp(4), 0, dp(4), 0)
        }
        form.addView(rows)
        form.addView(TextView(this).apply {
            text = "从上到下为优先顺序；一台设备可加入多个分组。仅列出 Server 已授权的本机可用出口。"
            textSize = 11f
            setTextColor(UiPalette.placeholder)
            setPadding(dp(2), dp(6), 0, 0)
        })

        fun redraw() {
            rows.removeAllViews()
            if (members.isEmpty()) {
                rows.addView(TextView(this).apply {
                    text = "尚未添加成员，分组保存后不可用。"
                    textSize = 12f
                    setTextColor(mutedColor)
                    gravity = Gravity.CENTER
                    setPadding(dp(8), dp(14), dp(8), dp(14))
                })
                return
            }
            members.forEachIndexed { index, id ->
                val row = LinearLayout(this).apply {
                    orientation = LinearLayout.HORIZONTAL
                    gravity = Gravity.CENTER_VERTICAL
                    minimumHeight = dp(44)
                    setPadding(dp(8), 0, 0, 0)
                }
                row.addView(TextView(this).apply {
                    text = "${index + 1}"
                    textSize = 11f
                    gravity = Gravity.CENTER
                    setTextColor(brandColor)
                    typeface = Typeface.DEFAULT_BOLD
                    background = rounded(UiPalette.brandSoft, 6)
                }, LinearLayout.LayoutParams(dp(22), dp(22)).apply { rightMargin = dp(10) })
                row.addView(TextView(this).apply {
                    text = ExitDisplayNames.label(id, inv.names) + if (inv.online[id] == false) " · 离线" else ""
                    textSize = 13.5f
                    setTextColor(if (inv.online[id] == false) mutedColor else inkColor)
                    maxLines = 1
                    ellipsize = android.text.TextUtils.TruncateAt.END
                }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
                row.addView(iconAction("↑", enabled = index > 0) {
                    java.util.Collections.swap(members, index, index - 1); redraw()
                })
                row.addView(iconAction("↓", enabled = index < members.lastIndex) {
                    java.util.Collections.swap(members, index, index + 1); redraw()
                })
                row.addView(iconAction("×", color = dangerColor) { members.removeAt(index); redraw() })
                rows.addView(row)
                if (index < members.lastIndex) {
                    rows.addView(View(this).apply { setBackgroundColor(lineColor) }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(1)))
                }
            }
        }
        redraw()

        form.addView(TextView(this).apply {
            text = "+ 添加设备"
            textSize = 13f
            gravity = Gravity.CENTER
            setTextColor(brandColor)
            setTypeface(typeface, Typeface.BOLD)
            background = rounded(UiPalette.brandSoft, 10, UiPalette.brandSoftBorder)
            isClickable = true
            isFocusable = true
            setOnClickListener {
                val candidates = inv.devices.filter { it !in members }
                if (candidates.isEmpty()) {
                    Toast.makeText(this@DeviceGroupsActivity, "暂无可添加的已授权设备", Toast.LENGTH_SHORT).show()
                    return@setOnClickListener
                }
                val labels = candidates.map { ExitDisplayNames.label(it, inv.names) + if (inv.online[it] == false) " · 离线" else "" }
                UiKit.alertDialog(this@DeviceGroupsActivity, "选择设备")
                    .setItems(labels.toTypedArray()) { _, i -> members.add(candidates[i]); redraw() }
                    .setNegativeButton("取消", null)
                    .show()
            }
        }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(42)).apply { topMargin = dp(12) })

        val dialog = UiKit.alertDialog(this, if (creating) "新建设备分组" else "编辑设备分组")
            .setView(ScrollView(this).apply { addView(form) })
            .setNegativeButton("取消", null)
            .setPositiveButton("保存", null)
            .create()
        dialog.setOnShowListener {
            dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener {
                val updated = original.copy(name = name.text.toString().trim(), members = members.toList())
                if (updated.name.isBlank()) {
                    name.error = "请输入分组名称"
                    return@setOnClickListener
                }
                val groups = if (creating) policy.deviceGroups + updated else policy.deviceGroups.map { if (it.id == updated.id) updated else it }
                if (save(groups)) dialog.dismiss()
            }
        }
        dialog.show()
    }

    // ---------------------------------------------------------------- 样式工具

    private fun card() = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL
        setPadding(dp(16), dp(16), dp(16), dp(12))
        background = rounded(surfaceColor, 16, lineColor)
    }

    private fun labeled(label: String, child: View) = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL
        addView(TextView(this@DeviceGroupsActivity).apply {
            text = label
            textSize = 12f
            setTextColor(mutedColor)
            setPadding(dp(2), 0, 0, dp(5))
        })
        addView(child)
    }

    private fun field(hintText: String) = EditText(this).apply {
        hint = hintText
        textSize = 14f
        setTextColor(inkColor)
        setHintTextColor(UiPalette.placeholder)
        background = rounded(UiPalette.inputBg, 12, lineColor)
        setPadding(dp(12), 0, dp(12), 0)
        setSingleLine(true)
        minimumHeight = dp(46)
    }

    private fun textAction(label: String, color: Int = brandColor, click: () -> Unit) = TextView(this).apply {
        text = label
        textSize = 12.5f
        gravity = Gravity.CENTER
        setTextColor(color)
        setPadding(dp(14), dp(8), dp(14), dp(8))
        isClickable = true
        isFocusable = true
        setOnClickListener { click() }
    }

    private fun iconAction(glyph: String, enabled: Boolean = true, color: Int = brandColor, click: () -> Unit) = TextView(this).apply {
        text = glyph
        textSize = 15f
        gravity = Gravity.CENTER
        setTextColor(if (enabled) color else UiPalette.placeholder)
        isEnabled = enabled
        isClickable = enabled
        isFocusable = enabled
        alpha = if (enabled) 1f else 0.45f
        contentDescription = when (glyph) { "↑" -> "上移"; "↓" -> "下移"; else -> "移除" }
        setOnClickListener { if (enabled) click() }
        layoutParams = LinearLayout.LayoutParams(dp(40), dp(44))
    }

    private fun rounded(fill: Int, radiusDp: Int, stroke: Int? = null) = UiKit.rounded(this, fill, radiusDp, stroke)

    private fun topMargin(value: Int) = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        ViewGroup.LayoutParams.WRAP_CONTENT,
    ).apply { topMargin = dp(value) }

    private fun dp(value: Int) = UiKit.dp(this, value)
}
