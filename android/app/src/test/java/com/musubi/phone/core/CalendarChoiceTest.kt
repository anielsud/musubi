package com.musubi.phone.core

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class CalendarChoiceTest {
    private val cals = listOf(DeviceCal(1, "Work", "sam@example.com"), DeviceCal(2, "Holidays", "sam@example.com"), DeviceCal(3, "Birthdays", ""))

    @Test
    fun onlyChosenCalendarsWhenSwitchedOnAndAllowed() {
        // A permission alone reads nothing: the switch is off by default.
        assertEquals(emptyList<Long>(), CalendarChoice.toRead(show = false, granted = true, chosen = setOf(1), available = cals))
        assertEquals(emptyList<Long>(), CalendarChoice.toRead(show = true, granted = false, chosen = setOf(1), available = cals))
        assertEquals(emptyList<Long>(), CalendarChoice.toRead(show = true, granted = true, chosen = emptySet(), available = cals))
        // A chosen calendar no longer on the phone is ignored.
        assertEquals(listOf(1L, 3L), CalendarChoice.toRead(show = true, granted = true, chosen = setOf(1, 3, 99), available = cals))
    }

    @Test
    fun selectionIsParameterised() {
        assertNull(CalendarChoice.selection("calendar_id", emptyList()))
        val (sel, args) = CalendarChoice.selection("calendar_id", listOf(1, 3))!!
        assertEquals("calendar_id IN (?,?)", sel)
        assertArrayEquals(arrayOf("1", "3"), args)
    }

    @Test
    fun storedChoiceIsParsedDefensively() {
        assertEquals(setOf(4L, 7L), CalendarChoice.parse(setOf("4", "7", "x", "-2", "")))
        assertEquals(emptySet<Long>(), CalendarChoice.parse(null))
        assertEquals(CalendarChoice.MAX_CHOSEN, CalendarChoice.parse((1..500).map { it.toString() }.toSet()).size)
        assertEquals("Work · sam@example.com", CalendarChoice.label(cals[0]))
        assertEquals("Birthdays", CalendarChoice.label(cals[2]))
    }
}
