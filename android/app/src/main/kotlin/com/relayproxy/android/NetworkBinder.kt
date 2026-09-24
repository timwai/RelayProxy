package com.relayproxy.android

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest

class NetworkBinder(context: Context) {
    companion object {
        const val MODE_AUTO = "auto"
        const val MODE_CELLULAR = "cellular"
        const val MODE_WIFI = "wifi"
    }

    private val connectivity =
        context.applicationContext.getSystemService(ConnectivityManager::class.java)

    private var callback: ConnectivityManager.NetworkCallback? = null
    private var boundNetwork: Network? = null

    fun bind(
        mode: String,
        onAvailable: () -> Unit,
        onLost: () -> Unit,
        onError: (String) -> Unit,
    ) {
        if (callback != null) return

        val transport = when (mode) {
            MODE_CELLULAR -> NetworkCapabilities.TRANSPORT_CELLULAR
            MODE_WIFI -> NetworkCapabilities.TRANSPORT_WIFI
            else -> null
        }
        val networkLabel = when (mode) {
            MODE_WIFI -> "Wi-Fi"
            MODE_CELLULAR -> "移动数据"
            else -> "默认"
        }

        val request = transport?.let {
            NetworkRequest.Builder()
                .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                .addTransportType(it)
                .build()
        }

        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                if (boundNetwork == network) return

                val previous = boundNetwork
                boundNetwork = network

                if (mode != MODE_AUTO && !connectivity.bindProcessToNetwork(network)) {
                    onError("无法将 RelayProxy 进程绑定到${networkLabel}网络")
                    return
                }

                // A new matching/default network replaces the previous one.
                // Tear down the old tunnel first so sockets are recreated on
                // the new path immediately instead of waiting for heartbeat timeout.
                if (previous != null && previous != network) {
                    onLost()
                }
                onAvailable()
            }

            override fun onLost(network: Network) {
                if (boundNetwork == network) {
                    boundNetwork = null
                    if (mode != MODE_AUTO) {
                        connectivity.bindProcessToNetwork(null)
                    }
                    onLost()
                }
            }

            override fun onUnavailable() {
                onError("${networkLabel}网络不可用")
            }
        }

        callback = cb
        try {
            when (mode) {
                MODE_AUTO -> {
                    // Observe the system-selected default network. This is passive
                    // and lets Relay reconnect immediately on Wi-Fi/cellular changes.
                    connectivity.registerDefaultNetworkCallback(cb)
                }
                MODE_WIFI -> {
                    // Wi-Fi-only should be passive: wait for an existing Wi-Fi network
                    // instead of keeping an active network request that can increase
                    // scanning / radio wakeups while Wi-Fi is unavailable.
                    connectivity.registerNetworkCallback(requireNotNull(request), cb)
                }
                else -> {
                    // Cellular-only is an explicit request to keep cellular available
                    // as the Relay exit even when another default network is active.
                    connectivity.requestNetwork(requireNotNull(request), cb)
                }
            }
        } catch (t: Throwable) {
            callback = null
            onError(t.message ?: "请求${networkLabel}网络失败")
        }
    }

    fun release() {
        connectivity.bindProcessToNetwork(null)
        callback?.let {
            runCatching { connectivity.unregisterNetworkCallback(it) }
        }
        callback = null
        boundNetwork = null
    }
}
