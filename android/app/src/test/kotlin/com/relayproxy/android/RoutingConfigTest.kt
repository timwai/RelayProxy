package com.relayproxy.android

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class RoutingConfigTest {
    private val applicationRule = RoutingRuleConfig(applications = listOf("org.telegram.messenger"))

    @Test
    fun `global proxy never requires application lookup`() {
        assertFalse(
            RoutingConfig(mode = "global_proxy", rules = listOf(applicationRule))
                .requiresApplicationIdentity(androidSdk = 35)
        )
    }

    @Test
    fun `rule mode only queries identity for active application conditions`() {
        assertFalse(RoutingConfig(mode = "rule").requiresApplicationIdentity(androidSdk = 35))
        assertFalse(
            RoutingConfig(mode = "rule", rules = listOf(applicationRule.copy(enabled = false)))
                .requiresApplicationIdentity(androidSdk = 35)
        )
        assertTrue(
            RoutingConfig(mode = "rule", rules = listOf(applicationRule))
                .requiresApplicationIdentity(androidSdk = 35)
        )
    }

    @Test
    fun `android 8 and 9 never attempt application lookup`() {
        assertFalse(
            RoutingConfig(mode = "rule", rules = listOf(applicationRule))
                .requiresApplicationIdentity(androidSdk = 28)
        )
    }
}
