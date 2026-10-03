package com.relayproxy.android

import org.junit.Assert.assertEquals
import org.junit.Test

class FlowOwnerIdentityTest {
    @Test fun sharedUidRetainsAllPackages() {
        assertEquals("com.example.a|com.example.b", FlowOwnerIdentity.encode(listOf("com.example.b", "com.example.a", "com.example.a")))
        assertEquals("android", FlowOwnerIdentity.encode(listOf("android")))
    }

    @Test fun oversizedGroupsFailClosedInsteadOfLosingRuleConditions() {
        assertEquals(FlowOwnerIdentity.UNKNOWN, FlowOwnerIdentity.encode((0..16).map { "com.app.p$it" }))
        assertEquals(FlowOwnerIdentity.UNKNOWN, FlowOwnerIdentity.encode(listOf("com." + "a".repeat(150), "com." + "b".repeat(150))))
    }

    @Test fun unknownAndInvalidPackagesStayUnknown() {
        for (packages in listOf(emptyList(), listOf("com..app"), listOf("com.1bad"), listOf("com.app|forged"))) {
            assertEquals(FlowOwnerIdentity.UNKNOWN, FlowOwnerIdentity.encode(packages))
        }
    }
}
