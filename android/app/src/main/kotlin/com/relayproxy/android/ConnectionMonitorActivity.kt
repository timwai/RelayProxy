package com.relayproxy.android

import android.app.Activity
import android.content.pm.ApplicationInfo
import android.graphics.Typeface
import android.graphics.drawable.Drawable
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import org.json.JSONArray
import org.json.JSONObject

class ConnectionMonitorActivity : Activity() {
    private lateinit var summaryText: TextView
    private lateinit var detailText: TextView
    private lateinit var applicationList: LinearLayout
    private val handler = Handler(Looper.getMainLooper())

    private val poll = object : Runnable {
        override fun run() {
            renderSnapshot()
            handler.postDelayed(this, 1_000)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        UiPalette.sync(this)
        setTheme(if (UiPalette.isDark) R.style.Theme_RelayProxy_Dark else R.style.Theme_RelayProxy_Light)
        window.statusBarColor = UiPalette.bg
        window.navigationBarColor = UiPalette.bg
        setContentView(buildUi())
    }

    override fun onResume() {
        super.onResume()
        handler.removeCallbacks(poll)
        handler.post(poll)
    }

    override fun onPause() {
        handler.removeCallbacks(poll)
        super.onPause()
    }

    private fun buildUi(): View {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(UiPalette.bg)
            fitsSystemWindows = true
        }

        val header = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            val p = dp(16)
            setPadding(p, dp(12), p, dp(10))
        }
        header.addView(TextView(this).apply {
            text = "‹"
            textSize = 34f
            setTextColor(UiPalette.ink)
            gravity = Gravity.CENTER
            isClickable = true
            isFocusable = true
            contentDescription = "返回"
            setOnClickListener { finish() }
        }, LinearLayout.LayoutParams(dp(44), dp(44)))

        val titles = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
        }
        titles.addView(TextView(this).apply {
            text = "实时应用监控"
            textSize = 20f
            setTextColor(UiPalette.ink)
            typeface = Typeface.DEFAULT_BOLD
        })
        titles.addView(TextView(this).apply {
            text = "当前通过 RelayProxy 建立网络连接的应用"
            textSize = 11.5f
            setTextColor(UiPalette.muted)
            setPadding(0, dp(2), 0, 0)
        })
        header.addView(titles, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        root.addView(header)

        val content = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            val p = dp(16)
            setPadding(p, dp(4), p, dp(32))
        }

        val summary = UiKit.card(this, paddingDp = 16, radiusDp = 14)
        summaryText = TextView(this).apply {
            text = "实时应用 0 · 活跃连接 0"
            textSize = 15f
            setTextColor(UiPalette.ink)
            typeface = Typeface.DEFAULT_BOLD
        }
        summary.addView(summaryText)
        detailText = TextView(this).apply {
            text = "每秒刷新 · 仅显示当前连接"
            textSize = 11.5f
            setTextColor(UiPalette.muted)
            setPadding(0, dp(5), 0, 0)
        }
        summary.addView(detailText)
        content.addView(summary)

        applicationList = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
        }
        content.addView(
            applicationList,
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ).apply { topMargin = dp(12) },
        )

        content.addView(TextView(this).apply {
            text = "应用归属由 Android VPN 的连接 UID 识别。共享 UID 会作为一组应用显示；Android 10 以下或无法识别的连接会标记为“未知应用”。"
            textSize = 10.5f
            setTextColor(UiPalette.placeholder)
            setLineSpacing(0f, 1.15f)
            setPadding(0, dp(14), 0, 0)
        })

        root.addView(ScrollView(this).apply {
            isFillViewport = true
            addView(content)
        }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
        return root
    }

    private fun renderSnapshot() {
        val snapshot = runCatching { JSONObject(RelayExitService.activeApplicationsJson()) }
            .getOrElse { JSONObject() }
        val applications = snapshot.optJSONArray("applications") ?: JSONArray()
        val activeConnections = snapshot.optInt("activeConnections", 0)
        val trackedConnections = snapshot.optInt("trackedConnections", 0)
        val omitted = snapshot.optLong("omittedConnections", 0)

        summaryText.text = "实时应用 ${applications.length()} · 活跃连接 $activeConnections"
        detailText.text = buildString {
            append("每秒刷新 · 已展示 $trackedConnections 条实时连接")
            if (omitted > 0) append(" · 历史容量省略 $omitted")
        }

        applicationList.removeAllViews()
        if (applications.length() == 0) {
            applicationList.addView(emptyState(activeConnections))
            return
        }
        for (index in 0 until applications.length()) {
            val item = applications.optJSONObject(index) ?: continue
            applicationList.addView(
                applicationCard(item),
                LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT,
                    ViewGroup.LayoutParams.WRAP_CONTENT,
                ).apply {
                    if (index > 0) topMargin = dp(10)
                },
            )
        }
    }

    private fun emptyState(activeConnections: Int): View {
        val card = UiKit.card(this, paddingDp = 24, radiusDp = 14)
        card.gravity = Gravity.CENTER_HORIZONTAL
        card.addView(TextView(this).apply {
            text = if (activeConnections > 0) "正在识别应用…" else "当前没有活跃应用连接"
            textSize = 14f
            setTextColor(UiPalette.ink)
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.CENTER
        })
        card.addView(TextView(this).apply {
            text = if (activeConnections > 0) {
                "已有网络连接，但应用归属暂未进入可展示快照。"
            } else {
                "启动 VPN 并打开任意联网应用后，这里会实时出现。"
            }
            textSize = 11.5f
            setTextColor(UiPalette.muted)
            gravity = Gravity.CENTER
            setPadding(0, dp(5), 0, 0)
        })
        return card
    }

    private fun applicationCard(item: JSONObject): View {
        val packageName = item.optString("packageName", FlowOwnerIdentity.UNKNOWN)
        val aliases = item.optJSONArray("packageAliases").strings()
        val packages = (listOf(packageName) + aliases)
            .filter { it.isNotBlank() && it != FlowOwnerIdentity.UNKNOWN }
            .distinct()
        val identity = resolveApplicationIdentity(packages)
        val sharedUid = item.optBoolean("sharedUid", false)

        val card = UiKit.card(this, paddingDp = 15, radiusDp = 14)
        val top = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        val icon = ImageView(this).apply {
            setImageDrawable(identity.icon)
            if (identity.icon == null) {
                setBackgroundColor(UiPalette.surfaceElevated)
            }
        }
        top.addView(icon, LinearLayout.LayoutParams(dp(42), dp(42)))

        val titleCol = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(12), 0, 0, 0)
        }
        titleCol.addView(TextView(this).apply {
            text = identity.label
            textSize = 14.5f
            setTextColor(UiPalette.ink)
            typeface = Typeface.DEFAULT_BOLD
            maxLines = 1
        })
        titleCol.addView(TextView(this).apply {
            text = when {
                packageName == FlowOwnerIdentity.UNKNOWN -> "无法识别应用归属"
                sharedUid -> "共享 UID · ${packages.joinToString(" · ")}"
                else -> packageName
            }
            textSize = 10.5f
            setTextColor(UiPalette.muted)
            maxLines = 2
            setPadding(0, dp(2), 0, 0)
        })
        top.addView(titleCol, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        val connections = item.optInt("connections", 0)
        top.addView(UiKit.chip(
            this,
            "$connections 条",
            UiPalette.success,
            UiPalette.successSoft,
            UiPalette.successSoftBorder,
        ))
        card.addView(top)

        val protocolRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, dp(11), 0, 0)
        }
        val tcp = item.optInt("tcp", 0)
        val udp = item.optInt("udp", 0)
        if (tcp > 0) protocolRow.addView(UiKit.chip(this, "TCP $tcp", UiPalette.brand, UiPalette.brandSoft))
        if (udp > 0) {
            protocolRow.addView(
                UiKit.chip(this, "UDP $udp", UiPalette.warning, UiPalette.warningSoft),
                LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.WRAP_CONTENT,
                    ViewGroup.LayoutParams.WRAP_CONTENT,
                ).apply { leftMargin = if (tcp > 0) dp(7) else 0 },
            )
        }
        val paths = item.optJSONArray("paths").strings()
        if (paths.isNotEmpty()) {
            protocolRow.addView(
                TextView(this).apply {
                    text = paths.joinToString(" / ") { displayPath(it) }
                    textSize = 10.5f
                    setTextColor(UiPalette.muted)
                    gravity = Gravity.END
                    maxLines = 1
                },
                LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                    leftMargin = dp(8)
                },
            )
        }
        card.addView(protocolRow)

        val uploadRate = item.optLong("uploadRate", 0)
        val downloadRate = item.optLong("downloadRate", 0)
        card.addView(TextView(this).apply {
            text = "↑ ${UiKit.formatSpeed(uploadRate)}   ↓ ${UiKit.formatSpeed(downloadRate)}"
            textSize = 13f
            typeface = Typeface.MONOSPACE
            setTextColor(UiPalette.ink)
            setPadding(0, dp(10), 0, 0)
        })
        card.addView(TextView(this).apply {
            text = "累计 ↑ ${UiKit.formatBytes(item.optLong("upload", 0))} · ↓ ${UiKit.formatBytes(item.optLong("download", 0))}"
            textSize = 10.5f
            setTextColor(UiPalette.placeholder)
            setPadding(0, dp(3), 0, 0)
        })

        val targets = item.optJSONArray("targets")
        if (targets != null && targets.length() > 0) {
            val labels = mutableListOf<String>()
            for (index in 0 until minOf(targets.length(), 4)) {
                val target = targets.optJSONObject(index) ?: continue
                val host = target.optString("host")
                if (host.isBlank()) continue
                val port = target.optInt("port", 0)
                labels += if (port > 0) "$host:$port" else host
            }
            if (labels.isNotEmpty()) {
                card.addView(infoLine("目标", labels.joinToString(" · ")))
            }
        }

        val exits = item.optJSONArray("exits").strings()
        if (exits.isNotEmpty()) {
            card.addView(infoLine("出口", exits.joinToString(" · ") { it.take(18) }))
        }
        return card
    }

    private fun infoLine(label: String, value: String): View {
        return LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.TOP
            setPadding(0, dp(8), 0, 0)
            addView(TextView(this@ConnectionMonitorActivity).apply {
                text = label
                textSize = 10.5f
                setTextColor(UiPalette.placeholder)
            }, LinearLayout.LayoutParams(dp(38), ViewGroup.LayoutParams.WRAP_CONTENT))
            addView(TextView(this@ConnectionMonitorActivity).apply {
                text = value
                textSize = 10.5f
                setTextColor(UiPalette.muted)
                maxLines = 2
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        }
    }

    private data class ApplicationIdentity(val label: String, val icon: Drawable?)

    @Suppress("DEPRECATION")
    private fun resolveApplicationIdentity(packages: List<String>): ApplicationIdentity {
        if (packages.isEmpty()) return ApplicationIdentity("未知应用", null)
        val infos = packages.mapNotNull { packageName ->
            runCatching {
                val info: ApplicationInfo = packageManager.getApplicationInfo(packageName, 0)
                Triple(
                    packageManager.getApplicationLabel(info).toString().ifBlank { packageName },
                    packageManager.getApplicationIcon(info),
                    packageName,
                )
            }.getOrNull()
        }
        if (infos.isEmpty()) return ApplicationIdentity(packages.first(), null)
        val label = if (infos.size == 1) {
            infos.first().first
        } else {
            infos.take(3).joinToString(" / ") { it.first } +
                if (infos.size > 3) " +${infos.size - 3}" else ""
        }
        return ApplicationIdentity(label, infos.first().second)
    }

    private fun displayPath(value: String): String = when (value.lowercase()) {
        "public_direct_quic" -> "公网直连"
        "p2p_quic" -> "P2P"
        "relay_quic" -> "Relay QUIC"
        "relay_tls" -> "Relay TLS"
        else -> value
    }

    private fun JSONArray?.strings(): List<String> {
        if (this == null) return emptyList()
        val result = ArrayList<String>(length())
        for (index in 0 until length()) {
            optString(index).trim().takeIf(String::isNotEmpty)?.let(result::add)
        }
        return result
    }

    private fun dp(value: Int): Int = UiKit.dp(this, value)
}
