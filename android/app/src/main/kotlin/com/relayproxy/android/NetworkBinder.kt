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
            else -> {
                onAvailable()
                return
            }
        }
        val networkLabel = if (mode == MODE_WIFI) "Wi-Fi" else "移动数据"

        val request = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addTransportType(transport)
            .build()

        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                boundNetwork = network
                if (connectivity.bindProcessToNetwork(network)) {
                    onAvailable()
                } else {
                    onError("无法将 RelayProxy 进程绑定到$networkLabel网络")
                }
            }

            override fun onLost(network: Network) {
                if (boundNetwork == network) {
                    boundNetwork = null
                    connectivity.bindProcessToNetwork(null)
                    onLost()
                }
            }

            override fun onUnavailable() {
                onError("$networkLabel网络不可用")
            }
        }

        callback = cb
        try {
            connectivity.requestNetwork(request, cb)
        } catch (t: Throwable) {
            callback = null
            onError(t.message ?: "请求$networkLabel网络失败")
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
