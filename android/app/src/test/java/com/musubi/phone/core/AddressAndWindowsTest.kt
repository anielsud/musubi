package com.musubi.phone.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.net.InetAddress

class AddressFilterTest {
    private fun ok(s: String) = AddressFilter.isAllowed(InetAddress.getByName(s))

    @Test
    fun privateAddressesOnly() {
        for (a in listOf("10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.254", "192.168.1.20",
            "100.64.0.1", "100.127.255.254", "169.254.10.10", "127.0.0.1",
            "::1", "fe80::1", "febf::1", "fd7a:115c:a1e0::1", "fd7a:115c:a1e0:ffff::1", "::ffff:192.168.1.5")) {
            assertTrue(a, ok(a))
        }
        for (a in listOf("8.8.8.8", "172.15.255.255", "172.32.0.1", "192.169.0.1", "100.63.255.255", "100.128.0.1",
            "11.0.0.1", "0.0.0.0", "255.255.255.255", "224.0.0.251",
            "2001:4860:4860::8888", "fd7a:115c:a1e1::1", "fd00::1", "fec0::1", "ff02::fb", "::ffff:8.8.8.8", "::")) {
            assertFalse(a, ok(a))
        }
    }

    @Test
    fun reportsDialableAddressesWithoutScope() {
        val all = listOf("127.0.0.1", "192.168.1.20", "fe80::1", "100.101.102.103", "fd7a:115c:a1e0::5", "8.8.8.8", "192.168.1.20")
            .map { InetAddress.getByName(it) }
        assertEquals(listOf("192.168.1.20", "100.101.102.103", "fd7a:115c:a1e0:0:0:0:0:5"), AddressFilter.reportable(all))
    }
}

class ListenWindowsTest {
    private var t = 0L
    private val w = ListenWindows { t }

    @Test
    fun windowsEndOnTime() {
        assertFalse(w.anyOpen())
        w.open(ListenWindows.Kind.AFTER_RECORDING, ListenWindows.AFTER_RECORDING_MS)
        w.open(ListenWindows.Kind.PERIODIC, ListenWindows.PERIODIC_MS)
        assertTrue(w.anyOpen())
        t = ListenWindows.PERIODIC_MS
        assertFalse(w.isOpen(ListenWindows.Kind.PERIODIC))
        assertTrue(w.isOpen(ListenWindows.Kind.AFTER_RECORDING))
        t = ListenWindows.AFTER_RECORDING_MS
        assertFalse(w.anyOpen())
    }

    @Test
    fun foregroundLastsUntilClosed() {
        w.open(ListenWindows.Kind.FOREGROUND)
        t = 365L * 24 * 3600 * 1000
        assertTrue(w.isOpen(ListenWindows.Kind.FOREGROUND))
        w.close(ListenWindows.Kind.FOREGROUND)
        assertFalse(w.anyOpen())
    }

    @Test
    fun reopeningNeverShortens() {
        w.open(ListenWindows.Kind.AFTER_RECORDING, ListenWindows.AFTER_RECORDING_MS)
        t = 60_000
        w.open(ListenWindows.Kind.AFTER_RECORDING, 1_000)
        t = 120_000
        assertTrue(w.isOpen(ListenWindows.Kind.AFTER_RECORDING))
        // A second recording stopping later extends it.
        w.open(ListenWindows.Kind.AFTER_RECORDING, ListenWindows.AFTER_RECORDING_MS)
        t = ListenWindows.AFTER_RECORDING_MS + 60_000
        assertTrue(w.isOpen(ListenWindows.Kind.AFTER_RECORDING))
    }

    @Test
    fun endEarlyClosesEveryWindow() {
        w.open(ListenWindows.Kind.FOREGROUND)
        w.open(ListenWindows.Kind.AFTER_RECORDING, ListenWindows.AFTER_RECORDING_MS)
        w.open(ListenWindows.Kind.PERIODIC, ListenWindows.PERIODIC_MS)
        w.endEarly()
        assertFalse(w.anyOpen())
        // A later window opens normally.
        w.open(ListenWindows.Kind.FOREGROUND)
        assertTrue(w.anyOpen())
    }
}
