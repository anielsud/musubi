package com.musubi.phone.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.util.Base64

class PairingTest {
    private fun seq(from: Int, n: Int) = ByteArray(n) { (from + it).toByte() }

    /** The Mac's vector (docs/phone.md on phone-mac, TestPairCodeVector). */
    @Test fun macVector() {
        val phone = Base64.getDecoder().decode("ICEiIyQlJicoKSorLC0uLw==")
        val mac = Base64.getDecoder().decode("MDEyMzQ1Njc4OTo7PD0+Pw==")
        assertTrue(phone.contentEquals(seq(0x20, 16)))
        assertTrue(mac.contentEquals(seq(0x30, 16)))
        assertEquals("452561", PairCode.derive(seq(0, 32), phone, mac))
    }

    /** Vectors computed independently (Python hashlib). The second has a code with a high first byte. */
    @Test fun ownVectors() {
        assertEquals("321415", PairCode.derive(seq(0, 32), seq(0xa0, 16), seq(0xb0, 16)))
        assertEquals("751793", PairCode.derive(ByteArray(32) { -1 }, ByteArray(16), ByteArray(16) { -1 }))
    }

    @Test fun codeIsAlwaysSixDigitsAndDependsOnEveryInput() {
        val base = PairCode.derive(seq(0, 32), seq(0x20, 16), seq(0x30, 16))
        assertEquals(6, base.length)
        assertFalse(base == PairCode.derive(seq(1, 32), seq(0x20, 16), seq(0x30, 16)))
        assertFalse(base == PairCode.derive(seq(0, 32), seq(0x21, 16), seq(0x30, 16)))
        assertFalse(base == PairCode.derive(seq(0, 32), seq(0x20, 16), seq(0x31, 16)))
        // Swapping the nonces must change the code (order is part of the contract).
        assertFalse(base == PairCode.derive(seq(0, 32), seq(0x30, 16), seq(0x20, 16)))
        repeat(200) { assertTrue(PairCode.derive(ByteArray(32) { _ -> it.toByte() }, PairCode.newNonce(), PairCode.newNonce()).matches(Regex("^\\d{6}$"))) }
    }

    @Test fun derivationRejectsWrongSizes() {
        for ((c, p, m) in listOf(Triple(31, 16, 16), Triple(32, 15, 16), Triple(32, 16, 17))) {
            try { PairCode.derive(ByteArray(c), ByteArray(p), ByteArray(m)); fail() } catch (_: IllegalArgumentException) {}
        }
    }

    @Test fun fingerprintBytesAndDisplay() {
        val hex = Hashing.hex(seq(0, 32))
        assertTrue(PairCode.fingerprintBytes(hex).contentEquals(seq(0, 32)))
        assertEquals("452 561", PairCode.display("452561"))
        try { PairCode.fingerprintBytes("zz"); fail() } catch (_: IllegalArgumentException) {}
    }

    @Test fun requestBodyCarriesBase64Nonce() {
        val o = org.json.JSONObject(String(PairParse.requestBody("Test Phone", seq(0x20, 16))))
        assertEquals("ICEiIyQlJicoKSorLC0uLw==", o.getString("nonce"))
        assertEquals("Test Phone", o.getString("device_name"))
    }

    @Test fun requestReplyIsStrict() {
        val ok = PairParse.requestReply("""{"request_id":"r-1_A","nonce":"MDEyMzQ1Njc4OTo7PD0+Pw=="}""".toByteArray())
        assertEquals("r-1_A", ok.requestId)
        val bad = listOf(
            """{"request_id":"../x","nonce":"MDEyMzQ1Njc4OTo7PD0+Pw=="}""",
            """{"request_id":"a/b","nonce":"MDEyMzQ1Njc4OTo7PD0+Pw=="}""",
            """{"request_id":"","nonce":"MDEyMzQ1Njc4OTo7PD0+Pw=="}""",
            """{"request_id":"a","nonce":"AAAA"}""",
            """{"request_id":"a","nonce":"not base64!"}""",
            """{"request_id":"a","nonce":12}""",
            """{"request_id":"a"}""",
            """[]""",
            """{"request_id":"${"a".repeat(129)}","nonce":"MDEyMzQ1Njc4OTo7PD0+Pw=="}""",
        )
        for (b in bad) {
            try { PairParse.requestReply(b.toByteArray()); fail(b) } catch (_: Exception) {}
        }
    }

    @Test fun pairStates() {
        assertTrue(PairParse.state("""{"state":"pending"}""".toByteArray()) === PairState.Pending)
        assertTrue(PairParse.state("""{"state":"denied"}""".toByteArray()) === PairState.Denied)
        assertTrue(PairParse.state("""{"state":"expired"}""".toByteArray()) === PairState.Expired)
        val p = PairParse.state("""{"state":"paired","device_id":"d1","token":"abcdefghijklmnopqrstuvwxyz012345"}""".toByteArray())
        assertEquals("d1", (p as PairState.Paired).reply.deviceId)
        for (b in listOf("""{"state":"paired","device_id":"d1"}""", """{"state":"paired","device_id":"d 1","token":"abcdefghijklmnopqrstuvwxyz012345"}""",
            """{"state":"allowed"}""", """{}""", """{"state":"paired","device_id":"d1","token":"short"}""")) {
            try { PairParse.state(b.toByteArray()); fail(b) } catch (_: Exception) {}
        }
    }

    @Test fun discoveryTxt() {
        fun attrs(vararg kv: Pair<String, String?>) = kv.associate { it.first to it.second?.toByteArray() }
        assertEquals("0123456789abcdef", DiscoveryParse.fingerprint(attrs("v" to "1", "fp" to "0123456789ABCDEF")))
        assertNull(DiscoveryParse.fingerprint(attrs("v" to "2", "fp" to "0123456789abcdef")))
        assertNull(DiscoveryParse.fingerprint(attrs("fp" to "0123456789abcdef")))
        assertNull(DiscoveryParse.fingerprint(attrs("v" to "1", "fp" to "0123456789abcde")))
        assertNull(DiscoveryParse.fingerprint(attrs("v" to "1", "fp" to "0123456789abcdeg")))
        assertNull(DiscoveryParse.fingerprint(attrs("v" to "1", "fp" to null)))
        val full = "0123456789abcdef" + "0".repeat(48)
        assertTrue(DiscoveryParse.matches(full, "0123456789ABCDEF"))
        assertFalse(DiscoveryParse.matches(full, "0123456789abcdee"))
        assertFalse(DiscoveryParse.matches("0123456789abcdef", "0123456789abcdef"))
        assertEquals("Musubi on Test Mac", DiscoveryParse.cleanName(" Musubi on Test Mac "))
        assertNull(DiscoveryParse.cleanName("bad\u0000name"))
        assertNull(DiscoveryParse.cleanName("x".repeat(64)))
    }
}
