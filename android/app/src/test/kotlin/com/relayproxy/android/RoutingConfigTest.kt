package com.relayproxy.android

import org.json.JSONObject
import org.junit.Assert.assertEquals
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
    @Test
    fun `core json preserves rule mode enabled state and order`() {
        val config = RoutingConfig(
            mode = "rule",
            defaultAction = "DIRECT",
            rules = listOf(
                RoutingRuleConfig(
                    id = "first",
                    name = "first",
                    enabled = false,
                    action = "REJECT",
                    targets = listOf("example.com"),
                ),
                RoutingRuleConfig(
                    id = "second",
                    name = "second",
                    enabled = true,
                    action = "PROXY",
                    exitId = "exit-b",
                    targets = listOf("*.example.com"),
                ),
            ),
        )

        val json = config.toJson(forCore = true)
        assertEquals("rule", json.getString("mode"))
        assertEquals("DIRECT", json.getString("default_action"))
        val rules = json.getJSONArray("rules")
        assertEquals(2, rules.length())
        assertEquals("first", rules.getJSONObject(0).getString("name"))
        assertFalse(rules.getJSONObject(0).getBoolean("enabled"))
        assertEquals("second", rules.getJSONObject(1).getString("name"))
        assertEquals("exit-b", rules.getJSONObject(1).getString("exit_id"))
    }

    @Test
    fun `stored json roundtrip preserves deletion and disabled rules`() {
        val original = RoutingConfig(
            mode = "rule",
            rules = listOf(
                RoutingRuleConfig(id = "keep", name = "keep", enabled = false),
                RoutingRuleConfig(id = "delete", name = "delete"),
            ),
        )
        val stored = JSONObject(original.toJson().toString())
        stored.put(
            "rules",
            org.json.JSONArray().put(stored.getJSONArray("rules").getJSONObject(0)),
        )

        val restored = RoutingConfig.fromJson(stored.toString())
        assertEquals(listOf("keep"), restored.rules.map { it.id })
        assertFalse(restored.rules.single().enabled)
    }


    @Test
    fun `core json includes configured proxy path mode`() {
        val json = JSONObject(
            ExitConfig(
                proxyPathMode = ExitConfig.PROXY_PATH_DIRECT_ONLY,
                proxyP2pEnabled = true,
            ).coreJson()
        )
        assertEquals(ExitConfig.PROXY_PATH_DIRECT_ONLY, json.getString("proxyPathMode"))
        assertTrue(json.getBoolean("proxyP2pEnabled"))
    }

}
