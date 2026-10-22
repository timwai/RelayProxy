package com.relayproxy.android

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class CoreDemandTest {
    @Test
    fun noFeatureMeansNoBackgroundConnectionEvenWithIdentityConfigured() {
        val demand = CoreDemand(exitEnabled = false, vpnEnabled = false, localProxyEnabled = false)
        assertFalse(demand.hasWork)
        assertFalse(demand.shouldRun(hasValidConnection = true))
    }

    @Test
    fun anyEnabledFeatureKeepsCoreAlive() {
        val exit = CoreDemand(exitEnabled = true, vpnEnabled = false, localProxyEnabled = false)
        val vpn = CoreDemand(exitEnabled = false, vpnEnabled = true, localProxyEnabled = false)
        val proxy = CoreDemand(exitEnabled = false, vpnEnabled = false, localProxyEnabled = true)
        assertTrue(exit.shouldRun(true))
        assertTrue(vpn.shouldRun(true))
        assertTrue(proxy.shouldRun(true))
        assertTrue(CoreDemand(true, true, true).shouldRun(true))
    }

    @Test
    fun invalidIdentityNeverStartsIdleConnection() {
        assertFalse(CoreDemand(true, false, false).shouldRun(false))
        assertFalse(CoreDemand(false, true, false).shouldRun(false))
        assertFalse(CoreDemand(false, false, true).shouldRun(false))
    }

    @Test
    fun stoppingExitKeepsVpnOrLocalProxyOnlyWhenTheyAreEnabled() {
        assertTrue(CoreDemand(false, true, false).shouldRun(true))
        assertTrue(CoreDemand(false, false, true).shouldRun(true))
        assertFalse(CoreDemand(false, false, false).shouldRun(true))
    }
}
