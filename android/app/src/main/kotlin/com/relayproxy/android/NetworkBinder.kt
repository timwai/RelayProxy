package com.relayproxy.android

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest

class NetworkBinder(context: Context) {
    companion object {
        // MODE_AUTO is retained only for compatibility with older saved configs.
        const val MODE_AUTO = "auto"
        const val MODE_CELLULAR = "cellular"
        const val MODE_WIFI = "wifi"
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
    private var cellularRequestCallback: ConnectivityManager.NetworkCallback? = null

    private var boundNetwork: Network? = null
    private var preferredMode: String = MODE_WIFI
    private var automaticSwitch = false

    private var availableCallback: ((String) -> Unit)? = null
    private var lostCallback: (() -> Unit)? = null
    private var errorCallback: ((String) -> Unit)? = null

    fun bind(
        mode: String,
        autoSwitch: Boolean,
        onAvailable: (String) -> Unit,
        onLost: () -> Unit,
        onError: (String) -> Unit,
    ) {
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
            override fun onAvailable(network: Network) {
                val previous = boundNetwork
                if (previous == network) return

                boundNetwork = network
                val activeMode = detectMode(network)
                if (previous != null && previous != network) {
                    lostCallback?.invoke()
                }
                availableCallback?.invoke(activeMode)
            }

            override fun onLost(network: Network) {
                if (boundNetwork == network) {
                    boundNetwork = null
                    lostCallback?.invoke()
                }
            }
        }

        callbacks += cb
        try {
            connectivity.registerDefaultNetworkCallback(cb)
        } catch (t: Throwable) {
            callbacks.remove(cb)
            throw t
        }
    }

    private fun registerTransportObserver(mode: String) {
        val transport = transportForMode(mode)
        val request = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addTransportType(transport)
            .build()

        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                updateCandidate(network, mode)
            }

            override fun onCapabilitiesChanged(
                network: Network,
                networkCapabilities: NetworkCapabilities,
            ) {
                updateCandidate(network, mode, networkCapabilities)
            }

            override fun onLost(network: Network) {
                removeCandidate(network)
            }
        }

        callbacks += cb
        try {
            connectivity.registerNetworkCallback(request, cb)
        } catch (t: Throwable) {
            callbacks.remove(cb)
            throw t
        }
    }

    private fun seedExistingNetworks() {
        connectivity.allNetworks.forEach { network ->
            val capabilities = connectivity.getNetworkCapabilities(network) ?: return@forEach
            val mode = when {
                capabilities.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> MODE_WIFI
                capabilities.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> MODE_CELLULAR
                else -> return@forEach
            }

            if (mode == preferredMode || automaticSwitch) {
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
        capabilities: NetworkCapabilities? = connectivity.getNetworkCapabilities(network),
    ) {
        candidates[network] = Candidate(
            network = network,
            mode = mode,
            validated = capabilities?.let(::isUsable) == true,
        )
        reevaluate()
    }

    @Synchronized
    private fun removeCandidate(network: Network) {
        candidates.remove(network)
        reevaluate()
    }

    @Synchronized
    private fun reevaluate() {
        if (preferredMode == MODE_AUTO) return

        val preferred = candidates.values.firstOrNull {
            it.mode == preferredMode && it.validated
        }
        val fallback = if (automaticSwitch) {
            candidates.values.firstOrNull {
                it.mode == otherMode(preferredMode) && it.validated
            }
        } else {
            null
        }
        val selected = preferred ?: fallback

        if (preferredMode == MODE_WIFI && automaticSwitch) {
            if (preferred == null) {
                // Bring cellular up only while Wi-Fi is actually unavailable
                // or connected without validated Internet access.
                ensureCellularRequest()
            } else {
                // Wi-Fi recovered. Release the explicit cellular request after
                // switching back to reduce radio usage and battery impact.
                releaseCellularRequest()
            }
        }

        switchTo(selected)
    }

    private fun switchTo(candidate: Candidate?) {
        val previous = boundNetwork

        if (candidate == null) {
            if (previous != null) {
                boundNetwork = null
                connectivity.bindProcessToNetwork(null)
                lostCallback?.invoke()
            }
            return
        }

        if (previous == candidate.network) return

        if (!connectivity.bindProcessToNetwork(candidate.network)) {
            errorCallback?.invoke(
                "无法将 RelayProxy 进程绑定到" + networkLabel(candidate.mode) + "网络"
            )
            return
        }

        boundNetwork = candidate.network

        // Recreate the Relay tunnel whenever the selected path changes so all
        // sockets immediately use the new network instead of waiting for an
        // existing connection to time out.
        if (previous != null && previous != candidate.network) {
            lostCallback?.invoke()
        }
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

    private fun detectMode(network: Network): String {
        val capabilities = connectivity.getNetworkCapabilities(network) ?: return MODE_AUTO
        return when {
            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> MODE_WIFI
            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> MODE_CELLULAR
            else -> MODE_AUTO
        }
    }

    @Synchronized
    fun release() {
        connectivity.bindProcessToNetwork(null)

        releaseCellularRequest()
        callbacks.toList().forEach {
            runCatching { connectivity.unregisterNetworkCallback(it) }
        }
        callbacks.clear()
        candidates.clear()
        boundNetwork = null

        availableCallback = null
        lostCallback = null
        errorCallback = null
    }
}
