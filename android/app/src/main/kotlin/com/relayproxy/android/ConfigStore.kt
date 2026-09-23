package com.relayproxy.android

import android.content.Context
import org.json.JSONObject

data class ExitConfig(
    val serverAddress: String = "",
    val deviceName: String = "RelayProxy Android",
    val quicPort: Int = 443,
    val tcpPort: Int = 443,
    val transportMode: String = "auto",
    val tlsEnabled: Boolean = true,
    val insecureTls: Boolean = false,
    val allowPrivateNetwork: Boolean = false,
    val cellularOnly: Boolean = false,
) {
    fun coreJson(): String = JSONObject()
        .put("serverAddress", serverAddress.trim())
        .put("deviceName", deviceName.trim())
        .put("quicPort", quicPort)
        .put("tcpPort", tcpPort)
        .put("transportMode", transportMode)
        .put("tlsEnabled", tlsEnabled)
        .put("insecureTLS", insecureTls)
        .put("allowInternet", true)
        .put("allowPrivateNetwork", allowPrivateNetwork)
        .put("allowLoopback", false)
        .toString()
}

class ConfigStore(context: Context) {
    private val prefs = context.getSharedPreferences("relayproxy_android", Context.MODE_PRIVATE)

    fun load(): ExitConfig = ExitConfig(
        serverAddress = prefs.getString("serverAddress", "") ?: "",
        deviceName = prefs.getString("deviceName", "RelayProxy Android") ?: "RelayProxy Android",
        quicPort = prefs.getInt("quicPort", 443),
        tcpPort = prefs.getInt("tcpPort", 443),
        transportMode = prefs.getString("transportMode", "auto") ?: "auto",
        tlsEnabled = prefs.getBoolean("tlsEnabled", true),
        insecureTls = prefs.getBoolean("insecureTls", false),
        allowPrivateNetwork = prefs.getBoolean("allowPrivateNetwork", false),
        cellularOnly = prefs.getBoolean("cellularOnly", false),
    )

    fun save(config: ExitConfig) {
        prefs.edit()
            .putString("serverAddress", config.serverAddress.trim())
            .putString("deviceName", config.deviceName.trim())
            .putInt("quicPort", config.quicPort)
            .putInt("tcpPort", config.tcpPort)
            .putString("transportMode", config.transportMode)
            .putBoolean("tlsEnabled", config.tlsEnabled)
            .putBoolean("insecureTls", config.insecureTls)
            .putBoolean("allowPrivateNetwork", config.allowPrivateNetwork)
            .putBoolean("cellularOnly", config.cellularOnly)
            .apply()
    }
}
