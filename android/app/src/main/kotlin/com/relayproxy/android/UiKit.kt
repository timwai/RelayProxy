package com.relayproxy.android

import android.content.Context
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView

import android.app.AlertDialog
import android.content.res.ColorStateList
import android.widget.ArrayAdapter
import android.widget.Switch

/**
 * 现代 Material 3 / Cyber Dashboard 风格的轻量设计系统与组件构造器。
 */
object UiPalette {
    @Volatile
    var isDark: Boolean = true

    fun sync(context: Context): Boolean {
        isDark = ConfigStore(context).isDarkTheme()
        return isDark
    }

    val bg: Int get() = if (isDark) Color.rgb(10, 15, 29) else Color.rgb(241, 245, 249) // #0A0F1D / #F1F5F9
    val surface: Int get() = if (isDark) Color.rgb(17, 24, 39) else Color.WHITE // #111827 / White
    val surfaceSubtle: Int get() = if (isDark) Color.rgb(15, 23, 42) else Color.rgb(248, 250, 252)
    val surfaceElevated: Int get() = if (isDark) Color.rgb(30, 41, 59) else Color.rgb(226, 232, 240)
    val inputBg: Int get() = if (isDark) Color.rgb(22, 31, 48) else Color.rgb(248, 250, 252) // #161F30 / #F8FAFC
    val line: Int get() = if (isDark) Color.rgb(38, 49, 68) else Color.rgb(226, 232, 240)
    val lineSubtle: Int get() = if (isDark) Color.rgb(55, 68, 90) else Color.rgb(203, 213, 225)
    val ink: Int get() = if (isDark) Color.WHITE else Color.rgb(15, 23, 42) // 纯白高对比 / #0F172A
    val muted: Int get() = if (isDark) Color.rgb(180, 195, 215) else Color.rgb(100, 116, 139) // 清晰可见明亮副文字
    val placeholder: Int get() = if (isDark) Color.rgb(130, 145, 168) else Color.rgb(148, 163, 184)
    val brand: Int get() = if (isDark) Color.rgb(96, 165, 250) else Color.rgb(37, 99, 235) // #60A5FA / #2563EB
    val brandDark: Int get() = Color.rgb(29, 78, 216)
    val brandSoft: Int get() = if (isDark) Color.argb(45, 59, 130, 246) else Color.argb(25, 37, 99, 235)
    val brandSoftBorder: Int get() = if (isDark) Color.argb(90, 96, 165, 250) else Color.argb(50, 37, 99, 235)
    val success: Int get() = if (isDark) Color.rgb(52, 211, 153) else Color.rgb(5, 150, 105)
    val successSoft: Int get() = if (isDark) Color.argb(45, 16, 185, 129) else Color.argb(25, 5, 150, 105)
    val successSoftBorder: Int get() = if (isDark) Color.argb(90, 52, 211, 153) else Color.argb(50, 5, 150, 105)
    val warning: Int get() = if (isDark) Color.rgb(251, 191, 36) else Color.rgb(217, 119, 6)
    val warningSoft: Int get() = if (isDark) Color.argb(45, 245, 158, 11) else Color.argb(25, 217, 119, 6)
    val warningSoftBorder: Int get() = if (isDark) Color.argb(90, 251, 191, 36) else Color.argb(50, 217, 119, 6)
    val danger: Int get() = if (isDark) Color.rgb(248, 113, 113) else Color.rgb(220, 38, 38)
    val dangerSoft: Int get() = if (isDark) Color.argb(45, 239, 68, 68) else Color.argb(25, 220, 38, 38)
    val dangerSoftBorder: Int get() = if (isDark) Color.argb(90, 248, 113, 113) else Color.argb(50, 220, 38, 38)
}

object UiKit {

    fun dp(context: Context, value: Int): Int {
        return (value * context.resources.displayMetrics.density + 0.5f).toInt()
    }

    fun rounded(
        context: Context,
        fillColor: Int,
        radiusDp: Int,
        strokeColor: Int? = null,
        strokeWidthDp: Int = 1,
    ): GradientDrawable {
        return GradientDrawable().apply {
            shape = GradientDrawable.RECTANGLE
            setColor(fillColor)
            cornerRadius = dp(context, radiusDp).toFloat()
            if (strokeColor != null) {
                setStroke(dp(context, strokeWidthDp), strokeColor)
            }
        }
    }

    fun gradientRounded(
        context: Context,
        startColor: Int,
        endColor: Int,
        radiusDp: Int,
        orientation: GradientDrawable.Orientation = GradientDrawable.Orientation.LEFT_RIGHT,
    ): GradientDrawable {
        return GradientDrawable(orientation, intArrayOf(startColor, endColor)).apply {
            shape = GradientDrawable.RECTANGLE
            cornerRadius = dp(context, radiusDp).toFloat()
        }
    }

    fun card(
        context: Context,
        paddingDp: Int = 16,
        radiusDp: Int = 14,
        backgroundColor: Int = UiPalette.surface,
        borderColor: Int = UiPalette.line,
    ): LinearLayout {
        return LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            val p = dp(context, paddingDp)
            setPadding(p, p, p, p)
            background = rounded(context, backgroundColor, radiusDp, borderColor)
        }
    }

    fun chip(
        context: Context,
        textValue: String,
        textColor: Int,
        fillColor: Int,
        borderColor: Int? = null,
        radiusDp: Int = 6,
    ): TextView {
        return TextView(context).apply {
            text = textValue
            textSize = 11f
            setTextColor(textColor)
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.CENTER
            val pxH = dp(context, 10)
            val pxV = dp(context, 4)
            setPadding(pxH, pxV, pxH, pxV)
            background = rounded(context, fillColor, radiusDp, borderColor)
        }
    }

    fun metricColumn(
        context: Context,
        label: String,
        initialValue: String = "—",
        valueColor: Int = UiPalette.ink,
    ): Pair<LinearLayout, TextView> {
        val valueView = TextView(context).apply {
            text = initialValue
            textSize = 14.5f
            setTextColor(valueColor)
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.CENTER_HORIZONTAL
        }
        val layout = LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            addView(TextView(context).apply {
                text = label
                textSize = 11f
                setTextColor(UiPalette.muted)
                gravity = Gravity.CENTER_HORIZONTAL
            })
            val lp = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ).apply { topMargin = dp(context, 3) }
            addView(valueView, lp)
        }
        return layout to valueView
    }

    fun formatSpeed(bytesPerSec: Long): String {
        if (bytesPerSec <= 0) return "0 B/s"
        val kb = bytesPerSec / 1024.0
        if (kb < 1024) {
            return String.format("%.0f KB/s", kb)
        }
        val mb = kb / 1024.0
        return String.format("%.1f MB/s", mb)
    }

    fun formatBytes(value: Long): String {
        if (value <= 0) return "0 B"
        val units = arrayOf("B", "KB", "MB", "GB", "TB")
        var amount = value.toDouble()
        var unit = 0
        while (amount >= 1024 && unit < units.lastIndex) {
            amount /= 1024
            unit++
        }
        return if (unit == 0) "$value ${units[unit]}" else "%.1f %s".format(amount, units[unit])
    }

    fun formatDuration(uptimeMs: Long): String {
        if (uptimeMs <= 0) return "—"
        val totalSeconds = uptimeMs / 1000
        val hours = totalSeconds / 3600
        val minutes = (totalSeconds % 3600) / 60
        val seconds = totalSeconds % 60
        return when {
            hours > 0 -> "${hours}小时 ${minutes}分"
            minutes > 0 -> "${minutes}分 ${seconds}秒"
            else -> "${seconds}秒"
        }
    }

    fun formatDeviceId(value: String): String {
        if (value.isBlank()) return "—"
        return value.chunked(16).joinToString("\n")
    }

    /**
     * 构建适配日间/夜间模式的 Spinner 适配器，确保选中项与下拉弹窗文字与底色均具备极高对比度
     */
    fun <T> themedSpinnerAdapter(context: Context, items: List<T>): ArrayAdapter<T> {
        return object : ArrayAdapter<T>(context, android.R.layout.simple_spinner_item, items) {
            override fun getView(position: Int, convertView: View?, parent: ViewGroup): View {
                val v = super.getView(position, convertView, parent)
                if (v is TextView) {
                    v.setTextColor(UiPalette.ink)
                    v.textSize = 13.5f
                }
                return v
            }

            override fun getDropDownView(position: Int, convertView: View?, parent: ViewGroup): View {
                val v = super.getDropDownView(position, convertView, parent)
                v.setBackgroundColor(UiPalette.surface)
                if (v is TextView) {
                    v.setTextColor(UiPalette.ink)
                    v.textSize = 14f
                    val p = dp(context, 12)
                    v.setPadding(p, p, p, p)
                }
                return v
            }
        }
    }

    /**
     * 构建适配日间/夜间模式的 AlertDialog.Builder
     */
    fun alertDialog(context: Context, title: String? = null, message: String? = null): AlertDialog.Builder {
        val themeRes = if (UiPalette.isDark) {
            android.R.style.Theme_DeviceDefault_Dialog_Alert
        } else {
            android.R.style.Theme_DeviceDefault_Light_Dialog_Alert
        }
        return AlertDialog.Builder(context, themeRes).apply {
            if (title != null) setTitle(title)
            if (message != null) setMessage(message)
        }
    }

    /**
     * 统一美化 Switch 控件的滑动轨道与拇指按钮色彩
     */
    fun styleSwitch(switchView: Switch) {
        val thumbColors = intArrayOf(UiPalette.brand, UiPalette.muted)
        val trackColors = if (UiPalette.isDark) {
            intArrayOf(Color.argb(120, 59, 130, 246), Color.argb(80, 51, 65, 85))
        } else {
            intArrayOf(Color.rgb(191, 219, 254), Color.rgb(226, 232, 240))
        }
        switchView.thumbTintList = ColorStateList(
            arrayOf(intArrayOf(android.R.attr.state_checked), intArrayOf()),
            thumbColors
        )
        switchView.trackTintList = ColorStateList(
            arrayOf(intArrayOf(android.R.attr.state_checked), intArrayOf()),
            trackColors
        )
    }
}
