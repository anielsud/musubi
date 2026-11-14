package com.musubi.phone.core

import java.io.File
import java.io.IOException
import java.time.Clock
import java.time.OffsetDateTime
import java.util.UUID

/**
 * Edits a recording's details (docs/phone.md, "Recording details"): every change is one atomic
 * meta.json rewrite under the store's lock with `rev` one higher. Nothing here touches the audio,
 * so editing during a recording never disturbs it; the slow parts of adding a photo (copying,
 * re-encoding, hashing) happen before the lock is taken.
 */
class DetailsEditor(private val store: RecordingStore, private val clock: Clock = Clock.systemDefaultZone()) {

    class Refused(message: String) : Exception(message)

    private fun edit(id: String, fn: (Details) -> Details?): Details {
        var out: Details? = null
        store.updateMeta(id) { m ->
            val next = fn(m.details)
            if (next == null) {
                out = m.details
                m
            } else {
                out = next.copy(rev = m.details.rev + 1)
                m.copy(details = out!!)
            }
        }
        return out!!
    }

    /**
     * Sets the notes: notes.txt (UTF-8) and its size and SHA-256 in meta.json, together under the
     * store's lock. The same text again changes nothing (no new rev).
     */
    fun setNotes(id: String, text: String): Details {
        if (text.length > Details.MAX_NOTES) throw Refused("Notes can be at most ${Details.MAX_NOTES} characters")
        val bytes = text.toByteArray(Charsets.UTF_8)
        val sha = Hashing.sha256Hex(bytes)
        return store.locked {
            edit(id) { d ->
                if (d.notes.sha256 == sha && d.notes.size == bytes.size.toLong()) null else {
                    val f = File(store.dirFor(id), Details.NOTES_FILE)
                    if (bytes.isEmpty()) f.delete() else Files.writeAtomic(f, bytes)
                    d.copy(notes = NotesInfo(bytes.size.toLong(), sha, OffsetDateTime.now(clock)))
                }
            }
        }
    }

    /** Adds a person by name (the same name again, ignoring case, changes nothing). */
    fun addPerson(id: String, name: String, email: String? = null): Details {
        val n = Details.cleanName(name) ?: throw Refused("A name is 1 to ${Details.MAX_NAME} characters")
        val e = email?.trim()?.takeIf { it.isNotEmpty() && it.length <= Details.MAX_EMAIL && it.contains('@') && it.none { c -> c.isISOControl() } }
        return edit(id) { d ->
            when {
                d.attendees.any { it.name.equals(n, ignoreCase = true) } -> null
                d.attendees.size >= Details.MAX_PEOPLE -> throw Refused("At most ${Details.MAX_PEOPLE} people")
                else -> d.copy(attendees = d.attendees + Attendee(n, e))
            }
        }
    }

    fun removePerson(id: String, name: String): Details =
        edit(id) { d -> if (d.attendees.none { it.name == name }) null else d.copy(attendees = d.attendees.filter { it.name != name }) }

    /** A new file in the recording's folder for a photo on its way in (camera output, a picked copy). */
    fun incoming(id: String): File = File(store.dirFor(id), INCOMING_PREFIX + UUID.randomUUID() + ".tmp")

    /**
     * Adds a prepared JPEG (in the recording's folder, from [incoming]) as the next photo. The file
     * is moved into place; on refusal it is deleted.
     */
    fun addPhoto(id: String, prepared: File): Details {
        try {
            require(prepared.parentFile == store.dirFor(id) && prepared.name.startsWith(INCOMING_PREFIX)) { "not an incoming file" }
            if (prepared.length() <= 0 || prepared.length() > Details.MAX_PHOTO_BYTES) throw Refused("A photo can be at most 15 MB")
            if (!isJpeg(prepared)) throw Refused("Not a JPEG photo")
            // Where it was taken stays on the phone: GPS tags are removed, orientation kept.
            ExifLocation.strip(prepared)
            val size = prepared.length()
            val sha = Hashing.sha256File(prepared)
            return store.locked {
                edit(id) { d ->
                    if (d.photos.size >= Details.MAX_PHOTOS) throw Refused("At most ${Details.MAX_PHOTOS} photos")
                    val dir = store.dirFor(id)
                    val used = d.photos.mapNotNull { photoNumber(it.name) } +
                        (dir.listFiles() ?: emptyArray()).mapNotNull { photoNumber(it.name) }
                    val n = (used.maxOrNull() ?: 0) + 1
                    if (n > 9999) throw Refused("No more photos can be added to this recording")
                    val name = Details.photoName(n)
                    if (!prepared.renameTo(File(dir, name))) throw IOException("could not keep the photo")
                    d.copy(photos = d.photos + PhotoInfo(name, size, sha, OffsetDateTime.now(clock)))
                }
            }
        } finally {
            if (prepared.exists()) prepared.delete()
        }
    }

    /** Deletes a photo from the recording (and from the Mac at its next sync). */
    fun deletePhoto(id: String, name: String): Details {
        require(Details.isPhotoName(name)) { "bad photo name" }
        return store.locked {
            val d = edit(id) { d -> if (d.photos.none { it.name == name }) null else d.copy(photos = d.photos.filter { it.name != name }) }
            File(store.dirFor(id), name).delete()
            d
        }
    }

    /** Drops incoming files a closed app left behind (a camera that never returned). */
    fun dropIncoming(id: String) {
        (store.dirFor(id).listFiles() ?: emptyArray()).filter { it.name.startsWith(INCOMING_PREFIX) }.forEach { it.delete() }
    }

    companion object {
        const val INCOMING_PREFIX = ".photo-in-"

        private val NUM = Regex("^photo-([0-9]{4})\\.jpg$")

        fun photoNumber(name: String): Int? = NUM.matchEntire(name)?.groupValues?.get(1)?.toInt()

        /** JPEG files start with SOI and a marker: FF D8 FF. */
        fun isJpeg(f: File): Boolean = f.inputStream().use { input ->
            val b = ByteArray(3)
            input.read(b) == 3 && b[0] == 0xFF.toByte() && b[1] == 0xD8.toByte() && b[2] == 0xFF.toByte()
        }
    }
}
