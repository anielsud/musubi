package com.musubi.phone

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.provider.CalendarContract
import androidx.core.content.ContextCompat
import com.musubi.phone.core.AgendaEvent
import com.musubi.phone.core.CalendarChoice
import com.musubi.phone.core.DeviceCal
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneId

/**
 * Reads the phone's own calendar: only when the owner switched it on (Today), the permission is
 * granted, and only the calendars the owner chose. Only that choice is stored; events are never
 * stored or sent.
 */
object DeviceCalendar {
    const val ID_PREFIX = "phone-calendar:"
    private const val PREFS = "phone_calendar"
    private const val K_SHOW = "show"
    private const val K_CHOSEN = "chosen"

    fun granted(ctx: Context) =
        ContextCompat.checkSelfPermission(ctx, Manifest.permission.READ_CALENDAR) == PackageManager.PERMISSION_GRANTED

    private fun prefs(ctx: Context) = ctx.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** Off until the owner turns it on; a permission granted earlier does not turn it on. */
    fun shown(ctx: Context) = prefs(ctx).getBoolean(K_SHOW, false)
    fun setShown(ctx: Context, on: Boolean) = prefs(ctx).edit().putBoolean(K_SHOW, on).apply()

    fun chosen(ctx: Context): Set<Long> = CalendarChoice.parse(prefs(ctx).getStringSet(K_CHOSEN, null))
    fun setChosen(ctx: Context, ids: Set<Long>) =
        prefs(ctx).edit().putStringSet(K_CHOSEN, ids.take(CalendarChoice.MAX_CHOSEN).map { it.toString() }.toSet()).apply()

    /** The phone's calendars (name and account), for the picker; empty without the permission. */
    fun calendars(ctx: Context): List<DeviceCal> {
        if (!granted(ctx)) return emptyList()
        val cols = arrayOf(CalendarContract.Calendars._ID, CalendarContract.Calendars.CALENDAR_DISPLAY_NAME, CalendarContract.Calendars.ACCOUNT_NAME)
        return runCatching {
            ctx.contentResolver.query(CalendarContract.Calendars.CONTENT_URI, cols, null, null, CalendarContract.Calendars.ACCOUNT_NAME + " ASC")?.use { c ->
                val out = ArrayList<DeviceCal>()
                while (c.moveToNext() && out.size < CalendarChoice.MAX_CHOSEN) {
                    out += DeviceCal(c.getLong(0), (c.getString(1) ?: "").take(200), (c.getString(2) ?: "").take(200))
                }
                out
            } ?: emptyList()
        }.getOrDefault(emptyList())
    }

    fun events(ctx: Context, days: Int): List<AgendaEvent> {
        val ids = CalendarChoice.toRead(shown(ctx), granted(ctx), chosen(ctx), calendars(ctx))
        val sel = CalendarChoice.selection(CalendarContract.Instances.CALENDAR_ID, ids) ?: return emptyList()
        val now = System.currentTimeMillis()
        val from = now - 24L * 3600 * 1000
        val to = now + days * 24L * 3600 * 1000
        val uri = CalendarContract.Instances.CONTENT_URI.buildUpon().also {
            android.content.ContentUris.appendId(it, from)
            android.content.ContentUris.appendId(it, to)
        }.build()
        val cols = arrayOf(
            CalendarContract.Instances.EVENT_ID, CalendarContract.Instances.BEGIN, CalendarContract.Instances.END,
            CalendarContract.Instances.TITLE, CalendarContract.Instances.EVENT_LOCATION, CalendarContract.Instances.ORGANIZER,
            CalendarContract.Instances.ALL_DAY,
        )
        val out = ArrayList<AgendaEvent>()
        val zone = ZoneId.systemDefault()
        runCatching {
            ctx.contentResolver.query(uri, cols, sel.first, sel.second, CalendarContract.Instances.BEGIN + " ASC")?.use { c ->
                while (c.moveToNext() && out.size < 500) {
                    if (c.getInt(6) == 1) continue // all-day entries are not meetings
                    val eventId = c.getLong(0)
                    val begin = c.getLong(1)
                    out += AgendaEvent(
                        id = "$ID_PREFIX$eventId-$begin",
                        start = OffsetDateTime.ofInstant(Instant.ofEpochMilli(begin), zone),
                        end = OffsetDateTime.ofInstant(Instant.ofEpochMilli(c.getLong(2)), zone),
                        subject = (c.getString(3) ?: "").take(500),
                        location = (c.getString(4) ?: "").take(500),
                        organizer = (c.getString(5) ?: "").take(500),
                        attendees = attendees(ctx, eventId),
                        brief = null,
                        fromPhone = true,
                    )
                }
            }
        }
        return out
    }

    private fun attendees(ctx: Context, eventId: Long): List<String> = runCatching {
        ctx.contentResolver.query(
            CalendarContract.Attendees.CONTENT_URI,
            arrayOf(CalendarContract.Attendees.ATTENDEE_NAME, CalendarContract.Attendees.ATTENDEE_EMAIL),
            CalendarContract.Attendees.EVENT_ID + " = ?", arrayOf(eventId.toString()), null,
        )?.use { c ->
            val names = ArrayList<String>()
            while (c.moveToNext() && names.size < 50) {
                val n = c.getString(0)?.takeIf { it.isNotBlank() } ?: c.getString(1) ?: continue
                names += n.take(200)
            }
            names
        } ?: emptyList()
    }.getOrDefault(emptyList())
}
