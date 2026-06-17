package com.crimobile.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.BugReport
import androidx.compose.material.icons.filled.FastForward
import androidx.compose.material.icons.filled.FastRewind
import androidx.compose.material.icons.filled.Pause
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import android.util.Log
import com.crimobile.CriViewModel
import com.crimobile.WordPopupState
import com.crimobile.model.PlaybackState
import com.crimobile.model.SubtitleEvent
import com.crimobile.model.SyncedSegment
import com.crimobile.model.WordBoundary

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun CriApp(viewModel: CriViewModel) {
    
    val playbackState by viewModel.playbackState.collectAsState()
    val segments by viewModel.recentSegments.collectAsState()
    val highlightRange by viewModel.highlightRange.collectAsState()
    val debugLog by viewModel.debugLog.collectAsState()
    val delaySeconds by viewModel.delaySeconds.collectAsState()

    val showPinyin by viewModel.showPinyin.collectAsState()
    val wordPopupState by viewModel.wordPopup.collectAsState()

    var showSettings by remember { mutableStateOf(false) }
    var showDebug by remember { mutableStateOf(false) }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("CRI Radio", color = Color.White) },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = Color(0xFF1A1A1A)
                ),
                actions = {
                    IconButton(onClick = {
                        showDebug = !showDebug
                        if (!showDebug) viewModel.clearDebugLog()
                    }) {
                        Icon(
                            imageVector = Icons.Filled.BugReport,
                            contentDescription = "Debug",
                            tint = if (showDebug) Color(0xFFFFC107) else Color.White,
                        )
                    }
                    IconButton(onClick = { showSettings = true }) {
                        Icon(
                            imageVector = Icons.Filled.Settings,
                            contentDescription = "Settings",
                            tint = Color.White,
                        )
                    }
                }
            )
        }
    ) { innerPadding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .background(Color(0xFF121212))
                .padding(innerPadding)
        ) {
            ConnectionIndicator(state = playbackState, delaySeconds = delaySeconds)

            Box(
                modifier = Modifier
                    .weight(0.75f)
                    .fillMaxWidth()
                    .padding(horizontal = 12.dp, vertical = 8.dp)
            ) {
                if (showDebug) {
                    DebugLogView(
                        log = debugLog,
                        modifier = Modifier.fillMaxSize(),
                    )
                } else {
                    SubtitleArea(
                        segments = segments,
                        highlightRange = highlightRange,
                        showPinyin = showPinyin,
                        showTranslation = false,
                        onWordClick = { wb, _ -> viewModel.onWordClick(wb) },
                        modifier = Modifier.fillMaxSize(),
                    )
                }
            }

            BottomControlBar(
                state = playbackState,
                onPlayPause = { viewModel.togglePlayPause() },
                modifier = Modifier
                    .weight(0.25f)
                    .fillMaxWidth(),
            )
        }
    }

    if (showSettings) {
        SettingsDialog(
            showPinyin = showPinyin,
            onTogglePinyin = { viewModel.setShowPinyin(!showPinyin) },
            currentHost = viewModel.serverHost,
            currentPort = viewModel.serverPort,
            onSave = { host, port ->
                viewModel.updateServerConfig(host, port)
                showSettings = false
            },
            onDismiss = { showSettings = false },
        )
    }

    if (wordPopupState != null) {
        WordPopupDialog(
            popup = wordPopupState!!,
            onPronounce = { viewModel.pronounceWord() },
            onSave = { viewModel.saveWord() },
            onDismiss = { viewModel.dismissPopup() },
        )
    }
}

// ── Connection indicator ──────────────────────────────────────────────────

@Composable
private fun ConnectionIndicator(state: PlaybackState, delaySeconds: Double?) {
    val (text, color, showLoader) = when (state) {
        PlaybackState.IDLE -> Triple("Ready", Color.Gray, false)
        PlaybackState.LOADING -> Triple("Buffering…", Color(0xFFFFC107), true)
        PlaybackState.PLAYING -> Triple("Live", Color(0xFF4CAF50), false)
        PlaybackState.PAUSED -> Triple("Paused", Color(0xFFFFC107), false)
    }

    Column(
        horizontalAlignment = Alignment.CenterHorizontally,
        modifier = Modifier.fillMaxWidth().padding(top = 4.dp),
    ) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.Center,
        ) {
            if (showLoader) {
                CircularProgressIndicator(
                    modifier = Modifier.size(12.dp),
                    strokeWidth = 2.dp,
                    color = color,
                )
                Spacer(modifier = Modifier.width(8.dp))
            }
            Text(text = text, color = color, fontSize = 14.sp)
        }
        if (state == PlaybackState.PLAYING && delaySeconds != null && delaySeconds >= 0) {
            Text(
                text = "delay: ${delaySeconds.toInt()}s",
                color = Color(0xFF888888),
                fontSize = 12.sp,
            )
        }
    }
}

// ── Subtitle area (scrolling feed from segments) ──────────────────────────

@Composable
private fun SubtitleArea(
    segments: List<SyncedSegment>,
    highlightRange: IntRange?,
    showPinyin: Boolean,
    showTranslation: Boolean,
    onWordClick: (WordBoundary, SubtitleEvent) -> Unit,
    modifier: Modifier = Modifier,
) {
    val listState = rememberLazyListState()

    // Auto-scroll to the last segment (most recent subtitle).
    LaunchedEffect(segments.size) {
        if (segments.isNotEmpty()) {
            listState.animateScrollToItem(maxOf(0, segments.lastIndex))
        }
    }

    if (segments.isEmpty()) {
        Box(
            modifier = modifier
                .background(Color(0xCC000000), shape = RoundedCornerShape(12.dp))
                .padding(12.dp),
            contentAlignment = Alignment.Center,
        ) {
            Text("", color = Color.Transparent, fontSize = 24.sp)
        }
        return
    }

    // Emit segment count to logcat for debugging.
    Log.d("CriUI", "SubtitleArea rendering ${segments.size} segments")

    LazyColumn(
        state = listState,
        modifier = modifier
            .background(Color(0xCC000000), shape = RoundedCornerShape(12.dp))
            .padding(12.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        itemsIndexed(segments) { idx, seg ->
            val isCurrent = idx == segments.lastIndex
            SegmentCard(
                segment = seg,
                isCurrent = isCurrent,
                highlightRange = if (isCurrent) highlightRange else null,
                showPinyin = showPinyin && isCurrent,
                showTranslation = showTranslation && isCurrent,
                onWordClick = if (isCurrent) { wb -> onWordClick(wb, seg.subtitle) } else null,
            )
        }
    }
}

// ── Single segment card ──────────────────────────────────────────────────

@Composable
private fun SegmentCard(
    segment: SyncedSegment,
    isCurrent: Boolean,
    highlightRange: IntRange?,
    showPinyin: Boolean,
    showTranslation: Boolean,
    onWordClick: ((WordBoundary) -> Unit)?,
) {
    val sub = segment.subtitle
    val zhAlpha = if (isCurrent) 1f else 0.4f

    Column(
        horizontalAlignment = Alignment.CenterHorizontally,
        modifier = Modifier.fillMaxWidth(),
    ) {
        if (isCurrent && onWordClick != null && showPinyin) {
            CharacterAlignedText(
                subtitle = sub,
                highlightedWordRange = highlightRange,
                onWordClick = onWordClick,
                alpha = zhAlpha,
            )
        } else if (isCurrent && onWordClick != null) {
            HanziText(
                subtitle = sub,
                highlightedWordRange = highlightRange,
                onWordClick = onWordClick,
                alpha = zhAlpha,
            )
        } else {
            Column(horizontalAlignment = Alignment.CenterHorizontally,
                modifier = Modifier.fillMaxWidth()) {
                if (showPinyin && sub.pinyin.isNotEmpty()) {
                    Text(sub.pinyin, color = Color(0xFFAAAAAA).copy(alpha = 0.3f),
                        fontSize = 11.sp, textAlign = TextAlign.Center)
                }
                Text(sub.textZh, color = Color.White.copy(alpha = zhAlpha),
                    fontSize = 16.sp, textAlign = TextAlign.Center)
            }
        }
        if (showTranslation && sub.en.isNotEmpty()) {
            Spacer(modifier = Modifier.height(2.dp))
            Text(sub.en, color = Color(0xFFE0E0E0), fontSize = 16.sp,
                textAlign = TextAlign.Center, maxLines = 3, modifier = Modifier.fillMaxWidth())
        }
    }
}

// ── Character-aligned pinyin + hanzi ──────────────────────────────────────

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun CharacterAlignedText(
    subtitle: SubtitleEvent,
    highlightedWordRange: IntRange?,
    onWordClick: (WordBoundary) -> Unit,
    alpha: Float,
) {
    val text = subtitle.textZh
    val words = subtitle.words
    val pinyinSyllables = subtitle.pinyin.split(" ", "　").filter { it.isNotEmpty() }

    FlowRow(
        horizontalArrangement = Arrangement.Center,
        modifier = Modifier.fillMaxWidth(),
    ) {
        text.forEachIndexed { charIdx, char ->
            val py = pinyinSyllables.getOrElse(charIdx) { "" }
            val wordIdx = words?.indexOfFirst { charIdx in it.charStart until it.charEnd } ?: -1
            val isHL = highlightedWordRange != null && charIdx in highlightedWordRange

            Column(
                horizontalAlignment = Alignment.CenterHorizontally,
                modifier = Modifier
                    .then(
                        if (wordIdx >= 0 && words != null) {
                            Modifier.clickable { onWordClick(words[wordIdx]) }
                        } else Modifier
                    )
                    .padding(horizontal = 1.dp),
            ) {
                Text(
                    text = py,
                    color = Color(0xFFAAAAAA).copy(alpha = if (py.isNotEmpty()) alpha else 0f),
                    fontSize = 10.sp, textAlign = TextAlign.Center, maxLines = 1,
                )
                Text(
                    text = char.toString(),
                    color = if (isHL) Color(0xFFFFC107) else Color.White.copy(alpha = alpha),
                    fontWeight = if (isHL) FontWeight.Bold else FontWeight.Normal,
                    fontSize = 22.sp, textAlign = TextAlign.Center,
                )
            }
        }
    }
}

// ── Hanzi text (no per-character pinyin) ──────────────────────────────────

@Composable
private fun HanziText(
    subtitle: SubtitleEvent,
    highlightedWordRange: IntRange?,
    onWordClick: (WordBoundary) -> Unit,
    alpha: Float = 1f,
) {
    val text = subtitle.textZh
    val words = subtitle.words

    if (words != null && words.isNotEmpty()) {
        val annotatedString = buildAnnotatedString {
            val sortedWords = words.sortedBy { it.charStart }
            var lastEnd = 0
            for (w in sortedWords) {
                if (w.charStart > lastEnd) {
                    append(text.substring(lastEnd, w.charStart))
                }
                pushStringAnnotation("word", words.indexOf(w).toString())
                val range = w.charStart until w.charEnd
                val wordChars = w.substring(text).toCharArray()
                if (highlightedWordRange != null &&
                    range.first <= highlightedWordRange.last &&
                    range.last >= highlightedWordRange.first
                ) {
                    withStyle(SpanStyle(color = Color(0xFFFFC107), fontWeight = FontWeight.Bold, fontSize = 24.sp)) {
                        for (i in wordChars.indices) {
                            append(wordChars[i].toString())
                            if (i < wordChars.lastIndex) append("⁠")
                        }
                    }
                } else {
                    withStyle(SpanStyle(color = Color.White.copy(alpha = alpha), fontSize = 24.sp)) {
                        for (i in wordChars.indices) {
                            append(wordChars[i].toString())
                            if (i < wordChars.lastIndex) append("⁠")
                        }
                    }
                }
                pop()
                lastEnd = w.charEnd
            }
            if (lastEnd < text.length) append(text.substring(lastEnd))
        }
        androidx.compose.foundation.text.ClickableText(
            text = annotatedString,
            onClick = { offset ->
                annotatedString.getStringAnnotations("word", offset, offset)
                    .firstOrNull()?.let { annotation ->
                        val idx = annotation.item.toInt()
                        if (idx in words.indices) onWordClick(words[idx])
                    }
            },
            modifier = Modifier.fillMaxWidth(),
            style = TextStyle(textAlign = TextAlign.Center),
        )
    } else {
        val annotatedString = buildAnnotatedString {
            text.forEachIndexed { index, char ->
                if (highlightedWordRange != null && index in highlightedWordRange) {
                    withStyle(SpanStyle(color = Color(0xFFFFC107), fontWeight = FontWeight.Bold, fontSize = 24.sp)) {
                        append(char)
                    }
                } else {
                    withStyle(SpanStyle(color = Color.White.copy(alpha = alpha), fontSize = 24.sp)) {
                        append(char)
                    }
                }
            }
        }
        Text(text = annotatedString, textAlign = TextAlign.Center, maxLines = 5, modifier = Modifier.fillMaxWidth())
    }
}



// ── Bottom control bar ────────────────────────────────────────────────────

@Composable
private fun BottomControlBar(
    state: PlaybackState,
    onPlayPause: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Row(
        modifier = modifier,
        horizontalArrangement = Arrangement.Center,
        verticalAlignment = Alignment.CenterVertically,
    ) {
        // Central button: Play / Pause / Loader
        IconButton(
            onClick = onPlayPause,
            modifier = Modifier.size(80.dp),
            enabled = state != PlaybackState.LOADING,
        ) {
            when (state) {
                PlaybackState.PLAYING -> {
                    Icon(Icons.Filled.Pause, "Pause", Modifier.size(64.dp), tint = Color.White)
                }
                PlaybackState.LOADING -> {
                    CircularProgressIndicator(Modifier.size(48.dp), color = Color(0xFFFFC107), strokeWidth = 3.dp)
                }
                PlaybackState.IDLE, PlaybackState.PAUSED -> {
                    Icon(Icons.Filled.PlayArrow, "Play", Modifier.size(64.dp), tint = Color.White)
                }
            }
        }
    }
}

// ── Debug log ─────────────────────────────────────────────────────────────

@Composable
private fun DebugLogView(log: List<String>, modifier: Modifier = Modifier) {
    val scrollState = rememberScrollState()
    Column(
        modifier = modifier
            .background(Color(0xCC000000), shape = RoundedCornerShape(12.dp))
            .padding(12.dp)
            .verticalScroll(scrollState),
    ) {
        if (log.isEmpty()) {
            Text("No debug output yet.\nPress Play to start.", color = Color(0xFF666666), fontSize = 14.sp)
        } else {
            log.forEach { line ->
                val color = when {
                    line.contains("✗") -> Color(0xFFF44336)
                    line.contains("✓") -> Color(0xFF4CAF50)
                    line.contains("→") -> Color(0xFF2196F3)
                    line.contains("⚠") -> Color(0xFFFF9800)
                    else -> Color(0xFFBBBBBB)
                }
                Text(line, color = color, fontSize = 12.sp, lineHeight = 16.sp)
            }
        }
    }
}

// ── Settings dialog ───────────────────────────────────────────────────────

@Composable
private fun SettingsDialog(
    showPinyin: Boolean,
    onTogglePinyin: () -> Unit,
    currentHost: String, currentPort: Int,
    onSave: (host: String, port: Int) -> Unit, onDismiss: () -> Unit,
) {
    var host by remember { mutableStateOf(currentHost) }
    var portText by remember { mutableStateOf(currentPort.toString()) }
    var error by remember { mutableStateOf<String?>(null) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Settings") },
        text = {
            Column {
                Row(verticalAlignment = Alignment.CenterVertically,
                    modifier = Modifier.fillMaxWidth().clickable { onTogglePinyin() }) {
                    Checkbox(checked = showPinyin, onCheckedChange = { onTogglePinyin() })
                    Spacer(Modifier.width(8.dp)); Text("Show Pinyin")
                }
                Spacer(Modifier.height(16.dp))
                OutlinedTextField(host, { host = it; error = null }, label = { Text("Host") },
                    singleLine = true, modifier = Modifier.fillMaxWidth())
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(portText, { portText = it; error = null }, label = { Text("Port") },
                    singleLine = true, modifier = Modifier.fillMaxWidth())
                if (error != null) Text(error!!, color = MaterialTheme.colorScheme.error,
                    fontSize = 12.sp, modifier = Modifier.padding(top = 4.dp))
            }
        },
        confirmButton = {
            Button(onClick = {
                val h = host.trim(); val p = portText.trim().toIntOrNull()
                when {
                    h.isEmpty() -> error = "Host cannot be empty"
                    p == null || p !in 1..65535 -> error = "Port must be 1–65535"
                    else -> onSave(h, p)
                }
            }) { Text("Save") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

// ── Word popup ────────────────────────────────────────────────────────────

@Composable
private fun WordPopupDialog(
    popup: WordPopupState,
    onPronounce: () -> Unit,
    onSave: () -> Unit,
    onDismiss: () -> Unit,
) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = {
            Text(popup.word, textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth(), fontSize = 24.sp,
                fontWeight = FontWeight.Bold)
        },
        text = {
            Column(horizontalAlignment = Alignment.CenterHorizontally,
                modifier = Modifier.fillMaxWidth()) {
                if (popup.pinyin.isNotEmpty()) {
                    Text(popup.pinyin, color = Color(0xFF666666), fontSize = 16.sp,
                        textAlign = TextAlign.Center, maxLines = 3)
                    Spacer(Modifier.height(6.dp))
                }
                if (popup.english.isNotEmpty()) {
                    Text(popup.english, color = Color.DarkGray, fontSize = 14.sp,
                        textAlign = TextAlign.Center, maxLines = 3)
                    Spacer(Modifier.height(12.dp))
                }
                Row(horizontalArrangement = Arrangement.SpaceEvenly,
                    modifier = Modifier.fillMaxWidth()) {
                    Button(onClick = onPronounce, colors = ButtonDefaults.buttonColors(
                        containerColor = Color(0xFF2196F3))) {
                        Text("🔊 Pronounce")
                    }
                    Button(onClick = onSave, colors = ButtonDefaults.buttonColors(
                        containerColor = Color(0xFF4CAF50))) {
                        Icon(Icons.Filled.Add, "Save", Modifier.size(20.dp), tint = Color.White)
                    }
                }
            }
        },
        confirmButton = {},
        dismissButton = { TextButton(onClick = onDismiss) { Text("Close") } },
    )
}
