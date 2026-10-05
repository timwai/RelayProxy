package com.relayproxy.android

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.res.Configuration
import android.content.Intent
import android.content.IntentFilter
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager
import android.os.SystemClock
import com.relayproxy.core.androidcore.Androidcore
import com.relayproxy.core.androidcore.Client
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.util.concurrent.Executors

class RelayExitService : Service() {
    companion object {
        const val ACTION_START = "com.relayproxy.android.START"
        const val ACTION_STOP = "com.relayproxy.android.STOP"
        const val ACTION_RECONFIGURE = "com.relayproxy.android.RECONFIGURE"
        const val ACTION_CONNECT = "com.relayproxy.android.CONNECT"
        const val ACTION_MESSAGE = "com.relayproxy.android.MESSAGE"
        const val EXTRA_MESSAGE_JSON = "message_json"
        private const val CHANNEL_ID = "relayproxy_exit"
        private const val MESSAGE_CHANNEL_ID = "relayproxy_messages"
        private const val NOTIFICATION_ID = 1001

        private const val UI_REFRESH_MS = 1_000L
        private const val MESSAGE_POLL_MS = 1_000L
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

        fun refreshGlobalMessageOverlaySetting() {
            activeInstance?.applyGlobalMessageOverlaySetting()
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
    @Volatile
    private var core: Client? = null

    @Volatile
    private var relayConfig: ExitConfig? = null

    @Volatile
    private var networkBinder: NetworkBinder? = null
    private var lastNotificationText: String? = null
    private lateinit var powerManager: PowerManager
    private lateinit var messageOverlayController: MessageOverlayController
    private var powerReceiverRegistered = false
    private var coreGeneration = 0L

    @Volatile
    private var destroyed = false

    @Volatile
    private var activeNetworkMode: String? = null

    private val powerStateReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            applyP2PPowerProfile()
            requestRefreshSoon()
        }
    }

    private val refresh = object : Runnable {
        override fun run() {
            core?.let {
                status = runCatching { decorateStatus(it.statusJSON()) }
                    .getOrElse { errorStatus(it.message ?: "读取状态失败") }
            }
            updateNotificationIfChanged()
            val delay = nextRefreshDelay()
            if (delay > 0) {
                handler.postDelayed(this, delay)
            }
        }
    }

    private val messagePoll = object : Runnable {
        override fun run() {
            core?.let(::drainMessages)
            if (!destroyed) {
                handler.postDelayed(this, MESSAGE_POLL_MS)
            }
        }
    }

    override fun onCreate() {
        super.onCreate()
        activeInstance = this
        powerManager = getSystemService(PowerManager::class.java)
        messageOverlayController = MessageOverlayController(this, ::showMessageNotification)
        registerPowerStateReceiver()
        createNotificationChannel()
        handler.post(messagePoll)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val store = ConfigStore(this)
        when (intent?.action) {
            ACTION_STOP -> {
                store.setDesiredRunning(false)
                if (shouldRunCore(store)) reconfigureRelay() else stopRelay(startId)
            }
            ACTION_START -> {
                store.setDesiredRunning(true)
                if (serviceStartedAtElapsed == 0L) {
                    serviceStartedAtElapsed = SystemClock.elapsedRealtime()
                }
                if (core != null || networkBinder != null) reconfigureRelay() else startRelay()
            }
            ACTION_RECONFIGURE -> {
                if (shouldRunCore(store)) {
                    if (serviceStartedAtElapsed == 0L) {
                        serviceStartedAtElapsed = SystemClock.elapsedRealtime()
                    }
                    reconfigureRelay()
                } else {
                    stopRelay(startId)
                }
            }
            ACTION_CONNECT -> {
                if (store.hasConnectionConfig()) {
                    if (serviceStartedAtElapsed == 0L) {
                        serviceStartedAtElapsed = SystemClock.elapsedRealtime()
                    }
                    if (core == null && networkBinder == null) startRelay() else reconfigureRelay()
                } else {
                    stopRelay(startId)
                }
            }
            else -> {
                if (shouldRunCore(store)) {
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

    override fun onConfigurationChanged(newConfig: Configuration) {
        super.onConfigurationChanged(newConfig)
        if (::messageOverlayController.isInitialized &&
            ConfigStore(this).themeMode() == ConfigStore.THEME_SYSTEM
        ) {
            messageOverlayController.refreshTheme()
        }
    }

    override fun onDestroy() {
        handler.removeCallbacks(refresh)
        handler.removeCallbacks(messagePoll)
        if (::messageOverlayController.isInitialized) {
            messageOverlayController.dismissAll()
        }
        unregisterPowerStateReceiver()
        if (activeInstance === this) {
            activeInstance = null
        }
        destroyed = true
        val old = synchronized(this) {
            coreGeneration++
            val value = core
            core = null
            value
        }
        networkBinder?.release()
        networkBinder = null
        activeNetworkMode = null
        executeCoreTask { runCatching { old?.stop() } }
        executor.shutdown()
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun shouldRunCore(store: ConfigStore): Boolean {
        return store.hasConnectionConfig()
    }

    private fun startRelay() {
        val startingText = "正在启动"
        startForeground(NOTIFICATION_ID, buildNotification(startingText))
        lastNotificationText = startingText
        scheduleRefresh(0)
        if (core != null || networkBinder != null) return

        val config = ConfigStore(this).load()
        relayConfig = config
        if (config.serverAddress.isBlank()) {
            status = errorStatus("请先填写 Relay Server 地址")
            updateNotificationIfChanged(force = true)
            scheduleRefresh(ERROR_REFRESH_MS)
            return
        }

        val networkLabel = when (config.networkMode) {
            NetworkBinder.MODE_WIFI -> "Wi-Fi"
            NetworkBinder.MODE_CELLULAR -> "移动数据"
            else -> "可用"
        }
        status = if (config.autoNetworkSwitch) {
            waitingStatus("等待可用网络（" + networkLabel + "优先）")
        } else {
            waitingStatus("等待" + networkLabel + "网络")
        }
        updateNotificationIfChanged(force = true)
        scheduleRefresh(WAITING_REFRESH_MS)

        val binder = NetworkBinder(this)
        networkBinder = binder
        binder.bind(
            mode = config.networkMode,
            autoSwitch = config.autoNetworkSwitch,
            onAvailable = onAvailable@{ activeMode ->
                if (networkBinder !== binder || destroyed) return@onAvailable
                activeNetworkMode = activeMode
                RelayVpnService.updateUnderlyingNetwork(this, NetworkBinder.currentProcessNetwork())
                startCore(relayConfig ?: config)
                applyP2PPowerProfile()
                requestRefreshSoon()
            },
            onLost = onLost@{
                if (networkBinder !== binder || destroyed) return@onLost
                activeNetworkMode = null
                RelayVpnService.updateUnderlyingNetwork(this, NetworkBinder.currentProcessNetwork())
                stopCoreOnly()
                status = if (config.autoNetworkSwitch) {
                    waitingStatus("网络不可用，正在自动切换")
                } else if (config.networkMode == NetworkBinder.MODE_AUTO) {
                    waitingStatus("网络已变化，正在重新连接")
                } else {
                    waitingStatus(networkLabel + "断开，等待恢复")
                }
                requestRefreshSoon()
            },
            onError = onError@{ message ->
                if (networkBinder !== binder || destroyed) return@onError
                activeNetworkMode = null
                stopCoreOnly()
                status = errorStatus(message)
                requestRefreshSoon()
            },
        )
    }

    private fun reconfigureRelay() {
        val config = ConfigStore(this).load()
        val previous = relayConfig
        relayConfig = config
        if (config.serverAddress.isBlank()) {
            stopCoreOnly()
            status = errorStatus("请先填写 Relay Server 地址")
            requestRefreshSoon()
            return
        }
        if (networkBinder == null) {
            startRelay()
            return
        }
        if (previous == null || previous.networkMode != config.networkMode ||
            previous.autoNetworkSwitch != config.autoNetworkSwitch
        ) {
            stopCoreOnly()
            val oldBinder = networkBinder
            networkBinder = null
            oldBinder?.release()
            activeNetworkMode = null
            startRelay()
            return
        }
        val running = core
        if (running != null && previous.copy(routing = config.routing, defaultExitId = config.defaultExitId) == config) {
            val result = runCatching {
                running.setRoutingConfig(config.routing.toJson(
                    forCore = true,
                    rejectUnknownApplications = config.vpnEnabled && Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q,
                ).toString())
                running.setDefaultExit(config.defaultExitId)
            }
            if (result.isFailure) {
                relayConfig = previous
                status = errorStatus(result.exceptionOrNull()?.message ?: "更新分流规则失败")
            }
            requestRefreshSoon()
            return
        }
        stopCoreOnly()
        if (activeNetworkMode != null) {
            startCore(config)
        }
    }

    private fun startCore(config: ExitConfig) {
        val generation = synchronized(this) {
            if (destroyed || core != null) return
            coreGeneration
        }
        executeCoreTask {
            val shouldStart = synchronized(this) {
                !destroyed && generation == coreGeneration && core == null
            }
            if (!shouldStart) return@executeCoreTask

            var client: Client? = null
            try {
                val identity = File(filesDir, "relayproxy/device-identity.json")
                val created = Androidcore.newClient(config.coreJson(), identity.absolutePath)
                client = created
                created.setPowerConstrained(shouldUseP2PLowPowerProfile())
                created.start()
                val accepted = synchronized(this) {
                    if (!destroyed && generation == coreGeneration && core == null) {
                        core = created
                        true
                    } else {
                        false
                    }
                }
                if (!accepted) {
                    runCatching { created.stop() }
                    return@executeCoreTask
                }
                status = decorateStatus(created.statusJSON())
            } catch (t: Throwable) {
                runCatching { client?.stop() }
                synchronized(this) {
                    if (core === client) core = null
                }
                if (isCurrentCoreGeneration(generation)) {
                    status = errorStatus(t.message ?: t.javaClass.simpleName)
                }
            }
            requestRefreshSoon()
        }
    }

    private fun stopCoreOnly() {
        val old = synchronized(this) {
            coreGeneration++
            val value = core
            core = null
            value
        }
        executeCoreTask { runCatching { old?.stop() } }
    }

    private fun stopRelay(startId: Int) {
        handler.removeCallbacks(refresh)
        val (old, generation) = synchronized(this) {
            coreGeneration++
            val value = core
            core = null
            value to coreGeneration
        }
        val binder = networkBinder
        networkBinder = null
        activeNetworkMode = null
        runCatching { binder?.release() }
        executeCoreTask {
            runCatching { old?.stop() }
            handler.post {
                if (!isCurrentCoreGeneration(generation) || shouldRunCore(ConfigStore(this))) {
                    return@post
                }
                status = JSONObject()
                    .put("connectionState", "STOPPED")
                    .put("approvalState", "unknown")
                    .toString()
                serviceStartedAtElapsed = 0
                lastNotificationText = null
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelfResult(startId)
            }
        }
    }

    private fun isCurrentCoreGeneration(generation: Long): Boolean = synchronized(this) {
        !destroyed && generation == coreGeneration
    }

    private fun executeCoreTask(task: () -> Unit) {
        runCatching { executor.execute(task) }
    }

    private fun registerPowerStateReceiver() {
        if (powerReceiverRegistered) return
        val filter = IntentFilter().apply {
            addAction(Intent.ACTION_SCREEN_ON)
            addAction(Intent.ACTION_SCREEN_OFF)
            addAction(PowerManager.ACTION_POWER_SAVE_MODE_CHANGED)
            addAction(PowerManager.ACTION_DEVICE_IDLE_MODE_CHANGED)
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            registerReceiver(powerStateReceiver, filter, Context.RECEIVER_NOT_EXPORTED)
        } else {
            @Suppress("DEPRECATION")
            registerReceiver(powerStateReceiver, filter)
        }
        powerReceiverRegistered = true
    }

    private fun unregisterPowerStateReceiver() {
        if (!powerReceiverRegistered) return
        powerReceiverRegistered = false
        runCatching { unregisterReceiver(powerStateReceiver) }
    }

    private fun shouldUseP2PLowPowerProfile(): Boolean =
        powerManager.isPowerSaveMode ||
            powerManager.isDeviceIdleMode ||
            !powerManager.isInteractive ||
            activeNetworkMode == NetworkBinder.MODE_CELLULAR

    private fun applyP2PPowerProfile() {
        val constrained = shouldUseP2PLowPowerProfile()
        core?.setPowerConstrained(constrained)
    }

    private fun createNotificationChannel() {
        val manager = getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(
            NotificationChannel(
                CHANNEL_ID,
                "RelayProxy 网络服务",
                NotificationManager.IMPORTANCE_LOW,
            )
        )
        manager.createNotificationChannel(
            NotificationChannel(
                MESSAGE_CHANNEL_ID,
                "RelayProxy 消息提醒",
                NotificationManager.IMPORTANCE_HIGH,
            ).apply {
                description = "按服务端消息规则显示验证码、普通消息和重要提醒"
            }
        )
    }

    private fun drainMessages(client: Client) {
        val raw = runCatching { client.popMessagesJSON() }.getOrDefault("[]")
        val messages = runCatching { JSONArray(raw) }.getOrNull() ?: return
        for (index in 0 until messages.length()) {
            val message = messages.optJSONObject(index) ?: continue
            if (!message.optBoolean("popup", false)) continue
            if (ConfigStore(this).isGlobalMessageOverlayEnabled()) {
                messageOverlayController.enqueue(message)
            } else if (uiVisible) {
                sendBroadcast(
                    Intent(ACTION_MESSAGE)
                        .setPackage(packageName)
                        .putExtra(EXTRA_MESSAGE_JSON, message.toString())
                )
            } else {
                showMessageNotification(message)
            }
        }
    }

    private fun applyGlobalMessageOverlaySetting() {
        if (!ConfigStore(this).isGlobalMessageOverlayEnabled() &&
            ::messageOverlayController.isInitialized
        ) {
            messageOverlayController.dismissAll()
        }
        requestRefreshSoon()
    }

    private fun showMessageNotification(message: JSONObject) {
        val raw = message.toString()
        val popupType = message.optString("popupType", "verification_code")
        val title = message.optString("title", "RelayProxy 消息").ifBlank { "RelayProxy 消息" }
        val content = message.optString("content", "")
        val code = message.optString("verificationCode", "")
        val displayTitle = if (popupType == "important") "重要提醒 · $title" else title
        val displayText = when (popupType) {
            "verification_code" -> if (code.isNotBlank()) "验证码 $code · $content" else content
            else -> content
        }.ifBlank { "收到一条新消息" }

        val openApp = PendingIntent.getActivity(
            this,
            message.optString("id", raw).hashCode(),
            Intent(this, MainActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP)
                .putExtra(EXTRA_MESSAGE_JSON, raw),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val notification = Notification.Builder(this, MESSAGE_CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_relayproxy)
            .setContentTitle(displayTitle)
            .setContentText(displayText)
            .setStyle(Notification.BigTextStyle().bigText(displayText))
            .setContentIntent(openApp)
            .setAutoCancel(true)
            .build()
        getSystemService(NotificationManager::class.java)
            .notify(20_000 + (message.optString("id", raw).hashCode() and 0x3fff), notification)
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
            .setContentTitle("RelayProxy 网络服务")
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
        val streams = (obj?.optLong("activeStreams", 0) ?: 0) +
            (obj?.optLong("proxyActiveTcp", 0) ?: 0) +
            (obj?.optLong("proxyActiveUdp", 0) ?: 0)
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
        val streams = (obj?.optLong("activeStreams", 0) ?: 0) +
            (obj?.optLong("proxyActiveTcp", 0) ?: 0) +
            (obj?.optLong("proxyActiveUdp", 0) ?: 0)
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

    private fun decorateStatus(raw: String): String = runCatching {
        val obj = JSONObject(raw)
        val activeMode = activeNetworkMode
        if (activeMode.isNullOrBlank()) {
            obj.remove("activeNetwork")
        } else {
            obj.put("activeNetwork", activeMode)
        }
        obj.put("powerConstrained", shouldUseP2PLowPowerProfile())
        obj.toString()
    }.getOrElse { raw }

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
