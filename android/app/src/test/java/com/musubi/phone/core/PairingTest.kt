package com.musubi.phone.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

class PairingTest {
    private fun bytes(from: Int, n: Int) = ByteArray(n) { (from + it).toByte() }

    @Test
    fun codeMatchesTheDocsVector() {
        // docs/phone.md: Mac cert 00..1f, phone cert 20..3f, Mac nonce 40..4f, phone nonce 50..5f → 999120.
        assertEquals("999120", PairCode.derive(bytes(0x00, 32), bytes(0x20, 32), bytes(0x40, 16), bytes(0x50, 16)))
        assertEquals("999 120", PairCode.display("999120"))
    }

    @Test
    fun codeDependsOnEveryInput() {
        val base = PairCode.derive(bytes(0, 32), bytes(0x20, 32), bytes(0x40, 16), bytes(0x50, 16))
        assertTrue(base != PairCode.derive(bytes(1, 32), bytes(0x20, 32), bytes(0x40, 16), bytes(0x50, 16)))
        assertTrue(base != PairCode.derive(bytes(0, 32), bytes(0x21, 32), bytes(0x40, 16), bytes(0x50, 16)))
        // Swapping the roles changes the code: a relay cannot reflect one side's values.
        assertTrue(base != PairCode.derive(bytes(0x20, 32), bytes(0, 32), bytes(0x50, 16), bytes(0x40, 16)))
        assertTrue(runCatching { PairCode.derive(bytes(0, 31), bytes(0x20, 32), bytes(0x40, 16), bytes(0x50, 16)) }.isFailure)
    }

    private class Clock { var t = 0L }

    private fun flow(c: Clock, paired: MutableList<String> = mutableListOf()) =
        PairingFlow({ c.t }) { _, name -> paired += name }

    private fun request(f: PairingFlow, conn: Any) =
        f.request(conn, "Test Mac", bytes(0, 32), bytes(0x20, 32), bytes(0x40, 16), bytes(0x50, 16))

    @Test
    fun requestNeedsAnOpenWindowAndOnlyOneMacAtATime() {
        val c = Clock()
        val f = flow(c)
        assertEquals(PairingFlow.RequestResult.Refused("not_pairing"), request(f, Any()))
        f.open()
        val a = Any()
        assertEquals(PairingFlow.RequestResult.Ok("999120"), request(f, a))
        assertEquals(PairingFlow.RequestResult.Refused("busy"), request(f, Any()))
    }

    @Test
    fun ownerConfirmsThenMacConfirmsPairs() {
        val c = Clock()
        val names = mutableListOf<String>()
        val f = flow(c, names)
        f.open()
        val conn = Any()
        request(f, conn)
        f.ownerConfirm()
        assertEquals(PairingFlow.Decision.OK, f.macConfirm(conn))
        assertEquals(listOf("Test Mac"), names)
        assertEquals(PairingFlow.State.Paired("Test Mac"), f.state())
        assertTrue(!f.isOpen())
    }

    @Test
    fun macConfirmWaitsForTheOwner() {
        val c = Clock()
        val f = flow(c)
        f.open()
        val conn = Any()
        request(f, conn)
        var decision: PairingFlow.Decision? = null
        val done = CountDownLatch(1)
        Thread { decision = f.macConfirm(conn); done.countDown() }.start()
        assertTrue(!done.await(300, TimeUnit.MILLISECONDS))
        f.ownerConfirm()
        assertTrue(done.await(5, TimeUnit.SECONDS))
        assertEquals(PairingFlow.Decision.OK, decision)
    }

    @Test
    fun ownerCancelAnswersCancelled() {
        val c = Clock()
        val names = mutableListOf<String>()
        val f = flow(c, names)
        f.open()
        val conn = Any()
        request(f, conn)
        f.ownerCancel()
        assertEquals(PairingFlow.Decision.CANCELLED, f.macConfirm(conn))
        assertTrue(names.isEmpty())
        assertEquals(PairingFlow.State.Failed(PairingFlow.Failure.CANCELLED), f.state())
    }

    @Test
    fun windowAndCodeExpire() {
        val c = Clock()
        val f = flow(c)
        f.open()
        c.t = PairingFlow.WINDOW_MS
        assertEquals(PairingFlow.State.Failed(PairingFlow.Failure.EXPIRED), f.state())
        assertEquals(PairingFlow.RequestResult.Refused("not_pairing"), request(f, Any()))

        f.open()
        val conn = Any()
        request(f, conn)
        c.t += PairingFlow.DECIDE_MS
        assertEquals(PairingFlow.Decision.EXPIRED, f.macConfirm(conn))
    }

    @Test
    fun onlyTheAskingConnectionConfirms() {
        val c = Clock()
        val names = mutableListOf<String>()
        val f = flow(c, names)
        f.open()
        val conn = Any()
        request(f, conn)
        f.ownerConfirm()
        assertEquals(PairingFlow.Decision.EXPIRED, f.macConfirm(Any()))
        assertTrue(names.isEmpty())
    }

    @Test
    fun macDenyAndDisconnectFail() {
        val c = Clock()
        val f = flow(c)
        f.open()
        val conn = Any()
        request(f, conn)
        f.macCancel(Any()) // another connection cannot cancel it
        assertTrue(f.state() is PairingFlow.State.Code)
        f.macCancel(conn)
        assertEquals(PairingFlow.State.Failed(PairingFlow.Failure.DENIED), f.state())

        f.open()
        val conn2 = Any()
        request(f, conn2)
        f.connectionClosed(conn2)
        assertEquals(PairingFlow.State.Failed(PairingFlow.Failure.DISCONNECTED), f.state())
    }
}
