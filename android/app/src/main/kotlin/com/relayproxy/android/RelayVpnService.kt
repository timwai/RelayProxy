package com.relayproxy.android

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.content.Context
import android.net.Network
import android.net.VpnService
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.ParcelFileDescriptor
import android.os.SystemClock
import hev.htproxy.TProxyService
import org.json.JSONObject
import java.io.File
import java.net.InetSocketAddress
import java.net.Socket
import java.util.concurrent.Executors

class RelayVpnService : VpnService() {
    companion object {
        const val ACTION_START = "com.relayproxy.android.VPN_START"
        const val ACTION_STOP = "com.relayproxy.android.VPN_STOP"
        const val ACTION_RECONFIGURE = "com.relayproxy.android.VPN_RECONFIGURE"
        private const val ACTION_UNDERLYING_NETWORK = "com.relayproxy.android.VPN_UNDERLYING_NETWORK"
        private const val EXTRA_UNDERLYING_NETWORK = "underlyingNetwork"
        private const val CHANNEL_ID = "relayproxy_vpn"
        private const val NOTIFICATION_ID = 1002
        private const val MAPPED_DNS_ADDRESS = "198.18.0.2"
        private const val MAPPED_DNS_NETWORK = "198.19.0.0"
        private const val MAPPED_DNS_NETMASK = "255.255.0.0"

        @Volatile
        private var status = JSONObject().put("vpnState", "STOPPED").toString()

        fun statusJson(): String = status

        fun isRunning(): Boolean = runCatching {
            JSONObject(status).optString("vpnState") == "RUNNING"
        }.getOrDefault(false)

        fun updateUnderlyingNetwork(context: Context, network: Network?) {
            if (!ConfigStore(context).isVpnDesiredRunning()) return
            val state = runCatching { JSONObject(status).optString("vpnState") }.getOrDefault("")
            if (state != "STARTING" && state != "RUNNING") return
            val intent = Intent(context, RelayVpnService::class.java)
                .setAction(ACTION_UNDERLYING_NETWORK)
                .putExtra(EXTRA_UNDERLYING_NETWORK, network)
            runCatching { context.startService(intent) }
        }
    }

    private val executor = Executors.newSingleThreadExecutor()
    private val handler = Handler(Looper.getMainLooper())
    private val statusLock = Any()
    @Volatile
    private var tun: ParcelFileDescriptor? = null

    @Volatile
    private var startInProgress = false

    private var operationGeneration = 0L

    @Volatile
    private var activeGeneration = -1L

    @Volatile
    private var destroyed = false

    private val nativeMonitor = object : Runnable {
        override fun run() {
            val generation = activeGeneration
            if (tun == null || !isCurrentGeneration(generation)) return
            if (!runCatching { TProxyService.TProxyIsRunning() }.getOrDefault(false)) {
                ConfigStore(this@RelayVpnService).setVpnDesiredRunning(false)
                scheduleStop(
                    stopOwnedRelay = true,
                    errorDetail = "VPN 转发进程已退出",
                )
                return
            }
            updateTunnelStats()
            handler.postDelayed(this, 1_000)
        }
    }

    override fun onCreate() {
        super.onCreate()
        val manager = getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(
            NotificationChannel(CHANNEL_ID, "RelayProxy VPN", NotificationManager.IMPORTANCE_LOW)
        )
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val store = ConfigStore(this)
        when (intent?.action) {
            ACTION_STOP -> {
                store.setVpnDesiredRunning(false)
                scheduleStop(stopOwnedRelay = true, startId = startId)
            }
            ACTION_START -> {
                store.setVpnDesiredRunning(true)
                if (statusState() != "RUNNING" &&
                    !(statusState() == "STARTING" && startInProgress)
                ) {
                    scheduleStart()
                }
            }
            ACTION_RECONFIGURE -> {
                store.setVpnDesiredRunning(true)
                scheduleStart()
            }
            ACTION_UNDERLYING_NETWORK -> updateUnderlyingNetworkFrom(intent)
            else -> if (store.isVpnDesiredRunning()) {
                if (statusState() != "RUNNING" && !startInProgress) scheduleStart()
            } else {
                stopSelf()
            }
        }
        return START_STICKY
    }

    override fun onRevoke() {
        ConfigStore(this).setVpnDesiredRunning(false)
        scheduleStop(stopOwnedRelay = true)
        super.onRevoke()
    }

    override fun onDestroy() {
        handler.removeCallbacks(nativeMonitor)
        synchronized(this) {
            destroyed = true
            operationGeneration++
        }
        executeVpnTask { stopNativeTunnel() }
        executor.shutdown()
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = super.onBind(intent)

    private fun scheduleStart() {
        val generation = nextGeneration()
        startInProgress = true
        activeGeneration = -1L
        handler.removeCallbacks(nativeMonitor)
        startForeground(NOTIFICATION_ID, buildNotification("正在启动 VPN"))
        setStatus("STARTING", "准备本机 SOCKS5 代理")
        executeVpnTask {
            stopNativeTunnel()
            if (!isCurrentGeneration(generation) ||
                !ConfigStore(this).isVpnDesiredRunning()
            ) {
                return@executeVpnTask
            }
            try {
                startTunnel(generation)
            } catch (t: Throwable) {
                stopNativeTunnel()
                val currentStore = ConfigStore(this)
                if (isCurrentGeneration(generation) && currentStore.isVpnDesiredRunning()) {
                    currentStore.setVpnDesiredRunning(false)
                    stopOwnedRelayIfNeeded()
                    setStatus("ERROR", t.message ?: t.javaClass.simpleName)
                    handler.post {
                        if (!isCurrentGeneration(generation)) return@post
                        stopForeground(STOP_FOREGROUND_REMOVE)
                        stopSelf()
                    }
                }
            } finally {
                if (isCurrentGeneration(generation)) startInProgress = false
            }
        }
    }

    private fun scheduleStop(
        stopOwnedRelay: Boolean,
        startId: Int? = null,
        errorDetail: String? = null,
    ) {
        val generation = nextGeneration()
        startInProgress = false
        activeGeneration = -1L
        handler.removeCallbacks(nativeMonitor)
        status = if (errorDetail == null) {
            JSONObject().put("vpnState", "STOPPED").toString()
        } else {
            JSONObject()
                .put("vpnState", "ERROR")
                .put("detail", errorDetail)
                .toString()
        }
        executeVpnTask {
            stopNativeTunnel()
            if (stopOwnedRelay) stopOwnedRelayIfNeeded()
            handler.post {
                if (!isCurrentGeneration(generation) ||
                    ConfigStore(this).isVpnDesiredRunning()
                ) {
                    return@post
                }
                getSystemService(NotificationManager::class.java).cancel(NOTIFICATION_ID)
                stopForeground(STOP_FOREGROUND_REMOVE)
                if (startId == null) stopSelf() else stopSelfResult(startId)
            }
        }
    }

    private fun startTunnel(generation: Long) {
        val store = ConfigStore(this)
        val config = store.load()
        check(isCurrentGeneration(generation) && store.isVpnDesiredRunning()) {
            "VPN 已取消启动"
        }
        require(config.serverAddress.isNotBlank()) { "请先填写 Relay Server 地址" }
        val proxyPort = if (config.clientEnabled && config.socks5Enabled) {
            config.socks5Port
        } else {
            config.vpnSocks5Port
        }

        startRelayService(RelayExitService.ACTION_RECONFIGURE)

        if (isCurrentGeneration(generation)) {
            setStatus("STARTING", "等待 Relay SOCKS5 代理启动")
        }
        check(waitForProxy(proxyPort, 30_000, generation)) {
            "代理尚未就绪；请检查 Relay 连接、proxy.client 授权和出口选择"
        }
        check(isCurrentGeneration(generation) && store.isVpnDesiredRunning()) {
            "VPN 已取消启动"
        }

        val configFile = File(filesDir, "relayproxy/vpn-tun.yml")
        configFile.parentFile?.mkdirs()
        configFile.writeText(
            """
            tunnel:
              name: relayproxy
              mtu: 1280
              ipv4: 198.18.0.1
              ipv6: 'fc00::1'
              icmp: 'off'
            socks5:
              address: 127.0.0.1
              port: $proxyPort
              udp: 'udp'
            mapdns:
              address: $MAPPED_DNS_ADDRESS
              port: 53
              network: $MAPPED_DNS_NETWORK
              netmask: $MAPPED_DNS_NETMASK
              cache-size: 10000
            """.trimIndent() + "\n"
        )

        val builder = Builder()
            .setSession("RelayProxy")
            .setMtu(1280)
            .addAddress("198.18.0.1", 32)
            .addAddress("fc00::1", 128)
            .addRoute("0.0.0.0", 0)
            .addRoute("::", 0)
            .setConfigureIntent(
                PendingIntent.getActivity(
                    this,
                    2,
                    Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
                    PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
                )
            )
        // Use hev-socks5-tunnel Mapped DNS instead of sending plaintext DNS
        // queries from the selected exit to public UDP/53 resolvers. Mapped DNS
        // preserves the original hostname and hands it to the RelayProxy SOCKS5
        // server, so the selected exit resolves the target on its own network.
        builder.addDnsServer(MAPPED_DNS_ADDRESS)
        applyApplicationScope(builder, config)
        NetworkBinder.currentProcessNetwork()?.let { builder.setUnderlyingNetworks(arrayOf(it)) }
        val descriptor = builder.establish() ?: error("Android 没有建立 VPN 接口")

        if (!isCurrentGeneration(generation) || !store.isVpnDesiredRunning()) {
            descriptor.close()
            return
        }
        tun = descriptor
        if (!TProxyService.TProxyStartService(configFile.absolutePath, descriptor.fd)) {
            tun = null
            descriptor.close()
            error("启动 SOCKS5 VPN 转发失败")
        }
        SystemClock.sleep(250)
        check(TProxyService.TProxyIsRunning()) { "SOCKS5 VPN 转发未能运行" }
        if (!isCurrentGeneration(generation) || !store.isVpnDesiredRunning()) {
            stopNativeTunnel()
            return
        }
        activeGeneration = generation
        setStatus(
            "RUNNING",
            when (config.vpnAppMode) {
                ExitConfig.VPN_APP_MODE_INCLUDE -> "选中应用已通过 Relay SOCKS5 转发"
                ExitConfig.VPN_APP_MODE_EXCLUDE -> "已排除选中应用，其余流量通过 Relay 转发"
                else -> "全局流量已通过 Relay SOCKS5 转发"
            },
        )
        updateTunnelStats()
        handler.post(nativeMonitor)
    }

    private fun startRelayService(action: String) {
        val intent = Intent(this, RelayExitService::class.java).setAction(action)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(intent)
        } else {
            startService(intent)
        }
    }

    private fun waitForProxy(port: Int, timeoutMs: Long, generation: Long): Boolean {
        val deadline = SystemClock.elapsedRealtime() + timeoutMs
        while (SystemClock.elapsedRealtime() < deadline) {
            if (!isCurrentGeneration(generation) ||
                !ConfigStore(this).isVpnDesiredRunning()
            ) {
                return false
            }
            val relayReady = runCatching {
                val relay = JSONObject(RelayExitService.statusJson())
                val proxyState = relay.optString("proxyState")
                relay.optString("connectionState") == "CONNECTED" &&
                    relay.optBoolean("clientApproved", false) &&
                    (proxyState == "ready" || proxyState == "legacy")
            }.getOrDefault(false)
            if (!relayReady) {
                SystemClock.sleep(250)
                continue
            }
            runCatching {
                Socket().use { socket ->
                    socket.connect(InetSocketAddress("127.0.0.1", port), 500)
                }
            }.onSuccess { return true }
            SystemClock.sleep(250)
        }
        return false
    }

    @Synchronized
    private fun nextGeneration(): Long {
        operationGeneration++
        return operationGeneration
    }

    @Synchronized
    private fun isCurrentGeneration(generation: Long): Boolean =
        !destroyed && generation == operationGeneration

    private fun executeVpnTask(task: () -> Unit) {
        runCatching { executor.execute(task) }
    }

    private fun statusState(): String = runCatching {
        JSONObject(status).optString("vpnState")
    }.getOrDefault("")

    private fun applyApplicationScope(builder: Builder, config: ExitConfig) {
        when (config.vpnAppMode) {
            ExitConfig.VPN_APP_MODE_INCLUDE -> {
                var added = 0
                config.vpnPackages.filterNot { it == packageName }.forEach { pkg ->
                    runCatching { builder.addAllowedApplication(pkg) }
                        .onSuccess { added++ }
                }
                check(added > 0) { "VPN 仅选中应用模式没有可用应用，请重新选择" }
            }
            ExitConfig.VPN_APP_MODE_EXCLUDE -> {
                builder.addDisallowedApplication(packageName)
                config.vpnPackages.filterNot { it == packageName }.forEach { pkg ->
                    runCatching { builder.addDisallowedApplication(pkg) }
                }
            }
            else -> builder.addDisallowedApplication(packageName)
        }
    }

    @Suppress("DEPRECATION")
    private fun updateUnderlyingNetworkFrom(intent: Intent) {
        val network = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            intent.getParcelableExtra(EXTRA_UNDERLYING_NETWORK, Network::class.java)
        } else {
            intent.getParcelableExtra(EXTRA_UNDERLYING_NETWORK)
        }
        val networks = network?.let { arrayOf(it) }
        runCatching { setUnderlyingNetworks(networks) }
        updateStatusFields {
            if (network == null) remove("underlyingNetwork")
            else put("underlyingNetwork", network.networkHandle.toString())
        }
    }

    private fun updateTunnelStats() {
        val values = runCatching { TProxyService.TProxyGetStats() }.getOrNull() ?: return
        if (values.size < 4) return
        updateStatusFields {
            put("tunTxPackets", values[0])
            put("tunTxBytes", values[1])
            put("tunRxPackets", values[2])
            put("tunRxBytes", values[3])
        }
    }

    private fun updateStatusFields(update: JSONObject.() -> Unit) {
        synchronized(statusLock) {
            val current = runCatching { JSONObject(status) }.getOrElse { JSONObject() }
            current.update()
            status = current.toString()
        }
    }

    private fun stopNativeTunnel() {
        runCatching {
            if (TProxyService.TProxyIsRunning()) TProxyService.TProxyStopService()
        }
        val old = tun
        tun = null
        runCatching { old?.close() }
    }

    private fun stopOwnedRelayIfNeeded() {
        val store = ConfigStore(this)
        val intent = Intent(this, RelayExitService::class.java)
        if (store.isDesiredRunning() || store.load().clientEnabled) {
            intent.action = RelayExitService.ACTION_RECONFIGURE
        } else {
            intent.action = RelayExitService.ACTION_STOP
        }
        runCatching { startService(intent) }
    }

    private fun setStatus(state: String, detail: String) {
        updateStatusFields {
            put("vpnState", state)
            put("detail", detail)
        }
        handler.post {
            val notification = buildNotification(
                when (state) {
                    "RUNNING" -> "VPN 已连接"
                    "ERROR" -> detail
                    else -> detail
                }
            )
            if (state == "RUNNING" || state == "STARTING") {
                getSystemService(NotificationManager::class.java).notify(NOTIFICATION_ID, notification)
            }
        }
    }

    private fun buildNotification(text: String): Notification {
        val openApp = PendingIntent.getActivity(
            this,
            1,
            Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return Notification.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_relayproxy)
            .setContentTitle("RelayProxy VPN")
            .setContentText(text)
            .setContentIntent(openApp)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .build()
    }
}
