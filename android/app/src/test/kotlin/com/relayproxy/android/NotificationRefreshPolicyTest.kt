package com.relayproxy.android

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class NotificationRefreshPolicyTest {
    private fun allowed(
        oldText: String? = "已连接 · 活跃连接 1",
        oldState: String? = "CONNECTED",
        newText: String = "已连接 · 活跃连接 2",
        newState: String = "CONNECTED",
        lastMs: Long = 1_000L,
        nowMs: Long = 2_000L,
        force: Boolean = false,
    ): Boolean = NotificationRefreshPolicy.shouldPost(
        force, oldText, oldState, newText, newState, lastMs, nowMs
    )

    @Test fun unchangedTextIsNotReposted() {
        assertFalse(allowed(newText = "已连接 · 活跃连接 1", nowMs = 10_000L))
    }

    @Test fun transientActiveCountChangesAreThrottled() {
        assertFalse(allowed())
        assertTrue(allowed(nowMs = 3_100L))
    }

    @Test fun criticalStateTransitionsAreNeverDelayed() {
        assertTrue(allowed(newText = "连接已断开", newState = "ERROR", nowMs = 1_010L))
        assertTrue(allowed(newText = "等待网络", newState = "WAITING_NETWORK", nowMs = 1_010L))
    }

    @Test fun forcedAndInitialUpdatesAlwaysProceed() {
        assertTrue(allowed(force = true, nowMs = 1_010L))
        assertTrue(allowed(oldText = null, oldState = null, nowMs = 1_010L))
    }
}
