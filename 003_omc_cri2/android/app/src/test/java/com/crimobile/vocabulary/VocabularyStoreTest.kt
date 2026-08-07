package com.crimobile.vocabulary

import com.crimobile.model.WordEntry
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

/**
 * Regression test for the vocabulary save/read path mismatch.
 *
 * Previously [VocabularyStore] wrote to public Downloads (falling back to
 * app-private on the API 29+ SecurityException) but read ONLY from public
 * Downloads, so on API ≥ 29 every saved word was read back as an empty list.
 * Both paths now use the same app-private directory.
 */
class VocabularyStoreTest {

    @get:Rule
    val tmp = TemporaryFolder()

    private fun newStore(): VocabularyStore = VocabularyStore.forDir(tmp.newFolder("vocab"))

    private fun word(text: String) = WordEntry(
        text = text, char_start = 0, char_end = 1,
        start_sec = 0.0, end_sec = 1.0, pinyin = text, translation = text
    )

    @Test
    fun `saved words round-trip on the same path`() {
        val store = newStore()
        assertTrue(store.getSavedWords().isEmpty())

        store.appendWord(word("试点"), "ctx")
        store.appendWord(word("经济"), "ctx")

        assertEquals(listOf("试点", "经济"), store.getSavedWords())
    }

    @Test
    fun `getSavedWords is empty when nothing has been saved`() {
        val store = newStore()
        assertTrue(store.getSavedWords().isEmpty())
    }

    @Test
    fun `independent stores in different dirs do not share state`() {
        val a = VocabularyStore.forDir(tmp.newFolder("a"))
        val b = VocabularyStore.forDir(tmp.newFolder("b"))
        a.appendWord(word("A"), "ctx")
        assertEquals(listOf("A"), a.getSavedWords())
        assertTrue(b.getSavedWords().isEmpty())
    }
}
