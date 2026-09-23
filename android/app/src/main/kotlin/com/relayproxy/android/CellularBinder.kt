package com.relayproxy.android

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest

class CellularBinder(context: Context) {
    private val connectivity =
        context.applicationContext.getSystemService(ConnectivityManager::class.java)

    private var callback: ConnectivityManager.NetworkCallback? = null
    private var boundNetwork: Network? = null

    fun bind(onAvailable: () -> Unit, onLost: () -> Unit, onError: (String) -> Unit) {
        if (callback != null) return

        val request = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addTransportType(NetworkCapabilities.TRANSPORT_CELLULAR)
            .build()

        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                boundNetwork = network
                if (connectivity.bindProcessToNetwork(network)) {
                    onAvailable()
                } else {
                    onError("无法将 RelayProxy 进程绑定到移动数据网络")
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
                onError("移动数据网络不可用")
            }
        }

        callback = cb
        try {
            connectivity.requestNetwork(request, cb)
        } catch (t: Throwable) {
            callback = null
            onError(t.message ?: "请求移动数据网络失败")
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
