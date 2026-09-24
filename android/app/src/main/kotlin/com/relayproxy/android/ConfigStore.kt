package com.relayproxy.android

import android.content.Context
import android.os.Build
import android.provider.Settings
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
    val networkMode: String = NetworkBinder.MODE_AUTO,
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

class ConfigStore(private val context: Context) {
    private val prefs = context.getSharedPreferences("relayproxy_android", Context.MODE_PRIVATE)

    fun defaultDeviceName(): String {
        val systemName = runCatching {
            Settings.Global.getString(context.contentResolver, Settings.Global.DEVICE_NAME)
        }.getOrNull()?.trim().orEmpty()
        if (systemName.isNotBlank()) return systemName

        val manufacturer = Build.MANUFACTURER?.trim().orEmpty()
        val model = Build.MODEL?.trim().orEmpty()
        if (model.isBlank()) return "Android"
        if (manufacturer.isBlank() || model.startsWith(manufacturer, ignoreCase = true)) {
            return model
        }
        return manufacturer.replaceFirstChar {
            if (it.isLowerCase()) it.titlecase() else it.toString()
        } + " " + model
    }

    fun load(): ExitConfig {
        val savedNetworkMode = prefs.getString("networkMode", null)
        val migratedNetworkMode = savedNetworkMode ?: if (
            prefs.getBoolean("cellularOnly", false)
        ) {
            NetworkBinder.MODE_CELLULAR
        } else {
            NetworkBinder.MODE_AUTO
        }

        val savedDeviceName = prefs.getString("deviceName", null)?.trim().orEmpty()
        val resolvedDeviceName = if (
            savedDeviceName.isBlank() || savedDeviceName == "RelayProxy Android"
        ) {
            defaultDeviceName()
        } else {
            savedDeviceName
        }

        return ExitConfig(
            serverAddress = prefs.getString("serverAddress", "") ?: "",
            deviceName = resolvedDeviceName,
            quicPort = prefs.getInt("quicPort", 443),
            tcpPort = prefs.getInt("tcpPort", 443),
            transportMode = prefs.getString("transportMode", "auto") ?: "auto",
            tlsEnabled = prefs.getBoolean("tlsEnabled", true),
            insecureTls = prefs.getBoolean("insecureTls", false),
            allowPrivateNetwork = prefs.getBoolean("allowPrivateNetwork", false),
            networkMode = migratedNetworkMode,
        )
    }

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
            .putString("networkMode", config.networkMode)
            .remove("cellularOnly")
            .apply()
    }

    fun isDesiredRunning(): Boolean = prefs.getBoolean("desiredRunning", false)

    fun setDesiredRunning(running: Boolean) {
        prefs.edit().putBoolean("desiredRunning", running).apply()
    }
}
