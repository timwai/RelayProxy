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
    val networkMode: String = NetworkBinder.MODE_WIFI,
    val autoNetworkSwitch: Boolean = true,
    val exitEnabled: Boolean = false,
    val clientEnabled: Boolean = false,
    val socks5Enabled: Boolean = true,
    val httpEnabled: Boolean = false,
    val defaultExitId: String = "",
    val socks5Port: Int = 1080,
    val httpPort: Int = 8080,
    val proxyP2pEnabled: Boolean = true,
    val vpnEnabled: Boolean = false,
    val vpnSocks5Port: Int = 1081,
    val vpnAppMode: String = VPN_APP_MODE_ALL,
    val vpnPackages: Set<String> = emptySet(),
    val vpnDnsServers: List<String> = listOf("1.1.1.1", "8.8.8.8"),
) {
    fun coreJson(): String {
        val localSocksEnabled = clientEnabled && socks5Enabled
        val coreClientEnabled = clientEnabled || vpnEnabled
        return JSONObject()
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
            .put("exitEnabled", exitEnabled)
            .put("clientEnabled", coreClientEnabled)
            .put("socks5Enabled", localSocksEnabled || vpnEnabled)
            .put("httpEnabled", clientEnabled && httpEnabled)
            .put("proxyP2pEnabled", proxyP2pEnabled)
            .put("defaultExitId", defaultExitId.trim())
            .put("socks5Listen", "127.0.0.1:${if (localSocksEnabled) socks5Port else vpnSocks5Port}")
            .put("httpListen", "127.0.0.1:$httpPort")
            .toString()
    }

    companion object {
        const val VPN_APP_MODE_ALL = "all"
        const val VPN_APP_MODE_INCLUDE = "include"
        const val VPN_APP_MODE_EXCLUDE = "exclude"
    }
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
        val legacyCellularOnly = prefs.getBoolean("cellularOnly", false)

        // MODE_AUTO was the old "follow Android default network" behavior.
        // Migrate it to Wi-Fi preferred + automatic fallback, which keeps the
        // expected Wi-Fi-first behavior while adding deterministic failover.
        val resolvedNetworkMode = when (savedNetworkMode) {
            NetworkBinder.MODE_WIFI -> NetworkBinder.MODE_WIFI
            NetworkBinder.MODE_CELLULAR -> NetworkBinder.MODE_CELLULAR
            else -> if (legacyCellularOnly) {
                NetworkBinder.MODE_CELLULAR
            } else {
                NetworkBinder.MODE_WIFI
            }
        }
        val resolvedAutoNetworkSwitch = when {
            prefs.contains("autoNetworkSwitch") ->
                prefs.getBoolean("autoNetworkSwitch", true)
            savedNetworkMode == NetworkBinder.MODE_AUTO ->
                true
            savedNetworkMode == null && !legacyCellularOnly ->
                true
            else ->
                false
        }

        val savedDeviceName = prefs.getString("deviceName", null)?.trim().orEmpty()
        val resolvedDeviceName = if (
            savedDeviceName.isBlank() || savedDeviceName == "RelayProxy Android"
        ) {
            defaultDeviceName()
        } else {
            savedDeviceName
        }

        val clientEnabled = prefs.getBoolean("clientEnabled", false)
        val socks5Enabled = prefs.getBoolean("socks5Enabled", true)
        val httpEnabled = prefs.getBoolean("httpEnabled", false)
        val socks5Port = prefs.getInt("socks5Port", 1080)
        val httpPort = prefs.getInt("httpPort", 8080)
        val reservedPorts = buildSet {
            if (clientEnabled && socks5Enabled) add(socks5Port)
            if (clientEnabled && httpEnabled) add(httpPort)
        }
        var vpnSocks5Port = 1081
        while (vpnSocks5Port in reservedPorts && vpnSocks5Port < 65535) {
            vpnSocks5Port++
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
            networkMode = resolvedNetworkMode,
            autoNetworkSwitch = resolvedAutoNetworkSwitch,
            exitEnabled = isDesiredRunning(),
            clientEnabled = clientEnabled,
            socks5Enabled = socks5Enabled,
            httpEnabled = httpEnabled,
            defaultExitId = prefs.getString("defaultExitId", "") ?: "",
            socks5Port = socks5Port,
            httpPort = httpPort,
            proxyP2pEnabled = prefs.getBoolean("proxyP2pEnabled", true),
            vpnEnabled = isVpnDesiredRunning(),
            vpnSocks5Port = vpnSocks5Port,
            vpnAppMode = when (prefs.getString("vpnAppMode", ExitConfig.VPN_APP_MODE_ALL)) {
                ExitConfig.VPN_APP_MODE_INCLUDE -> ExitConfig.VPN_APP_MODE_INCLUDE
                ExitConfig.VPN_APP_MODE_EXCLUDE -> ExitConfig.VPN_APP_MODE_EXCLUDE
                else -> ExitConfig.VPN_APP_MODE_ALL
            },
            vpnPackages = prefs.getStringSet("vpnPackages", emptySet())
                ?.map(String::trim)
                ?.filter(String::isNotBlank)
                ?.toSet()
                .orEmpty(),
            vpnDnsServers = prefs.getString("vpnDnsServers", "1.1.1.1,8.8.8.8")
                .orEmpty()
                .split(',', '\n', ';', ' ')
                .map(String::trim)
                .filter(String::isNotBlank)
                .distinct(),
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
            .putBoolean("autoNetworkSwitch", config.autoNetworkSwitch)
            .putBoolean("clientEnabled", config.clientEnabled)
            .putBoolean("socks5Enabled", config.socks5Enabled)
            .putBoolean("httpEnabled", config.httpEnabled)
            .putString("defaultExitId", config.defaultExitId.trim())
            .putInt("socks5Port", config.socks5Port)
            .putInt("httpPort", config.httpPort)
            .putBoolean("proxyP2pEnabled", config.proxyP2pEnabled)
            .putString("vpnAppMode", config.vpnAppMode)
            .putStringSet("vpnPackages", config.vpnPackages.toSet())
            .putString("vpnDnsServers", config.vpnDnsServers.joinToString(","))
            .putInt("configVersion", 2)
            .remove("cellularOnly")
            .remove("vpnManagingRelay")
            .apply()
    }

    fun isDesiredRunning(): Boolean = prefs.getBoolean("desiredRunning", false)

    fun setDesiredRunning(running: Boolean) {
        prefs.edit().putBoolean("desiredRunning", running).apply()
    }

    fun isVpnDesiredRunning(): Boolean = prefs.getBoolean("vpnDesiredRunning", false)

    fun setVpnDesiredRunning(running: Boolean) {
        prefs.edit().putBoolean("vpnDesiredRunning", running).apply()
    }

}
