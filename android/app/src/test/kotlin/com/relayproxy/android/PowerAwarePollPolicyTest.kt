package com.relayproxy.android

import org.junit.Assert.assertEquals
import org.junit.Test

class PowerAwarePollPolicyTest {
    @Test
    fun foregroundKeepsRealtimeStatistics() {
        assertEquals(1_000L, PowerAwarePollPolicy.relayRefreshMs(true, true, "CONNECTED", 0))
        assertEquals(1_000L, PowerAwarePollPolicy.vpnMonitorMs(true, true))
    }

    @Test
    fun idleBackgroundAvoidsSecondLevelPolling() {
        assertEquals(60_000L, PowerAwarePollPolicy.relayRefreshMs(false, true, "CONNECTED", 0))
        assertEquals(15_000L, PowerAwarePollPolicy.relayRefreshMs(false, true, "CONNECTED", 3))
        assertEquals(10_000L, PowerAwarePollPolicy.relayRefreshMs(false, true, "CONNECTING", 0))
        assertEquals(30_000L, PowerAwarePollPolicy.vpnMonitorMs(false, true))
    }

    @Test
    fun screenOffUsesDeepIdleIntervalsButDoesNotStopForwarding() {
        assertEquals(120_000L, PowerAwarePollPolicy.relayRefreshMs(false, false, "CONNECTED", 0))
        assertEquals(120_000L, PowerAwarePollPolicy.relayRefreshMs(false, false, "CONNECTED", 2))
        assertEquals(120_000L, PowerAwarePollPolicy.relayRefreshMs(false, false, "ERROR", 0))
        assertEquals(15_000L, PowerAwarePollPolicy.relayRefreshMs(false, false, "CONNECTING", 0))
        assertEquals(120_000L, PowerAwarePollPolicy.vpnMonitorMs(false, false))
    }

    @Test
    fun screenOffChecksVpnHealthButSkipsStatistics() {
        assertEquals(false, PowerAwarePollPolicy.shouldCollectVpnStats(false, false))
        assertEquals(true, PowerAwarePollPolicy.shouldCollectVpnStats(true, false))
        assertEquals(true, PowerAwarePollPolicy.shouldCollectVpnStats(false, true))
    }

    @Test
    fun stoppedServiceNeverRefreshes() {
        assertEquals(0L, PowerAwarePollPolicy.relayRefreshMs(true, true, "STOPPED", 0))
        assertEquals(0L, PowerAwarePollPolicy.relayRefreshMs(false, false, "STOPPED", 0))
    }
}
