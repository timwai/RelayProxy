package com.relayproxy.android

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.graphics.Color
import android.graphics.PixelFormat
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.text.TextUtils
import android.view.Gravity
import android.view.MotionEvent
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import android.view.animation.DecelerateInterpolator
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.TextView
import org.json.JSONObject
import java.util.ArrayDeque
import kotlin.math.abs
import kotlin.math.roundToInt

/**
 * System-wide RelayProxy message popup rendered with TYPE_APPLICATION_OVERLAY.
 *
 * The service owns this controller, so popups can be shown while another app is
 * in the foreground. When overlay permission is unavailable the caller-provided
 * fallback receives the message and can post a normal notification instead.
 */
internal class MessageOverlayController(
    private val context: Context,
    private val fallback: (JSONObject) -> Unit,
) {
    companion object {
        fun hasOverlayPermission(context: Context): Boolean =
            Build.VERSION.SDK_INT < Build.VERSION_CODES.M || Settings.canDrawOverlays(context)

        private const val TYPE_VERIFICATION = "verification_code"
        private const val TYPE_MESSAGE = "message"
        private const val TYPE_IMPORTANT = "important"
    }

    private val windowManager = context.getSystemService(WindowManager::class.java)
    private val clipboard = context.getSystemService(ClipboardManager::class.java)
    private val handler = Handler(Looper.getMainLooper())
    private val queue = ArrayDeque<JSONObject>()

    private var currentView: View? = null
    private var currentCard: View? = null
    private var currentMessage: JSONObject? = null
    private var currentParams: WindowManager.LayoutParams? = null
    private var queueBadge: TextView? = null
    private var autoDismiss: Runnable? = null
    private var dismissing = false

    fun enqueue(message: JSONObject) {
        if (Looper.myLooper() != Looper.getMainLooper()) {
            val copy = JSONObject(message.toString())
            handler.post { enqueue(copy) }
            return
        }
        if (!hasOverlayPermission(context)) {
            fallback(message)
            return
        }

        val id = message.optString("id").trim()
        if (id.isNotEmpty()) {
            if (currentMessage?.optString("id") == id) return
            if (queue.any { it.optString("id") == id }) return
        }

        queue.addLast(JSONObject(message.toString()))
        updateQueueBadge()
        if (currentView == null) showNext()
    }

    fun dismissAll() {
        if (Looper.myLooper() != Looper.getMainLooper()) {
            handler.post(::dismissAll)
            return
        }
        queue.clear()
        cancelAutoDismiss()
        currentView?.let { runCatching { windowManager.removeViewImmediate(it) } }
        currentView = null
        currentCard = null
        currentMessage = null
        currentParams = null
        queueBadge = null
        dismissing = false
    }

    private fun showNext() {
        if (currentView != null || dismissing) return
        val message = queue.pollFirst() ?: return
        if (!hasOverlayPermission(context)) {
            fallback(message)
            flushQueueToFallback()
            return
        }

        val popupType = normalizedType(message)
        val palette = Palette(ConfigStore(context).isDarkTheme(), popupType)
        val width = minOf(
            context.resources.displayMetrics.widthPixels - dp(24),
            dp(440),
        ).coerceAtLeast(dp(280))
        val params = WindowManager.LayoutParams(
            width,
            WindowManager.LayoutParams.WRAP_CONTENT,
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY
            } else {
                @Suppress("DEPRECATION")
                WindowManager.LayoutParams.TYPE_PHONE
            },
            WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or
                WindowManager.LayoutParams.FLAG_LAYOUT_IN_SCREEN,
            PixelFormat.TRANSLUCENT,
        ).apply {
            gravity = Gravity.TOP or Gravity.START
            x = ((context.resources.displayMetrics.widthPixels - width) / 2).coerceAtLeast(0)
            y = statusBarHeight() + dp(14)
            setTitle("RelayProxy message overlay")
        }

        val root = buildPopup(message, popupType, palette, params)
        currentMessage = message
        currentView = root
        currentParams = params
        dismissing = false

        try {
            windowManager.addView(root, params)
        } catch (_: Throwable) {
            currentView = null
            currentCard = null
            currentMessage = null
            currentParams = null
            queueBadge = null
            fallback(message)
            showNext()
            return
        }

        currentCard?.apply {
            alpha = 0f
            translationY = -dp(18).toFloat()
            scaleX = 0.975f
            scaleY = 0.975f
            animate()
                .alpha(1f)
                .translationY(0f)
                .scaleX(1f)
                .scaleY(1f)
                .setDuration(220)
                .setInterpolator(DecelerateInterpolator())
                .start()
        }
        updateQueueBadge()
        scheduleAutoDismiss(timeoutFor(popupType))
    }

    private fun buildPopup(
        message: JSONObject,
        popupType: String,
        palette: Palette,
        params: WindowManager.LayoutParams,
    ): View {
        val outer = FrameLayout(context).apply {
            clipToPadding = false
            setPadding(dp(2), dp(2), dp(2), dp(8))
        }

        val card = LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            elevation = dp(18).toFloat()
            background = gradient(
                palette.cardStart,
                palette.cardEnd,
                radius = 22,
                stroke = palette.border,
            )
        }
        currentCard = card

        card.addView(
            View(context).apply {
                background = gradient(palette.accent, palette.accentEnd, radius = 99)
            },
            LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(4)),
        )

        val body = LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            val horizontal = dp(18)
            setPadding(horizontal, dp(15), horizontal, dp(16))
        }
        card.addView(body)

        val header = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        body.addView(header)

        val icon = TextView(context).apply {
            text = when (popupType) {
                TYPE_IMPORTANT -> "!"
                TYPE_MESSAGE -> "✦"
                else -> "123"
            }
            gravity = Gravity.CENTER
            textSize = if (popupType == TYPE_VERIFICATION) 11f else 18f
            typeface = Typeface.DEFAULT_BOLD
            setTextColor(palette.accent)
            background = rounded(palette.accentSoft, 13, palette.accentBorder)
        }
        header.addView(icon, LinearLayout.LayoutParams(dp(42), dp(42)))

        val titleGroup = LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(12), 0, dp(8), 0)
        }
        header.addView(
            titleGroup,
            LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f),
        )

        val badgeRow = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        titleGroup.addView(badgeRow)

        badgeRow.addView(TextView(context).apply {
            text = when (popupType) {
                TYPE_IMPORTANT -> "重要提醒"
                TYPE_MESSAGE -> "消息"
                else -> "验证码"
            }
            textSize = 10f
            typeface = Typeface.DEFAULT_BOLD
            setTextColor(palette.accent)
            setPadding(dp(8), dp(3), dp(8), dp(3))
            background = rounded(palette.accentSoft, 99, palette.accentBorder)
        })

        queueBadge = TextView(context).apply {
            visibility = View.GONE
            textSize = 9.5f
            typeface = Typeface.DEFAULT_BOLD
            setTextColor(palette.muted)
            setPadding(dp(7), dp(3), dp(7), dp(3))
            background = rounded(palette.subtle, 99, palette.border)
        }
        badgeRow.addView(
            queueBadge,
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ).apply { leftMargin = dp(6) },
        )

        val title = message.optString("title").trim().ifEmpty {
            when (popupType) {
                TYPE_IMPORTANT -> "RelayProxy 重要提醒"
                TYPE_MESSAGE -> "RelayProxy 消息"
                else -> "收到新的验证码"
            }
        }
        titleGroup.addView(TextView(context).apply {
            text = title
            textSize = 16f
            typeface = Typeface.DEFAULT_BOLD
            setTextColor(palette.ink)
            maxLines = 2
            ellipsize = TextUtils.TruncateAt.END
            setPadding(0, dp(4), 0, 0)
        })

        val close = TextView(context).apply {
            text = "×"
            gravity = Gravity.CENTER
            textSize = 22f
            setTextColor(palette.muted)
            background = rounded(palette.subtle, 11, palette.border)
            isClickable = true
            isFocusable = true
            contentDescription = "关闭消息"
            setOnClickListener { dismissCurrent() }
        }
        header.addView(close, LinearLayout.LayoutParams(dp(34), dp(34)))

        installDragGesture(titleGroup, params)

        val source = message.optString("source").trim()
        val rule = message.optString("verificationRule").trim()
        val meta = buildString {
            if (source.isNotEmpty()) append("来自 ").append(source)
            if (rule.isNotEmpty()) {
                if (isNotEmpty()) append("  ·  ")
                append(rule)
            }
        }
        if (meta.isNotEmpty()) {
            body.addView(TextView(context).apply {
                text = meta
                textSize = 10.5f
                setTextColor(palette.muted)
                maxLines = 1
                ellipsize = TextUtils.TruncateAt.END
            }, topMargin(12))
        }

        val code = message.optString("verificationCode").trim()
        if (code.isNotEmpty()) {
            val codeBox = LinearLayout(context).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER_VERTICAL
                setPadding(dp(14), dp(12), dp(10), dp(12))
                background = rounded(palette.codeBackground, 15, palette.accentBorder)
            }
            body.addView(codeBox, topMargin(12))

            codeBox.addView(TextView(context).apply {
                text = code
                textSize = 29f
                typeface = Typeface.MONOSPACE
                letterSpacing = 0.12f
                setTextColor(palette.codeText)
                isTextSelectable = true
                maxLines = 1
                ellipsize = TextUtils.TruncateAt.END
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

            val copy = TextView(context).apply {
                text = "复制"
                textSize = 11f
                typeface = Typeface.DEFAULT_BOLD
                gravity = Gravity.CENTER
                setTextColor(Color.WHITE)
                setPadding(dp(12), dp(8), dp(12), dp(8))
                background = rounded(palette.accent, 10)
                isClickable = true
                isFocusable = true
                setOnClickListener {
                    clipboard.setPrimaryClip(
                        ClipData.newPlainText("RelayProxy verification code", code),
                    )
                    text = "已复制 ✓"
                    cancelAutoDismiss()
                    scheduleAutoDismiss(900L)
                }
            }
            codeBox.addView(
                copy,
                LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.WRAP_CONTENT,
                    ViewGroup.LayoutParams.WRAP_CONTENT,
                ).apply { leftMargin = dp(10) },
            )
        }

        val contentText = message.optString("content").trim()
        if (contentText.isNotEmpty()) {
            body.addView(TextView(context).apply {
                text = contentText
                textSize = 13f
                setTextColor(palette.body)
                maxLines = if (popupType == TYPE_IMPORTANT) 7 else 5
                ellipsize = TextUtils.TruncateAt.END
                setLineSpacing(0f, 1.18f)
                isTextSelectable = true
            }, topMargin(if (code.isNotEmpty()) 12 else 14))
        }

        val actions = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.END or Gravity.CENTER_VERTICAL
        }
        body.addView(actions, topMargin(15))

        actions.addView(
            TextView(context).apply {
                text = "按住标题可拖动"
                textSize = 9.5f
                setTextColor(palette.placeholder)
            },
            LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f),
        )

        actions.addView(TextView(context).apply {
            text = if (popupType == TYPE_IMPORTANT) "我知道了" else "关闭"
            textSize = 11.5f
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.CENTER
            setTextColor(if (popupType == TYPE_IMPORTANT) Color.WHITE else palette.ink)
            setPadding(dp(14), dp(9), dp(14), dp(9))
            background = if (popupType == TYPE_IMPORTANT) {
                rounded(palette.accent, 11)
            } else {
                rounded(palette.subtle, 11, palette.border)
            }
            isClickable = true
            isFocusable = true
            setOnClickListener { dismissCurrent() }
        })

        outer.addView(
            card,
            FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ),
        )
        return outer
    }

    private fun installDragGesture(
        dragTarget: View,
        params: WindowManager.LayoutParams,
    ) {
        var downRawX = 0f
        var downRawY = 0f
        var startX = 0
        var startY = 0
        dragTarget.setOnTouchListener { _, event ->
            when (event.actionMasked) {
                MotionEvent.ACTION_DOWN -> {
                    downRawX = event.rawX
                    downRawY = event.rawY
                    startX = params.x
                    startY = params.y
                    cancelAutoDismiss()
                    true
                }
                MotionEvent.ACTION_MOVE -> {
                    val dx = event.rawX - downRawX
                    val dy = event.rawY - downRawY
                    val view = currentView ?: return@setOnTouchListener true
                    val maxX = (context.resources.displayMetrics.widthPixels - params.width).coerceAtLeast(0)
                    val maxY = (context.resources.displayMetrics.heightPixels - dp(120)).coerceAtLeast(statusBarHeight())
                    params.x = (startX + dx.roundToInt()).coerceIn(0, maxX)
                    params.y = (startY + dy.roundToInt()).coerceIn(statusBarHeight(), maxY)
                    runCatching { windowManager.updateViewLayout(view, params) }
                    true
                }
                MotionEvent.ACTION_UP, MotionEvent.ACTION_CANCEL -> {
                    val type = currentMessage?.let(::normalizedType) ?: TYPE_MESSAGE
                    scheduleAutoDismiss(timeoutFor(type))
                    true
                }
                else -> false
            }
        }
    }

    private fun dismissCurrent() {
        if (dismissing) return
        val view = currentView ?: return
        val card = currentCard
        dismissing = true
        cancelAutoDismiss()

        val remove = Runnable {
            runCatching { windowManager.removeViewImmediate(view) }
            currentView = null
            currentCard = null
            currentMessage = null
            currentParams = null
            queueBadge = null
            dismissing = false
            showNext()
        }
        if (card == null) {
            remove.run()
            return
        }
        card.animate()
            .alpha(0f)
            .translationY(-dp(10).toFloat())
            .scaleX(0.985f)
            .scaleY(0.985f)
            .setDuration(150)
            .withEndAction(remove)
            .start()
    }

    private fun flushQueueToFallback() {
        while (queue.isNotEmpty()) fallback(queue.removeFirst())
        updateQueueBadge()
    }

    private fun updateQueueBadge() {
        val badge = queueBadge ?: return
        if (queue.isEmpty()) {
            badge.visibility = View.GONE
        } else {
            badge.text = "+${queue.size} 条待处理"
            badge.visibility = View.VISIBLE
        }
    }

    private fun scheduleAutoDismiss(delayMs: Long) {
        cancelAutoDismiss()
        if (delayMs <= 0) return
        val task = Runnable { dismissCurrent() }
        autoDismiss = task
        handler.postDelayed(task, delayMs)
    }

    private fun cancelAutoDismiss() {
        autoDismiss?.let(handler::removeCallbacks)
        autoDismiss = null
    }

    private fun timeoutFor(type: String): Long = when (type) {
        TYPE_IMPORTANT -> 30_000L
        TYPE_VERIFICATION -> 20_000L
        else -> 12_000L
    }

    private fun normalizedType(message: JSONObject): String = when (
        message.optString("popupType", TYPE_VERIFICATION).trim().lowercase()
    ) {
        TYPE_MESSAGE -> TYPE_MESSAGE
        TYPE_IMPORTANT -> TYPE_IMPORTANT
        else -> TYPE_VERIFICATION
    }

    private fun statusBarHeight(): Int {
        val id = context.resources.getIdentifier("status_bar_height", "dimen", "android")
        return if (id > 0) context.resources.getDimensionPixelSize(id) else dp(28)
    }

    private fun dp(value: Int): Int =
        (value * context.resources.displayMetrics.density + 0.5f).toInt()

    private fun topMargin(value: Int): LinearLayout.LayoutParams =
        LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT,
            ViewGroup.LayoutParams.WRAP_CONTENT,
        ).apply { topMargin = dp(value) }

    private fun rounded(
        fill: Int,
        radius: Int,
        stroke: Int? = null,
    ): GradientDrawable = GradientDrawable().apply {
        shape = GradientDrawable.RECTANGLE
        setColor(fill)
        cornerRadius = dp(radius).toFloat()
        stroke?.let { setStroke(dp(1), it) }
    }

    private fun gradient(
        start: Int,
        end: Int,
        radius: Int,
        stroke: Int? = null,
    ): GradientDrawable = GradientDrawable(
        GradientDrawable.Orientation.TL_BR,
        intArrayOf(start, end),
    ).apply {
        shape = GradientDrawable.RECTANGLE
        cornerRadius = dp(radius).toFloat()
        stroke?.let { setStroke(dp(1), it) }
    }

    private data class Palette(
        val dark: Boolean,
        val type: String,
    ) {
        val ink = if (dark) Color.rgb(248, 250, 252) else Color.rgb(15, 23, 42)
        val body = if (dark) Color.rgb(203, 213, 225) else Color.rgb(71, 85, 105)
        val muted = if (dark) Color.rgb(148, 163, 184) else Color.rgb(100, 116, 139)
        val placeholder = if (dark) Color.rgb(100, 116, 139) else Color.rgb(148, 163, 184)
        val border = if (dark) Color.rgb(51, 65, 85) else Color.rgb(226, 232, 240)
        val subtle = if (dark) Color.rgb(30, 41, 59) else Color.rgb(248, 250, 252)

        val accent = when (type) {
            TYPE_IMPORTANT -> if (dark) Color.rgb(251, 146, 60) else Color.rgb(234, 88, 12)
            TYPE_MESSAGE -> if (dark) Color.rgb(96, 165, 250) else Color.rgb(37, 99, 235)
            else -> if (dark) Color.rgb(45, 212, 191) else Color.rgb(13, 148, 136)
        }
        val accentEnd = when (type) {
            TYPE_IMPORTANT -> if (dark) Color.rgb(248, 113, 113) else Color.rgb(220, 38, 38)
            TYPE_MESSAGE -> if (dark) Color.rgb(129, 140, 248) else Color.rgb(79, 70, 229)
            else -> if (dark) Color.rgb(96, 165, 250) else Color.rgb(37, 99, 235)
        }
        val accentSoft = when (type) {
            TYPE_IMPORTANT -> if (dark) Color.rgb(68, 43, 28) else Color.rgb(255, 247, 237)
            TYPE_MESSAGE -> if (dark) Color.rgb(25, 42, 70) else Color.rgb(239, 246, 255)
            else -> if (dark) Color.rgb(20, 55, 58) else Color.rgb(240, 253, 250)
        }
        val accentBorder = when (type) {
            TYPE_IMPORTANT -> if (dark) Color.rgb(120, 72, 42) else Color.rgb(254, 215, 170)
            TYPE_MESSAGE -> if (dark) Color.rgb(48, 76, 118) else Color.rgb(191, 219, 254)
            else -> if (dark) Color.rgb(40, 91, 91) else Color.rgb(153, 246, 228)
        }
        val cardStart = when {
            dark && type == TYPE_IMPORTANT -> Color.rgb(31, 25, 24)
            dark -> Color.rgb(15, 23, 42)
            type == TYPE_IMPORTANT -> Color.rgb(255, 251, 247)
            else -> Color.WHITE
        }
        val cardEnd = when {
            dark && type == TYPE_IMPORTANT -> Color.rgb(24, 21, 23)
            dark -> Color.rgb(17, 24, 39)
            type == TYPE_MESSAGE -> Color.rgb(248, 250, 255)
            type == TYPE_VERIFICATION -> Color.rgb(247, 254, 252)
            else -> Color.rgb(255, 247, 237)
        }
        val codeBackground = accentSoft
        val codeText = accent
    }
}
