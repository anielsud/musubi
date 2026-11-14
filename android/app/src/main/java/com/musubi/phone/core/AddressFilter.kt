package com.musubi.phone.core

import java.net.Inet4Address
import java.net.Inet6Address
import java.net.InetAddress

/**
 * Which source addresses may connect (docs/phone.md, "Accepting"): RFC 1918, 100.64.0.0/10 and
 * fd7a:115c:a1e0::/48 (Tailscale), link-local and loopback. Everything else is closed before TLS.
 */
object AddressFilter {
    fun isAllowed(a: InetAddress): Boolean {
        val b = a.address
        return when (a) {
            is Inet4Address -> v4(b)
            is Inet6Address -> {
                // IPv4-mapped (::ffff:a.b.c.d): judge the IPv4 address.
                if (b.take(10).all { it == 0.toByte() } && b[10] == 0xff.toByte() && b[11] == 0xff.toByte()) {
                    v4(b.copyOfRange(12, 16))
                } else {
                    a.isLoopbackAddress || isV6LinkLocal(b) || isTailscaleV6(b)
                }
            }
            else -> false
        }
    }

    private fun u(b: Byte) = b.toInt() and 0xff

    private fun v4(b: ByteArray): Boolean {
        val a0 = u(b[0])
        val a1 = u(b[1])
        return a0 == 10 ||
            (a0 == 172 && a1 in 16..31) ||
            (a0 == 192 && a1 == 168) ||
            (a0 == 100 && a1 in 64..127) ||
            (a0 == 169 && a1 == 254) ||
            a0 == 127
    }

    private fun isV6LinkLocal(b: ByteArray) = u(b[0]) == 0xfe && (u(b[1]) and 0xc0) == 0x80

    private fun isTailscaleV6(b: ByteArray) =
        u(b[0]) == 0xfd && u(b[1]) == 0x7a && u(b[2]) == 0x11 && u(b[3]) == 0x5c && u(b[4]) == 0xa1 && u(b[5]) == 0xe0

    /**
     * The addresses the phone reports in `hello`: allowed ones the Mac can dial, so no loopback
     * and no link-local (which needs an interface scope the Mac cannot know). Text form, no scope.
     */
    fun reportable(all: List<InetAddress>, max: Int = 16): List<String> =
        all.asSequence()
            .filter { isAllowed(it) && !it.isLoopbackAddress && !it.isLinkLocalAddress }
            .mapNotNull { it.hostAddress?.substringBefore('%') }
            .distinct()
            .take(max)
            .toList()
}
