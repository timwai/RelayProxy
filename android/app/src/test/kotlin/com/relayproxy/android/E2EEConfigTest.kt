package com.relayproxy.android

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Test

class E2EEConfigTest {
    @Test
    fun `core config defaults to opportunistic E2EE`() {
        val json = JSONObject(ExitConfig().coreJson())
        assertEquals(ExitConfig.E2EE_MODE_OPPORTUNISTIC, json.getString("e2eeMode"))
    }

    @Test
    fun `core config carries selected E2EE policy`() {
        val json = JSONObject(
            ExitConfig(e2eeMode = ExitConfig.E2EE_MODE_REQUIRED).coreJson()
        )
        assertEquals(ExitConfig.E2EE_MODE_REQUIRED, json.getString("e2eeMode"))
    }
}
