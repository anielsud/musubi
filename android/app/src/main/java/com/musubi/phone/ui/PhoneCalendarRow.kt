package com.musubi.phone.ui

import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.materialswitch.MaterialSwitch
import com.musubi.phone.DeviceCalendar
import com.musubi.phone.MainActivity
import com.musubi.phone.core.CalendarChoice

/**
 * Today's control for the phone's own calendar: a switch (off by default, independent of the
 * permission) and, while on, which calendars to merge in. Only the choice is remembered.
 */
class PhoneCalendarRow(private val a: MainActivity) {
    fun view(screen: Screen): View {
        val box = a.vbox()
        val on = DeviceCalendar.shown(a) && DeviceCalendar.granted(a)
        val sw = MaterialSwitch(a).apply {
            text = "Show the phone's calendar"
            isChecked = on
            setOnCheckedChangeListener { _, checked -> toggled(screen, checked) }
        }
        box.addView(sw, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        if (on) {
            val cals = DeviceCalendar.calendars(a)
            val n = CalendarChoice.toRead(true, true, DeviceCalendar.chosen(a), cals).size
            val row = LinearLayout(a).apply { orientation = LinearLayout.HORIZONTAL; gravity = Gravity.CENTER_VERTICAL }
            row.addView(a.label(if (n == 0) "No calendars chosen" else "$n of ${cals.size} calendars", 13f, secondary = true),
                LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            row.addView(a.button("Choose calendars", outlined = true) { pick(screen) })
            box.addView(row)
        }
        return box
    }

    private fun toggled(screen: Screen, on: Boolean) {
        if (!on) {
            // The permission stays as it is; the app just stops reading the calendar.
            DeviceCalendar.setShown(a, false)
            screen.refresh()
            return
        }
        if (DeviceCalendar.granted(a)) {
            turnedOn(screen)
            return
        }
        a.requestCalendar { granted ->
            if (granted) turnedOn(screen) else {
                a.toast("Calendar access is off; allow it in the phone's settings for Musubi")
                screen.refresh()
            }
        }
    }

    private fun turnedOn(screen: Screen) {
        DeviceCalendar.setShown(a, true)
        screen.refresh()
        if (DeviceCalendar.chosen(a).isEmpty()) pick(screen)
    }

    private fun pick(screen: Screen) {
        val cals = DeviceCalendar.calendars(a)
        if (cals.isEmpty()) {
            a.toast("No calendars on this phone")
            return
        }
        val chosen = DeviceCalendar.chosen(a)
        val checked = BooleanArray(cals.size) { cals[it].id in chosen }
        MaterialAlertDialogBuilder(a)
            .setTitle("Calendars to show")
            .setMultiChoiceItems(cals.map { CalendarChoice.label(it) }.toTypedArray(), checked) { _, i, c -> checked[i] = c }
            .setPositiveButton("OK") { _, _ ->
                // Calendars no longer on the phone drop out of the choice here.
                DeviceCalendar.setChosen(a, cals.filterIndexed { i, _ -> checked[i] }.map { it.id }.toSet())
                screen.refresh()
            }
            .setNegativeButton("Cancel", null)
            .show()
    }
}
