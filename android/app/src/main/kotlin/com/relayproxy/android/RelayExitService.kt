package com.relayproxy.android

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Intent
import android.os.Handler
import android.os.IBinder
import android.os.Looper
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

        @Volatile
        private var status = JSONObject()
            .put("connectionState", "STOPPED")
            .put("approvalState", "unknown")
            .toString()

        fun statusJson(): String = status
    }

    private val executor = Executors.newSingleThreadExecutor()
    private val handler = Handler(Looper.getMainLooper())
    private var core: Client? = null
    private var cellularBinder: CellularBinder? = null

    private val refresh = object : Runnable {
        override fun run() {
            core?.let {
                status = runCatching { it.statusJSON() }
                    .getOrElse { errorStatus(it.message ?: "读取状态失败") }
            }
            updateNotification()
            handler.postDelayed(this, 2000)
        }
    }

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
        handler.post(refresh)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> stopRelay()
            else -> startRelay()
        }
        return START_STICKY
    }

    override fun onDestroy() {
        handler.removeCallbacks(refresh)
        cellularBinder?.release()
        val running = core
        core = null
        runCatching { running?.stop() }
        executor.shutdownNow()
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun startRelay() {
        startForeground(NOTIFICATION_ID, buildNotification("正在启动"))
        if (core != null || cellularBinder != null) return

        val config = ConfigStore(this).load()
        if (config.serverAddress.isBlank()) {
            status = errorStatus("请先填写 Relay Server 地址")
            updateNotification()
            return
        }

        if (config.cellularOnly) {
            status = waitingStatus("等待移动数据网络")
            val binder = CellularBinder(this)
            cellularBinder = binder
            binder.bind(
                onAvailable = { startCore(config) },
                onLost = {
                    stopCoreOnly()
                    status = waitingStatus("移动数据断开，等待恢复")
                },
                onError = { message ->
                    stopCoreOnly()
                    status = errorStatus(message)
                },
            )
        } else {
            startCore(config)
        }
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
        val binder = cellularBinder
        cellularBinder = null
        executor.execute {
            runCatching { old?.stop() }
            runCatching { binder?.release() }
            status = JSONObject()
                .put("connectionState", "STOPPED")
                .put("approvalState", "unknown")
                .toString()
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

    private fun buildNotification(text: String): Notification =
        Notification.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_relayproxy)
            .setContentTitle("RelayProxy 网络出口")
            .setContentText(text)
            .setOngoing(true)
            .build()

    private fun updateNotification() {
        val obj = runCatching { JSONObject(status) }.getOrNull()
        val state = obj?.optString("connectionState", "UNKNOWN") ?: "UNKNOWN"
        val approval = obj?.optString("approvalState", "unknown") ?: "unknown"
        val streams = obj?.optLong("activeStreams", 0) ?: 0
        val text = when {
            state == "CONNECTED" && approval == "approved" -> "已连接 · 活跃连接 $streams"
            state == "STOPPED" -> "已停止"
            else -> "$state · $approval"
        }
        getSystemService(NotificationManager::class.java)
            .notify(NOTIFICATION_ID, buildNotification(text))
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
