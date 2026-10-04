package com.relayproxy.android

import android.content.Context
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.LinearGradient
import android.graphics.Paint
import android.graphics.Path
import android.graphics.Shader
import android.util.AttributeSet
import android.view.View

/**
 * 实时网络上下行吞吐波形走势视图。
 * 使用双缓冲采样与贝塞尔平滑曲线，在 Canvas 上实时绘制网络流量态势。
 */
class ThroughputSparklineView @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
    defStyleAttr: Int = 0,
) : View(context, attrs, defStyleAttr) {

    private val maxSamples = 32
    private val samples = FloatArray(maxSamples)
    private var currentSampleCount = 0

    private val linePaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.STROKE
        strokeWidth = dp(1.8f)
        strokeCap = Paint.Cap.ROUND
        strokeJoin = Paint.Join.ROUND
        color = Color.rgb(59, 130, 246) // Blue 500
    }

    private val fillPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.FILL
    }

    private val dotPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.FILL
        color = Color.rgb(96, 165, 250) // Blue 400
    }

    private val dotGlowPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.FILL
        color = Color.argb(80, 59, 130, 246)
    }

    private val path = Path()
    private val fillPath = Path()

    init {
        // 初始填充微小 baseline 样本，避免突兀
        for (i in 0 until maxSamples) {
            samples[i] = 0f
        }
    }

    /**
     * 添加新的实时速率采样点 (Bytes/s)
     */
    fun addSample(bytesPerSec: Long) {
        // 平移样本
        System.arraycopy(samples, 1, samples, 0, maxSamples - 1)
        samples[maxSamples - 1] = bytesPerSec.toFloat().coerceAtLeast(0f)
        if (currentSampleCount < maxSamples) currentSampleCount++
        postInvalidateOnAnimation()
    }

    override fun onSizeChanged(w: Int, h: Int, oldw: Int, oldh: Int) {
        super.onSizeChanged(w, h, oldw, oldh)
        if (w > 0 && h > 0) {
            val alpha = if (UiPalette.isDark) 80 else 45
            fillPaint.shader = LinearGradient(
                0f, 0f, 0f, h.toFloat(),
                Color.argb(alpha, 59, 130, 246),
                Color.argb(0, 59, 130, 246),
                Shader.TileMode.CLAMP
            )
        }
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        val w = width.toFloat()
        val h = height.toFloat()
        if (w <= 0f || h <= 0f) return

        // 计算当前样本中的峰值，设定最小量程 64 KB/s 避免微小噪点放大为满格
        var maxVal = 64f * 1024f
        for (s in samples) {
            if (s > maxVal) maxVal = s
        }

        val stepX = w / (maxSamples - 1).toFloat()
        val verticalPadding = dp(4f)
        val availableHeight = h - verticalPadding * 2

        path.reset()
        fillPath.reset()

        var lastX = 0f
        var lastY = h - verticalPadding

        for (i in 0 until maxSamples) {
            val norm = (samples[i] / maxVal).coerceIn(0f, 1f)
            val x = i * stepX
            val y = h - verticalPadding - (norm * availableHeight)

            if (i == 0) {
                path.moveTo(x, y)
                fillPath.moveTo(x, h)
                fillPath.lineTo(x, y)
            } else {
                // 平滑连接
                val prevX = (i - 1) * stepX
                val prevNorm = (samples[i - 1] / maxVal).coerceIn(0f, 1f)
                val prevY = h - verticalPadding - (prevNorm * availableHeight)
                val cX = (prevX + x) / 2f
                path.cubicTo(cX, prevY, cX, y, x, y)
                fillPath.cubicTo(cX, prevY, cX, y, x, y)
            }
            lastX = x
            lastY = y
        }

        // 封闭 fillPath
        fillPath.lineTo(w, h)
        fillPath.close()

        // 绘制渐变填充与外轮廓
        canvas.drawPath(fillPath, fillPaint)
        canvas.drawPath(path, linePaint)

        // 在最右侧当前采样点绘制光斑
        if (samples[maxSamples - 1] > 0f) {
            canvas.drawCircle(lastX, lastY, dp(4.5f), dotGlowPaint)
            canvas.drawCircle(lastX, lastY, dp(2.2f), dotPaint)
        }
    }

    private fun dp(value: Float): Float {
        return value * resources.displayMetrics.density
    }
}
