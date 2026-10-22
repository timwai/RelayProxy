package com.relayproxy.android

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.LinkProperties
import android.net.NetworkRequest
import android.os.Handler
import android.os.Looper
import android.util.Log

class NetworkBinder(context: Context) {
    companion object {
        // MODE_AUTO is retained only for compatibility with older saved configs.
        const val MODE_AUTO = "auto"
        const val MODE_CELLULAR = "cellular"
        const val MODE_WIFI = "wifi"

        @Volatile
        private var processNetwork: Network? = null

        fun currentProcessNetwork(): Network? = processNetwork
    }

    private data class Candidate(
        val network: Network,
        val mode: String,
        val validated: Boolean,
    )

    private val connectivity =
        context.applicationContext.getSystemService(ConnectivityManager::class.java)

    private val callbacks = mutableListOf<ConnectivityManager.NetworkCallback>()
    private val candidates = mutableMapOf<Network, Candidate>()
    private val linkSignatures = mutableMapOf<Network, String>()
    private val handler = Handler(Looper.getMainLooper())
    private var pendingPromotion: Network? = null
    private var promotionTask: Runnable? = null
    private var promotionDelayMs = 1_500L
    private var linkChangeTask: Runnable? = null
    private var legacyDefault: Network? = null
    private var cellularRequestCallback: ConnectivityManager.NetworkCallback? = null

    private var boundNetwork: Network? = null
    private var preferredMode: String = MODE_WIFI
    private var automaticSwitch = false
    private var released = false

    private var availableCallback: ((String) -> Unit)? = null
    private var lostCallback: (() -> Unit)? = null
    private var errorCallback: ((String) -> Unit)? = null

    @Synchronized
    fun bind(
        mode: String,
        autoSwitch: Boolean,
        onAvailable: (String) -> Unit,
        onLost: () -> Unit,
        onError: (String) -> Unit,
    ) {
        if (released) return
        if (callbacks.isNotEmpty() || cellularRequestCallback != null) return

        preferredMode = when (mode) {
            MODE_CELLULAR -> MODE_CELLULAR
            MODE_WIFI -> MODE_WIFI
            else -> MODE_AUTO
        }
        automaticSwitch = autoSwitch
        availableCallback = onAvailable
        lostCallback = onLost
        errorCallback = onError

        if (preferredMode == MODE_AUTO) {
            bindLegacyDefaultNetwork()
            return
        }

        try {
            registerTransportObserver(preferredMode)

            if (automaticSwitch) {
                registerTransportObserver(otherMode(preferredMode))
            }

            // Seed already-connected networks before deciding whether cellular
            // must be explicitly requested. This avoids waking the cellular
            // radio when a validated preferred Wi-Fi is already available.
            seedExistingNetworks()

            if (preferredMode == MODE_CELLULAR) {
                // Cellular is not necessarily kept active while Wi-Fi is the
                // Android default, so explicitly request it when cellular is
                // the selected primary path.
                ensureCellularRequest()
            }

            reevaluate()
        } catch (t: Throwable) {
            release()
            onError(t.message ?: "请求出口网络失败")
        }
    }

    private fun bindLegacyDefaultNetwork() {
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onCapabilitiesChanged(network: Network, capabilities: NetworkCapabilities) {
                synchronized(this@NetworkBinder) {
                    legacyDefault = network
                    updateCandidate(network, modeFor(capabilities), capabilities)
                }
            }
            override fun onLinkPropertiesChanged(network: Network, properties: LinkProperties) {
                updateLink(network, properties)
            }
            override fun onLost(network: Network) { removeCandidate(network) }
        }
        callbacks += cb
        try { connectivity.registerDefaultNetworkCallback(cb) }
        catch (t: Throwable) { callbacks.remove(cb); throw t }
    }

    private fun registerTransportObserver(mode: String) {
        val transport = transportForMode(mode)
        val request = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addTransportType(transport)
            .build()

        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onCapabilitiesChanged(
                network: Network,
                networkCapabilities: NetworkCapabilities,
            ) {
                updateCandidate(network, mode, networkCapabilities)
            }

            override fun onLinkPropertiesChanged(network: Network, properties: LinkProperties) {
                updateLink(network, properties)
            }
            override fun onLost(network: Network) { removeCandidate(network) }
        }

        callbacks += cb
        try {
            connectivity.registerNetworkCallback(request, cb)
        } catch (t: Throwable) {
            callbacks.remove(cb)
            throw t
        }
    }

    @Synchronized
    private fun seedExistingNetworks() {
        if (released) return
        connectivity.allNetworks.forEach { network ->
            val capabilities = connectivity.getNetworkCapabilities(network) ?: return@forEach
            val mode = when {
                capabilities.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> MODE_WIFI
                capabilities.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> MODE_CELLULAR
                else -> return@forEach
            }

            if (mode == preferredMode || automaticSwitch) {
                connectivity.getLinkProperties(network)?.let { linkSignatures[network] = linkSignature(it) }
                candidates[network] = Candidate(
                    network = network,
                    mode = mode,
                    validated = isUsable(capabilities),
                )
            }
        }
    }

    @Synchronized
    private fun updateCandidate(
        network: Network,
        mode: String,
        capabilities: NetworkCapabilities,
    ) {
        if (released) return
        candidates[network] = Candidate(
            network = network,
            mode = mode,
            validated = isUsable(capabilities),
        )
        reevaluate()
    }

    @Synchronized
    private fun removeCandidate(network: Network) {
        if (released) return
        candidates.remove(network)
        linkSignatures.remove(network)
        if (legacyDefault == network) legacyDefault = null
        reevaluate()
    }

    @Synchronized
    private fun reevaluate() {
        if (released) return
        if (preferredMode == MODE_AUTO) {
            switchTo(candidates[legacyDefault]?.takeIf { it.validated && linkSignatures.containsKey(it.network) })
            return
        }

        val usable = candidates.values.filter { it.validated && linkSignatures.containsKey(it.network) }
        val current = candidates[boundNetwork]?.takeIf { it in usable }
        val preferred = current?.takeIf { it.mode == preferredMode } ?: usable.firstOrNull {
            it.mode == preferredMode && it.validated
        }
        val fallback = if (automaticSwitch) {
            current?.takeIf { it.mode == otherMode(preferredMode) } ?: usable.firstOrNull {
                it.mode == otherMode(preferredMode)
            }
        } else {
            null
        }
        val selected = preferred ?: fallback

        // A healthy fallback keeps carrying traffic while preferred Wi-Fi
        // proves stable. Actual path loss switches immediately.
        if (current != null && selected != null && current.network != selected.network) {
            if (pendingPromotion != selected.network) {
                cancelPromotion()
                pendingPromotion = selected.network
                promotionTask = Runnable {
                    synchronized(this) {
                        val ready = candidates[selected.network]
                        if (!released && ready?.validated == true && linkSignatures.containsKey(selected.network)) {
                            switchTo(ready)
                        }
                        pendingPromotion = null
                        promotionTask = null
                        reevaluate()
                    }
                }.also { handler.postDelayed(it, promotionDelayMs) }
            }
        } else {
            cancelPromotion()
            switchTo(selected)
        }

        if (preferredMode == MODE_WIFI && automaticSwitch) {
            if (preferred == null) {
                // Bring cellular up only while Wi-Fi is actually unavailable
                // or connected without validated Internet access.
                ensureCellularRequest()
            } else if (boundNetwork == preferred.network) {
                // Wi-Fi recovered. Release the explicit cellular request after
                // switching back to reduce radio usage and battery impact.
                releaseCellularRequest()
            }
        }
    }

    private fun switchTo(candidate: Candidate?) {
        val previous = boundNetwork

        if (candidate == null) {
            if (previous != null) {
                boundNetwork = null
                processNetwork = null
                connectivity.bindProcessToNetwork(null)
                lostCallback?.invoke()
            }
            return
        }

        if (previous == candidate.network) return
        linkChangeTask?.let { handler.removeCallbacks(it) }
        linkChangeTask = null

        if (preferredMode != MODE_AUTO && !connectivity.bindProcessToNetwork(candidate.network)) {
            val message = "无法将 RelayProxy 进程绑定到" + networkLabel(candidate.mode) + "网络"
            if (candidates[previous]?.validated == true) {
                // A failed promotion must not tear down the healthy fallback.
                promotionDelayMs = 30_000L
                Log.w("RelayProxy", message)
            } else {
                errorCallback?.invoke(message)
            }
            return
        }

        promotionDelayMs = 1_500L
        boundNetwork = candidate.network
        processNetwork = candidate.network

        // Core preserves its listeners and stream recovery registry; the
        // callback replaces only transports after new sockets are rebound.
        availableCallback?.invoke(candidate.mode)
    }

    private fun ensureCellularRequest() {
        if (cellularRequestCallback != null) return

        val request = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addTransportType(NetworkCapabilities.TRANSPORT_CELLULAR)
            .build()

        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onUnavailable() {
                // The passive cellular observer remains registered. If cellular
                // becomes available later it will still be selected automatically.
                reevaluate()
            }
        }
        cellularRequestCallback = cb

        try {
            connectivity.requestNetwork(request, cb)
        } catch (t: Throwable) {
            cellularRequestCallback = null
            errorCallback?.invoke(t.message ?: "请求移动数据网络失败")
        }
    }

    private fun releaseCellularRequest() {
        val cb = cellularRequestCallback ?: return
        cellularRequestCallback = null
        runCatching { connectivity.unregisterNetworkCallback(cb) }
    }

    private fun isUsable(capabilities: NetworkCapabilities): Boolean =
        capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) &&
            capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)

    private fun transportForMode(mode: String): Int = when (mode) {
        MODE_CELLULAR -> NetworkCapabilities.TRANSPORT_CELLULAR
        else -> NetworkCapabilities.TRANSPORT_WIFI
    }

    private fun otherMode(mode: String): String =
        if (mode == MODE_CELLULAR) MODE_WIFI else MODE_CELLULAR

    private fun networkLabel(mode: String): String = when (mode) {
        MODE_WIFI -> "Wi-Fi"
        MODE_CELLULAR -> "移动数据"
        else -> "默认"
    }

    private fun modeFor(capabilities: NetworkCapabilities): String = when {
        capabilities.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> MODE_WIFI
        capabilities.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> MODE_CELLULAR
        else -> MODE_AUTO
    }

    private fun linkSignature(p: LinkProperties): String = listOf(
        p.interfaceName.orEmpty(), p.linkAddresses.map { it.toString() }.sorted().joinToString(),
        p.dnsServers.map { it.hostAddress.orEmpty() }.sorted().joinToString(),
        p.routes.map { it.toString() }.sorted().joinToString(), p.mtu.toString(),
    ).joinToString("|")

    @Synchronized
    private fun updateLink(network: Network, properties: LinkProperties) {
        if (released) return
        val signature = linkSignature(properties)
        val previous = linkSignatures.put(network, signature)
        if (boundNetwork == network && previous != null && previous != signature) {
            linkChangeTask?.let { handler.removeCallbacks(it) }
            linkChangeTask = Runnable {
                synchronized(this) {
                    linkChangeTask = null
                    if (!released && boundNetwork == network) {
                        availableCallback?.invoke(candidates[network]?.mode ?: MODE_AUTO)
                    }
                }
            }.also { handler.postDelayed(it, 250L) }
        }
        reevaluate()
    }

    private fun cancelPromotion() {
        promotionTask?.let { handler.removeCallbacks(it) }
        promotionTask = null
        pendingPromotion = null
    }

    @Synchronized
    fun release() {
        if (released) return
        released = true
        cancelPromotion()
        linkChangeTask?.let { handler.removeCallbacks(it) }
        linkChangeTask = null
        linkSignatures.clear()
        legacyDefault = null
        connectivity.bindProcessToNetwork(null)

        releaseCellularRequest()
        callbacks.toList().forEach {
            runCatching { connectivity.unregisterNetworkCallback(it) }
        }
        callbacks.clear()
        candidates.clear()
        boundNetwork = null
        processNetwork = null

        availableCallback = null
        lostCallback = null
        errorCallback = null
    }
}
