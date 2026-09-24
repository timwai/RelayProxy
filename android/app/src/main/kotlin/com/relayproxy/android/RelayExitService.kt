package com.relayproxy.android

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.SystemClock
import com.relayproxy.core.androidcore.Androidcore
import com.relayproxy.core.androidcore.Client
import org.json.JSONObject
import java.io.File
import java.util.concurrent.Executors

class RelayExitService : Service() {
    companion object {
        const val ACTION_START = "com.relayproxy.android.START"
        const val ACTION_STOP = "com.relayproxy.android.STOP"
        private const val CHANNEL_ID = "relayproxy_exit"
        private const val NOTIFICATION_ID = 1001

        private const val UI_REFRESH_MS = 1_000L
        private const val CONNECTING_REFRESH_MS = 3_000L
        private const val ACTIVE_REFRESH_MS = 5_000L
        private const val IDLE_REFRESH_MS = 20_000L
        private const val WAITING_REFRESH_MS = 30_000L
        private const val ERROR_REFRESH_MS = 30_000L

        @Volatile
        private var status = JSONObject()
            .put("connectionState", "STOPPED")
            .put("approvalState", "unknown")
            .toString()

        @Volatile
        private var serviceStartedAtElapsed: Long = 0

        @Volatile
        private var uiVisible = false

        @Volatile
        private var activeInstance: RelayExitService? = null

        fun setUiVisible(visible: Boolean) {
            uiVisible = visible
            activeInstance?.requestRefreshSoon()
        }

        fun statusJson(): String = runCatching {
            val uptime = if (serviceStartedAtElapsed > 0) {
                (SystemClock.elapsedRealtime() - serviceStartedAtElapsed).coerceAtLeast(0)
            } else {
                0
            }
            JSONObject(status)
                .put("serviceUptimeMs", uptime)
                .toString()
        }.getOrElse { status }
    }

    private val executor = Executors.newSingleThreadExecutor()
    private val handler = Handler(Looper.getMainLooper())
    private var core: Client? = null
    private var networkBinder: NetworkBinder? = null
    private var lastNotificationText: String? = null

    private val refresh = object : Runnable {
        override fun run() {
            core?.let {
                status = runCatching { it.statusJSON() }
                    .getOrElse { errorStatus(it.message ?: "读取状态失败") }
            }
            updateNotificationIfChanged()
            val delay = nextRefreshDelay()
            if (delay > 0) {
                handler.postDelayed(this, delay)
            }
        }
    }

    override fun onCreate() {
        super.onCreate()
        activeInstance = this
        createNotificationChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val store = ConfigStore(this)
        when (intent?.action) {
            ACTION_STOP -> {
                store.setDesiredRunning(false)
                stopRelay()
            }
            ACTION_START -> {
                store.setDesiredRunning(true)
                if (serviceStartedAtElapsed == 0L) {
                    serviceStartedAtElapsed = SystemClock.elapsedRealtime()
                }
                startRelay()
            }
            else -> {
                if (store.isDesiredRunning()) {
                    if (serviceStartedAtElapsed == 0L) {
                        serviceStartedAtElapsed = SystemClock.elapsedRealtime()
                    }
                    startRelay()
                } else {
                    stopSelf()
                }
            }
        }
        return START_STICKY
    }

    override fun onDestroy() {
        handler.removeCallbacks(refresh)
        if (activeInstance === this) {
            activeInstance = null
        }
        networkBinder?.release()
        val running = core
        core = null
        runCatching { running?.stop() }
        executor.shutdownNow()
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun startRelay() {
        val startingText = "正在启动"
        startForeground(NOTIFICATION_ID, buildNotification(startingText))
        lastNotificationText = startingText
        scheduleRefresh(0)
        if (core != null || networkBinder != null) return

        val config = ConfigStore(this).load()
        if (config.serverAddress.isBlank()) {
            status = errorStatus("请先填写 Relay Server 地址")
            updateNotificationIfChanged(force = true)
            scheduleRefresh(ERROR_REFRESH_MS)
            return
        }

        if (config.networkMode == NetworkBinder.MODE_AUTO) {
            startCore(config)
            return
        }

        val networkLabel = if (config.networkMode == NetworkBinder.MODE_WIFI) "Wi-Fi" else "移动数据"
        status = waitingStatus("等待${networkLabel}网络")
        updateNotificationIfChanged(force = true)
        scheduleRefresh(WAITING_REFRESH_MS)
        val binder = NetworkBinder(this)
        networkBinder = binder
        binder.bind(
            mode = config.networkMode,
            onAvailable = {
                startCore(config)
                requestRefreshSoon()
            },
            onLost = {
                stopCoreOnly()
                status = waitingStatus("${networkLabel}断开，等待恢复")
                requestRefreshSoon()
            },
            onError = { message ->
                stopCoreOnly()
                status = errorStatus(message)
                requestRefreshSoon()
            },
        )
    }

    private fun startCore(config: ExitConfig) {
        executor.execute {
            synchronized(this) {
                if (core != null) return@execute
                try {
                    val identity = File(filesDir, "relayproxy/device-identity.json")
                    val client = Androidcore.newClient(config.coreJson(), identity.absolutePath)
                    client.start()
                    core = client
                    status = client.statusJSON()
                } catch (t: Throwable) {
                    status = errorStatus(t.message ?: t.javaClass.simpleName)
                }
            }
            requestRefreshSoon()
        }
    }

    private fun stopCoreOnly() {
        val old = synchronized(this) {
            val value = core
            core = null
            value
        }
        executor.execute {
            runCatching { old?.stop() }
        }
    }

    private fun stopRelay() {
        handler.removeCallbacks(refresh)
        val old = synchronized(this) {
            val value = core
            core = null
            value
        }
        val binder = networkBinder
        networkBinder = null
        executor.execute {
            runCatching { old?.stop() }
            runCatching { binder?.release() }
            status = JSONObject()
                .put("connectionState", "STOPPED")
                .put("approvalState", "unknown")
                .toString()
            serviceStartedAtElapsed = 0
            lastNotificationText = null
            handler.post {
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
            }
        }
    }

    private fun createNotificationChannel() {
        val manager = getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(
            NotificationChannel(
                CHANNEL_ID,
                "RelayProxy 网络出口",
                NotificationManager.IMPORTANCE_LOW,
            )
        )
    }

    private fun buildNotification(text: String): Notification {
        val openApp = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return Notification.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_relayproxy)
            .setContentTitle("RelayProxy 网络出口")
            .setContentText(text)
            .setContentIntent(openApp)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .build()
    }

    private fun updateNotificationIfChanged(force: Boolean = false) {
        val obj = runCatching { JSONObject(status) }.getOrNull()
        val state = obj?.optString("connectionState", "UNKNOWN") ?: "UNKNOWN"
        val approval = obj?.optString("approvalState", "unknown") ?: "unknown"
        val streams = obj?.optLong("activeStreams", 0) ?: 0
        val text = when {
            state == "CONNECTED" && approval == "approved" && streams > 0 ->
                "已连接 · 活跃连接 $streams"
            state == "CONNECTED" && approval == "approved" ->
                "已连接 · 空闲"
            state == "WAITING_NETWORK" ->
                obj?.optString("lastError", "等待网络") ?: "等待网络"
            state == "ERROR" ->
                obj?.optString("lastError", "服务异常") ?: "服务异常"
            state == "STOPPED" -> "已停止"
            else -> "$state · $approval"
        }

        if (!force && text == lastNotificationText) {
            return
        }
        lastNotificationText = text
        getSystemService(NotificationManager::class.java)
            .notify(NOTIFICATION_ID, buildNotification(text))
    }

    private fun nextRefreshDelay(): Long {
        if (uiVisible) return UI_REFRESH_MS

        val obj = runCatching { JSONObject(status) }.getOrNull()
        val state = obj?.optString("connectionState", "UNKNOWN") ?: "UNKNOWN"
        val streams = obj?.optLong("activeStreams", 0) ?: 0
        return when (state) {
            "CONNECTING" -> CONNECTING_REFRESH_MS
            "CONNECTED" -> if (streams > 0) ACTIVE_REFRESH_MS else IDLE_REFRESH_MS
            "WAITING_NETWORK" -> WAITING_REFRESH_MS
            "ERROR" -> ERROR_REFRESH_MS
            "STOPPED" -> 0L
            else -> IDLE_REFRESH_MS
        }
    }

    private fun requestRefreshSoon() {
        scheduleRefresh(0)
    }

    private fun scheduleRefresh(delayMs: Long) {
        handler.removeCallbacks(refresh)
        if (delayMs <= 0) {
            handler.post(refresh)
        } else {
            handler.postDelayed(refresh, delayMs)
        }
    }

    private fun errorStatus(message: String): String = JSONObject()
        .put("connectionState", "ERROR")
        .put("approvalState", "unknown")
        .put("lastError", message)
        .toString()

    private fun waitingStatus(message: String): String = JSONObject()
        .put("connectionState", "WAITING_NETWORK")
        .put("approvalState", "unknown")
        .put("lastError", message)
        .toString()
}
