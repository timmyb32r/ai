package com.crimobile.ui

import androidx.compose.foundation.lazy.LazyListState
import com.crimobile.model.SubtitleSegment
import com.crimobile.model.WordEntry

/**
 * Вычисляет вертикальную скорость скролла для удержания активного слова
 * в зоне 25%-75% экрана. Таблица multiplier'ов применяется непрерывно:
 * позиция 50% → multiplier = 1.0, отклонение → коррекция.
 */
class KaraokeSpeedController {

    /**
     * Таблица multiplier'ов: позиция (0.0–1.0 от верха) → multiplier.
     * 101 точка (индексы 0–100, позиции 0.00–1.00).
     * Между точками — линейная интерполяция в getMultiplier().
     *
     * Формула: multiplier = position * 2
     *   0.00 → 0.0    (верх экрана — полная остановка)
     *   0.25 → 0.5    (граница верхней зоны)
     *   0.50 → 1.0    (центр — base_speed)
     *   0.75 → 1.5    (граница нижней зоны)
     *   1.00 → 2.0    (низ экрана — двойная скорость)
     */
    private val multiplierTable: FloatArray = FloatArray(101) { i ->
        val position = i / 100f
        position * 2f
    }

    /**
     * @return multiplier для позиции [0.0, 1.0] от верхней границы экрана.
     *         Значения вне [0,1] клипятся.
     */
    fun getMultiplier(verticalPositionFraction: Float): Float {
        val clamped = verticalPositionFraction.coerceIn(0f, 1f)
        val index = (clamped * 100).toInt()
        val remainder = (clamped * 100) - index

        if (index >= 100) return multiplierTable[100]
        val a = multiplierTable[index]
        val b = multiplierTable[index + 1]
        return a + (b - a) * remainder
    }

    /**
     * Вычисляет позицию активного слова в долях от высоты viewport (0–1).
     * 0 = верх экрана, 1 = низ экрана.
     *
     * @return позиция [0,1] или null если не удалось определить
     */
    fun getActiveWordVerticalPosition(
        listState: LazyListState,
        segments: List<SubtitleSegment>,
        activeWord: WordEntry,
        viewportHeightPx: Float
    ): Float? {
        val layoutInfo = listState.layoutInfo
        val visibleItems = layoutInfo.visibleItemsInfo
        if (visibleItems.isEmpty()) return null

        // Найти индекс сегмента с активным словом
        val activeIndex = segments.indexOfFirst { seg ->
            seg.words.any { w -> w === activeWord }
        }
        if (activeIndex < 0) return null

        // Найти видимый item для этого сегмента
        val itemInfo = visibleItems.find { it.index == activeIndex }
        if (itemInfo == null) {
            // Сегмент вне видимой области
            val firstVisible = visibleItems.first().index
            val lastVisible = visibleItems.last().index
            return if (activeIndex < firstVisible) 0f      // выше экрана
            else if (activeIndex > lastVisible) 1f          // ниже экрана
            else null
        }

        // Позиция центра item'а относительно верха viewport
        val itemCenterY = itemInfo.offset + itemInfo.size / 2f
        val viewportStart = layoutInfo.viewportStartOffset
        val adjustedY = itemCenterY - viewportStart

        return (adjustedY / viewportHeightPx).coerceIn(0f, 1f)
    }

    /**
     * Вычисляет base_speed (строк/сек) по видимым строкам.
     *
     * Формула: base_speed = visible_lines / delta_t
     *   - visible_lines = количество уникальных видимых сегментов
     *   - delta_t = разница между max timeline_end и min timeline_start (сек)
     *
     * Пример: 5 строк, delta_t = 10 сек → base_speed = 5/10 = 0.5 строк/сек
     *
     * @return скорость в строках/сек или 0 если данных недостаточно
     */
    fun calculateBaseSpeed(segments: List<SubtitleSegment>, listState: LazyListState): Float {
        val visibleItems = listState.layoutInfo.visibleItemsInfo
        if (visibleItems.isEmpty() || segments.isEmpty()) return 0f

        val firstVisibleIndex = visibleItems.first().index
        val lastVisibleIndex = visibleItems.last().index

        if (firstVisibleIndex >= segments.size || lastVisibleIndex >= segments.size) return 0f

        val firstSeg = segments[firstVisibleIndex]
        val lastSeg = segments[lastVisibleIndex]

        val minTime = firstSeg.words.firstOrNull()?.start_sec ?: firstSeg.timeline_start_sec
        val maxTime = lastSeg.words.lastOrNull()?.end_sec ?: lastSeg.timeline_end_sec

        val deltaSec = (maxTime - minTime).toFloat()
        // Считаем уникальные индексы сегментов (исключая spacer'ы)
        val uniqueVisibleSegments = visibleItems.map { it.index }.distinct().size
        if (uniqueVisibleSegments <= 1 || deltaSec <= 0f) return 0f

        // base_speed = visible_lines / delta_t (строк/сек)
        return uniqueVisibleSegments / deltaSec
    }
}
