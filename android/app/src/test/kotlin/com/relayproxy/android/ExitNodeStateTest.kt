package com.relayproxy.android

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class ExitNodeStateTest {
    private fun status(connection: String = "CONNECTED", approved: Boolean = true) = JSONObject("""
        {"connectionState":"$connection","clientApproved":$approved,"proxyExits":[
          {"deviceId":"online","online":true},
          {"deviceId":"offline","online":false},
          {"deviceId":"legacy"},
          {"deviceId":"","online":true}
        ]}
    """)

    @Test
    fun onlyExplicitlyOnlineAuthorizedExitsCanBeTested() {
        val state = ExitNodeState.from(status())
        assertEquals(setOf("online"), state.online)
        assertTrue(state.canProbe("online"))
        for (id in listOf("offline", "legacy", "removed", "")) {
            assertFalse(state.canProbe(id))
            assertEquals("离线", state.presence(id))
        }
        assertEquals("在线", state.presence("online"))
    }

    @Test
    fun cachedOnlineInventoryBecomesUnknownWhenDisconnected() {
        for (connection in listOf("CONNECTING", "DISCONNECTED", "CLOSED", "")) {
            val state = ExitNodeState.from(status(connection))
            assertFalse(state.canProbe("online"))
            assertEquals("状态未知", state.presence("online"))
        }
        assertFalse(ExitNodeState.from(null).canProbe("online"))
    }

    @Test
    fun approvalAndEnabledClientAreRequiredEvenForOnlineExits() {
        assertFalse(ExitNodeState.from(status(approved = false)).canProbe("online"))
        assertFalse(ExitNodeState.from(status(), clientEnabled = false).canProbe("online"))
    }

    @Test
    fun offlineRefreshImmediatelyRemovesProbeTargets() {
        val initial = status()
        assertTrue(ExitNodeState.from(initial).canProbe("online"))
        initial.getJSONArray("proxyExits").getJSONObject(0).put("online", false)
        val offline = ExitNodeState.from(initial)
        assertFalse(offline.canProbe("online"))
        assertEquals("离线", offline.presence("online"))
        assertTrue(offline.online.isEmpty())
    }

    @Test
    fun measurementMustMatchTheRequestedExitAndHaveARealPath() {
        val raw = """{"exitId":"online","latencyMs":37,"path":"relay_tls"}"""
        assertEquals(ExitLatency(37, "relay_tls"), ExitLatency.parse(raw, "online"))
        assertThrows(IllegalArgumentException::class.java) { ExitLatency.parse(raw, "other") }
        assertThrows(IllegalArgumentException::class.java) { ExitLatency.parse(raw.replace("37", "0"), "online") }
        assertThrows(IllegalArgumentException::class.java) { ExitLatency.parse(raw.replace("relay_tls", "unknown"), "online") }
    }

    @Test
    fun lanLabelRequiresMeasuredLanPathMetadata() {
        val measured = ExitLatency.parse("""{"exitId":"online","latencyMs":3,"path":"p2p_quic","pathInfo":{"network":"lan"}}""", "online")
        assertEquals("局域网直连 QUIC", ExitLatency.pathLabel(measured.path, measured.network))
        assertEquals("P2P QUIC", ExitLatency.pathLabel("p2p_quic"))
        assertEquals("中继 TLS", ExitLatency.pathLabel("relay_tls", "lan"))
    }
}
