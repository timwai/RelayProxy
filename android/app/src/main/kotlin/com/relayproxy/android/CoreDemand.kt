package com.relayproxy.android

/**
 * An Android identity/configuration is not itself a reason to hold a network
 * connection. Only features the user explicitly enabled keep Relay running.
 */
internal data class CoreDemand(
    val exitEnabled: Boolean,
    val vpnEnabled: Boolean,
    val localProxyEnabled: Boolean,
) {
    val hasWork: Boolean
        get() = exitEnabled || vpnEnabled || localProxyEnabled

    fun shouldRun(hasValidConnection: Boolean): Boolean = hasWork && hasValidConnection
}
