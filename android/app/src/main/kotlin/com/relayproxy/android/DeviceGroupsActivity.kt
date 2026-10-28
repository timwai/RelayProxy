package com.relayproxy.android

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.widget.*
import org.json.JSONObject

class DeviceGroupsActivity : Activity() {
    private lateinit var policy: RoutingConfig
    private val store get() = ConfigStore(this)
    override fun onCreate(savedInstanceState: Bundle?) { super.onCreate(savedInstanceState); render() }
    private fun notifyRuntime() {
        if (store.isDesiredRunning() || store.isVpnDesiredRunning() || store.load().clientEnabled) {
            val i = Intent(this, RelayExitService::class.java).setAction(RelayExitService.ACTION_RECONFIGURE)
            if (Build.VERSION.SDK_INT >= 26) startForegroundService(i) else startService(i)
        }
        if (store.isVpnDesiredRunning()) {
            val i = Intent(this, RelayVpnService::class.java).setAction(RelayVpnService.ACTION_RECONFIGURE)
            if (Build.VERSION.SDK_INT >= 26) startForegroundService(i) else startService(i)
        }
    }
    private fun save(groups: List<DeviceGroupConfig>): Boolean = try {
        policy = store.saveRouting(policy.copy(deviceGroups = groups)); notifyRuntime(); render(); true
    } catch (e: Exception) { Toast.makeText(this, e.message, Toast.LENGTH_LONG).show(); false }
    private fun render() {
        policy = store.load().routing
        val body = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(24,24,24,24) }
        body.addView(Button(this).apply { text = "返回"; setOnClickListener { finish() } })
        body.addView(TextView(this).apply { text = "设备分组"; textSize = 24f })
        body.addView(TextView(this).apply { text = "按顺序选择首个在线且已授权的成员；建立连接失败时尝试下一成员。已有连接不迁移，主设备恢复后新连接优先使用主设备。" })
        body.addView(Button(this).apply { text = "新增分组"; setOnClickListener { edit(DeviceGroupConfig()) } })
        policy.deviceGroups.forEach { group ->
            body.addView(TextView(this).apply { text = group.name + "\n" + group.members.joinToString(" → "); setPadding(0,24,0,0) })
            body.addView(Button(this).apply { text = "编辑"; setOnClickListener { edit(group) } })
            body.addView(Button(this).apply { text = "删除"; setOnClickListener { save(policy.deviceGroups.filter { it.id != group.id }) } })
        }
        setContentView(ScrollView(this).apply { addView(body) })
    }
    private fun edit(original: DeviceGroupConfig) {
        val members = original.members.toMutableList()
        val status = runCatching { JSONObject(RelayExitService.statusJson()) }.getOrNull()
        val names = ExitDisplayNames.fromStatus(status, store.customExitNames())
        val available = status?.optJSONArray("proxyExits")
        val devices = buildList { if (available != null) for (i in 0 until available.length()) { val e = available.optJSONObject(i) ?: continue; val id = e.optString("deviceId"); if (id.isNotBlank()) add(id) } }
        val body = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(24,8,24,8) }
        val name = EditText(this).apply { hint = "分组名称"; setText(original.name) }; body.addView(name)
        val rows = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }; body.addView(rows)
        fun redraw() {
            rows.removeAllViews()
            members.forEachIndexed { index, id ->
                val row = LinearLayout(this)
                row.addView(TextView(this).apply { text = "${index+1}. ${ExitDisplayNames.label(id,names)}" }, LinearLayout.LayoutParams(0,-2,1f))
                listOf(-1,1).forEach { delta -> row.addView(Button(this).apply { text = if (delta < 0) "↑" else "↓"; isEnabled = index+delta in members.indices; setOnClickListener { java.util.Collections.swap(members,index,index+delta); redraw() } }, LinearLayout.LayoutParams(64,-2)) }
                row.addView(Button(this).apply { text = "×"; setOnClickListener { members.remove(id); redraw() } }, LinearLayout.LayoutParams(64,-2)); rows.addView(row)
            }
        }
        redraw()
        body.addView(Button(this).apply { text = "添加设备"; setOnClickListener {
            val candidates = devices.filter { it !in members }; if (candidates.isEmpty()) { Toast.makeText(this@DeviceGroupsActivity,"暂无可添加的已授权设备",Toast.LENGTH_SHORT).show() } else {
                AlertDialog.Builder(this@DeviceGroupsActivity).setTitle("选择设备").setItems(candidates.map { ExitDisplayNames.label(it,names) }.toTypedArray()) { _, i -> members.add(candidates[i]); redraw() }.show()
            }
        } })
        val dialog = AlertDialog.Builder(this).setTitle("设备分组").setView(ScrollView(this).apply { addView(body) }).setNegativeButton("取消",null).setPositiveButton("保存",null).create()
        dialog.setOnShowListener { dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener {
            val updated = original.copy(name=name.text.toString().trim(),members=members.toList())
            val groups = if (policy.deviceGroups.any { it.id == updated.id }) policy.deviceGroups.map { if (it.id == updated.id) updated else it } else policy.deviceGroups + updated
            if(save(groups)) dialog.dismiss()
        } }; dialog.show()
    }
}
