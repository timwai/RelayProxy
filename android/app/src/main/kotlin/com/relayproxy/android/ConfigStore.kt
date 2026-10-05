package com.relayproxy.android

import android.content.Context
import android.os.Build
import android.provider.Settings
import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

data class RoutingRuleConfig(
    val id: String = UUID.randomUUID().toString(),
    val name: String = "新规则",
    val enabled: Boolean = true,
    val action: String = "PROXY",
    val exitId: String = "",
    val applications: List<String> = emptyList(),
    val targets: List<String> = emptyList(),
    val ports: List<String> = emptyList(),
    val protocols: List<String> = emptyList(),
) {
    fun toJson(forCore: Boolean): JSONObject = JSONObject()
        .put("id", if (forCore) null else id)
        .put("name", name.trim())
        .put("enabled", enabled)
        .put("action", action)
        .put("exit_id", exitId.trim())
        .put(if (forCore) "processes" else "applications", applications.toJsonArray())
        .put("targets", targets.toJsonArray())
        .put("ports", ports.toJsonArray())
        .put("protocols", protocols.toJsonArray())
}

data class RoutingConfig(
    val mode: String = "global_proxy",
    val defaultAction: String = "PROXY",
    val rules: List<RoutingRuleConfig> = emptyList(),
    val revision: Long = 0,
) {
    fun requiresApplicationIdentity(androidSdk: Int): Boolean =
        androidSdk >= Build.VERSION_CODES.Q && mode == "rule" &&
            rules.any { rule -> rule.enabled && rule.applications.isNotEmpty() }

    fun toJson(
        forCore: Boolean = false,
        rejectUnknownApplications: Boolean = false,
    ): JSONObject = JSONObject()
        .put("mode", mode)
        .put("default_action", defaultAction)
        .put("revision", if (forCore) null else revision)
        .put("rules", JSONArray().apply {
            if (forCore && rejectUnknownApplications && rules.any {
                    it.enabled && it.applications.isNotEmpty()
                }
            ) {
                put(
                    RoutingRuleConfig(
                        id = "android-unknown-application",
                        name = "未知应用保护",
                        action = "REJECT",
                        applications = listOf(ANDROID_UNKNOWN_PROCESS),
                    ).toJson(forCore = true)
                )
            }
            rules.forEach { put(it.toJson(forCore)) }
        })

    companion object {
        fun fromJson(raw: String?): RoutingConfig {
            if (raw.isNullOrBlank()) return RoutingConfig()
            return runCatching {
                val json = JSONObject(raw)
                val rulesJson = json.optJSONArray("rules") ?: JSONArray()
                val rules = buildList {
                    for (index in 0 until rulesJson.length()) {
                        val item = rulesJson.optJSONObject(index) ?: continue
                        add(
                            RoutingRuleConfig(
                                id = item.optString("id").ifBlank { UUID.randomUUID().toString() },
                                name = item.optString("name", "规则 ${index + 1}"),
                                enabled = item.optBoolean("enabled", true),
                                action = item.optString("action", "PROXY"),
                                exitId = item.optString("exit_id"),
                                applications = item.stringList("applications").ifEmpty {
                                    item.stringList("processes")
                                },
                                targets = item.stringList("targets"),
                                ports = item.stringList("ports"),
                                protocols = item.stringList("protocols"),
                            )
                        )
                    }
                }
                RoutingConfig(
                    mode = json.optString("mode", "global_proxy"),
                    defaultAction = json.optString("default_action", "PROXY"),
                    rules = rules,
                    revision = json.optLong("revision", 0),
                )
            }.getOrDefault(RoutingConfig())
        }
    }
}

private fun List<String>.toJsonArray() = JSONArray().also { array ->
    forEach { array.put(it) }
}

private fun JSONObject.stringList(name: String): List<String> {
    val array = optJSONArray(name) ?: return emptyList()
    return buildList {
        for (index in 0 until array.length()) {
            val value = array.optString(index).trim()
            if (value.isNotBlank()) add(value)
        }
    }.distinct()
}

data class ExitConfig(
    val serverAddress: String = "",
    val identityId: String = "",
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
    val proxyPathMode: String = PROXY_PATH_AUTO,
    val vpnEnabled: Boolean = false,
    val vpnSocks5Port: Int = 1081,
    val vpnAppMode: String = VPN_APP_MODE_ALL,
    val vpnPackages: Set<String> = emptySet(),
    val vpnIpv6Enabled: Boolean = false,
    val vpnDnsServers: List<String> = listOf("1.1.1.1", "8.8.8.8"),
    val vpnProxyToken: String = "",
    val routing: RoutingConfig = RoutingConfig(),
) {
    fun coreJson(): String {
        require(
            Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q ||
                routing.rules.none { it.enabled && it.applications.isNotEmpty() }
        ) { "Android 8/9 不能启用包含应用条件的分流规则" }
        val localSocksEnabled = clientEnabled && socks5Enabled
        val coreClientEnabled = clientEnabled || vpnEnabled
        return JSONObject()
            .put("serverAddress", serverAddress.trim())
            .put("identityId", identityId.trim())
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
            .put("requestedCapabilities", org.json.JSONArray().apply {
                put("proxy.client")
                put("proxy.exit")
            })
            .put("socks5Enabled", localSocksEnabled)
            .put("httpEnabled", clientEnabled && httpEnabled)
            .put("proxyP2pEnabled", proxyP2pEnabled)
            .put("proxyPathMode", proxyPathMode)
            .put("defaultExitId", defaultExitId.trim())
            .put("socks5Listen", "127.0.0.1:$socks5Port")
            .put("httpListen", "127.0.0.1:$httpPort")
            .put("vpnProxyEnabled", vpnEnabled)
            .put("vpnProxyListen", "127.0.0.1:$vpnSocks5Port")
            .put("vpnProxyToken", vpnProxyToken)
            .put(
                "routing",
                routing.toJson(
                    forCore = true,
                    rejectUnknownApplications = vpnEnabled && Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q,
                )
            )
            .toString()
    }

    companion object {
        const val VPN_APP_MODE_ALL = "all"
        const val VPN_APP_MODE_INCLUDE = "include"
        const val VPN_APP_MODE_EXCLUDE = "exclude"
        const val PROXY_PATH_AUTO = "auto"
        const val PROXY_PATH_DIRECT_ONLY = "direct_only"
        const val PROXY_PATH_P2P_ONLY = "p2p_only"
        const val PROXY_PATH_RELAY_ONLY = "relay_only"
    }
}

class ConfigStore(private val context: Context) {
    private val prefs = context.getSharedPreferences("relayproxy_android", Context.MODE_PRIVATE)

    fun isDarkTheme(): Boolean = prefs.getBoolean("dark_theme", true)
    fun setDarkTheme(isDark: Boolean) {
        prefs.edit().putBoolean("dark_theme", isDark).apply()
    }

    fun hasConnectionConfig(config: ExitConfig = load()): Boolean =
        config.serverAddress.isNotBlank() &&
            Regex("^(?=.*[a-z])(?=.*[0-9])[a-z0-9]{16}$").matches(config.identityId)

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
            identityId = prefs.getString("identityId", "") ?: "",
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
            proxyPathMode = when (prefs.getString("proxyPathMode", ExitConfig.PROXY_PATH_AUTO)) {
                ExitConfig.PROXY_PATH_DIRECT_ONLY -> ExitConfig.PROXY_PATH_DIRECT_ONLY
                ExitConfig.PROXY_PATH_P2P_ONLY -> ExitConfig.PROXY_PATH_P2P_ONLY
                ExitConfig.PROXY_PATH_RELAY_ONLY -> ExitConfig.PROXY_PATH_RELAY_ONLY
                else -> ExitConfig.PROXY_PATH_AUTO
            },
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
            vpnIpv6Enabled = prefs.getBoolean("vpnIpv6Enabled", false),
            vpnDnsServers = prefs.getString("vpnDnsServers", "1.1.1.1,8.8.8.8")
                .orEmpty()
                .split(',', '\n', ';', ' ')
                .map(String::trim)
                .filter(String::isNotBlank)
                .distinct(),
            vpnProxyToken = SecretStore(context).vpnProxyToken(),
            routing = RoutingConfig.fromJson(prefs.getString("routing", null)),
        )
    }

    fun save(config: ExitConfig) {
        prefs.edit()
            .putString("serverAddress", config.serverAddress.trim())
            .putString("identityId", config.identityId.trim().lowercase())
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
            .putString("proxyPathMode", config.proxyPathMode)
            .putString("vpnAppMode", config.vpnAppMode)
            .putStringSet("vpnPackages", config.vpnPackages.toSet())
            .putBoolean("vpnIpv6Enabled", config.vpnIpv6Enabled)
            .putString("vpnDnsServers", config.vpnDnsServers.joinToString(","))
            .putInt("configVersion", 5)
            .remove("cellularOnly")
            .remove("vpnManagingRelay")
            .apply()
    }

    fun saveRouting(routing: RoutingConfig): RoutingConfig = synchronized(routingLock) {
        val current = RoutingConfig.fromJson(prefs.getString("routing", null))
        check(routing.revision == current.revision) { "规则已在其他页面修改，请返回列表后重新编辑" }
        val updated = routing.copy(revision = current.revision + 1)
        prefs.edit()
            .putString("routing", updated.toJson().toString())
            .putInt("configVersion", 5)
            .apply()
        updated
    }

    fun isDesiredRunning(): Boolean = prefs.getBoolean("desiredRunning", false)

    fun setDesiredRunning(running: Boolean) {
        prefs.edit().putBoolean("desiredRunning", running).apply()
    }

    fun isVpnDesiredRunning(): Boolean = prefs.getBoolean("vpnDesiredRunning", false)

    fun setVpnDesiredRunning(running: Boolean) {
        prefs.edit().putBoolean("vpnDesiredRunning", running).apply()
    }

    companion object {
        private val routingLock = Any()
    }

}

private const val ANDROID_UNKNOWN_PROCESS = "__android_unknown__"
