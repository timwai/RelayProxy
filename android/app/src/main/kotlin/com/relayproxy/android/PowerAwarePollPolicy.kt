package com.relayproxy.android

/**
 * Diagnostic snapshots are for UI/notifications, not packet forwarding.
 * Native VPN, relay connections, reconnection and push listeners stay running.
 */
internal object PowerAwarePollPolicy {
    private const val VISIBLE_MS = 1_000L
    private const val CONNECTING_MS = 10_000L
    private const val BACKGROUND_ACTIVE_MS = 15_000L
    private const val BACKGROUND_IDLE_MS = 60_000L
    private const val SCREEN_OFF_MS = 120_000L

    fun relayRefreshMs(
        uiVisible: Boolean,
        interactive: Boolean,
        state: String,
        activeStreams: Long,
    ): Long {
        if (state == "STOPPED") return 0L
        if (uiVisible) return VISIBLE_MS
        if (!interactive) {
            return if (state == "CONNECTING") BACKGROUND_ACTIVE_MS else SCREEN_OFF_MS
        }
        return when (state) {
            "CONNECTING" -> CONNECTING_MS
            "CONNECTED" -> if (activeStreams > 0) BACKGROUND_ACTIVE_MS else BACKGROUND_IDLE_MS
            else -> BACKGROUND_IDLE_MS
        }
    }

    fun vpnMonitorMs(uiVisible: Boolean, interactive: Boolean): Long =
        when {
            uiVisible -> VISIBLE_MS
            interactive -> 30_000L
            else -> SCREEN_OFF_MS
        }
}
