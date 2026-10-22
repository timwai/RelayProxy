package com.relayproxy.android

/**
 * Only repeated noncritical notification text changes are throttled.
 * State transitions and explicit failures must still be shown immediately.
 */
internal object NotificationRefreshPolicy {
    const val MIN_UPDATE_INTERVAL_MS = 2_000L

    fun shouldPost(
        force: Boolean,
        previousText: String?,
        previousState: String?,
        newText: String,
        newState: String,
        lastPostedMs: Long,
        nowMs: Long,
    ): Boolean {
        if (force || previousText == null || previousState == null) return true
        if (previousText == newText) return false
        if (previousState != newState) return true
        return nowMs - lastPostedMs >= MIN_UPDATE_INTERVAL_MS
    }
}
