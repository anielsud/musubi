package com.musubi.phone.core

import com.musubi.phone.core.BriefText.Bullet
import com.musubi.phone.core.BriefText.Cite
import com.musubi.phone.core.BriefText.Para
import com.musubi.phone.core.BriefText.Text
import org.junit.Assert.assertEquals
import org.junit.Test

class BriefTextTest {
    @Test
    fun paragraphsBulletsAndBold() {
        val blocks = BriefText.parse("## Pilot\n\nAsk **Robin Vale** first.\n- Date\n  - Budget\n    * Owner\n* Next", emptySet())
        assertEquals(
            listOf(
                Para(listOf(Text("Pilot"))),
                Para(listOf(Text("Ask "), Text("Robin Vale", bold = true), Text(" first."))),
                Bullet(0, listOf(Text("Date"))),
                Bullet(1, listOf(Text("Budget"))),
                Bullet(2, listOf(Text("Owner"))),
                Bullet(0, listOf(Text("Next"))),
            ),
            blocks,
        )
    }

    @Test
    fun citationsBecomeMarksAndUnmatchedOnesAreDropped() {
        val got = BriefText.inline("The date moved [2][9] and **budget [5]** holds [7].", setOf(2, 5))
        assertEquals(
            listOf(Text("The date moved "), Cite(2), Text(" and "), Text("budget ", bold = true), Cite(5, bold = true), Text(" holds.")),
            got,
        )
        assertEquals(listOf(Text("Nothing cited.")), BriefText.inline("Nothing cited [1].", emptySet()))
        assertEquals(listOf(Text("a b")), BriefText.inline("a [3] b", emptySet()))
    }

    @Test
    fun depthIsCapped() {
        val b = BriefText.parse(" ".repeat(40) + "- deep", emptySet()).single() as Bullet
        assertEquals(BriefText.MAX_DEPTH, b.depth)
    }
}
