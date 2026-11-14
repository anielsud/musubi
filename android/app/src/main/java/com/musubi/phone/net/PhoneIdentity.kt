package com.musubi.phone.net

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import com.musubi.phone.core.Hashing
import java.math.BigInteger
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.MessageDigest
import java.security.PrivateKey
import java.security.SecureRandom
import java.security.cert.X509Certificate
import java.security.spec.ECGenParameterSpec
import java.util.Date
import javax.security.auth.x500.X500Principal

/**
 * The phone's TLS identity (docs/phone.md, "Phone certificate"): an ECDSA P-256 key generated in
 * the Android Keystore, never exportable, and the self-signed certificate the Keystore issues for
 * it. The Mac pins this certificate's SHA-256 (over its DER bytes) at pairing.
 */
class PhoneIdentity private constructor(val privateKey: PrivateKey, val certificate: X509Certificate) {
    val sha256: ByteArray = MessageDigest.getInstance("SHA-256").digest(certificate.encoded)
    val sha256Hex: String = Hashing.hex(sha256)

    companion object {
        private const val KEYSTORE = "AndroidKeyStore"
        const val ALIAS = "musubi-phone-tls"
        private const val YEARS = 20L

        @Volatile private var cached: PhoneIdentity? = null

        /** Loads the identity, creating it on first use. Keystore work: call off the main thread. */
        @Synchronized
        fun get(): PhoneIdentity {
            cached?.let { return it }
            val ks = KeyStore.getInstance(KEYSTORE).apply { load(null) }
            val existing = ks.getEntry(ALIAS, null) as? KeyStore.PrivateKeyEntry
            val id = if (existing != null) {
                PhoneIdentity(existing.privateKey, existing.certificate as X509Certificate)
            } else {
                create()
                val e = ks.getEntry(ALIAS, null) as KeyStore.PrivateKeyEntry
                PhoneIdentity(e.privateKey, e.certificate as X509Certificate)
            }
            cached = id
            return id
        }

        private fun create() {
            val now = System.currentTimeMillis()
            val gen = KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, KEYSTORE)
            gen.initialize(
                KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_SIGN)
                    .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
                    // DIGEST_NONE: the TLS stack (Conscrypt) hashes the handshake itself and asks the
                    // Keystore to sign the digest; the others cover the certificate's self-signature.
                    .setDigests(KeyProperties.DIGEST_NONE, KeyProperties.DIGEST_SHA256, KeyProperties.DIGEST_SHA384, KeyProperties.DIGEST_SHA512)
                    // Generic subject: the certificate is seen by anyone the phone's server talks to.
                    .setCertificateSubject(X500Principal("CN=Musubi phone"))
                    .setCertificateSerialNumber(BigInteger(63, SecureRandom()).add(BigInteger.ONE))
                    .setCertificateNotBefore(Date(now - 24L * 3600 * 1000))
                    .setCertificateNotAfter(Date(now + YEARS * 365 * 24 * 3600 * 1000))
                    .build(),
            )
            gen.generateKeyPair()
        }
    }
}
