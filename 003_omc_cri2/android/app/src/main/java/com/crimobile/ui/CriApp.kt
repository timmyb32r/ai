package com.crimobile.ui

import android.util.Log
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.foundation.background
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.gestures.detectVerticalDragGestures
import androidx.compose.foundation.gestures.scrollBy
import androidx.compose.foundation.interaction.DragInteraction
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.Image
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.ClickableText
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.VolumeUp
import androidx.compose.material.icons.filled.*
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ColorFilter
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalDensity
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import com.crimobile.R
import com.crimobile.ServerConfig
import com.crimobile.model.*
import com.crimobile.offline.DownloadProgress
import com.crimobile.offline.SyncConfig
import com.crimobile.viewmodel.CriAction
import com.crimobile.viewmodel.CriViewState

// ── Design tokens (matching 001_omc_cri style) ────────────────────────
private val Bg = Color(0xFF121212)
private val Surface = Color(0xFF1A1A1A)
private val CardBg = Color(0xFF222222)
private val Amber = Color(0xFFFFC107)
private val Green = Color(0xFF4CAF50)
private val TextPrimary = Color.White
private val TextSecondary = Color(0xFF888888)
private val TextPinyin = Color(0xFFAAAAAA)

// Scroll mode FSM — lives inside the scroll loop; drag-mode is set by a separate
// LaunchedEffect that only *collects* interactions (never calls scroll*).
// AUTO:  smooth auto-scroll active (default)
// MANUAL: user dragged the list; auto-scroll suspended until recenter
// PAUSED: player paused or word being pronounced
private enum class ScrollMode { AUTO, MANUAL, PAUSED }

// Sealed result from the scroll loop's snapshot-safe computation.
// Keeps the pure-calculation phase separate from the suspend-scroll-execution phase.
private sealed class ScrollResult {
    data class ScrollBy(val px: Float) : ScrollResult()
    data class ScrollTo(val index: Int) : ScrollResult()
    data object NoOp : ScrollResult()
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun CriApp(state: CriViewState, onAction: (CriAction) -> Unit) {
    // Channel-based recenter: sends Unit when user taps Recenter.
    // CONFLATED means multiple taps merge into one — no queue buildup.
    val recenterChannel = remember { Channel<Unit>(Channel.CONFLATED) }

    MaterialTheme(
        colorScheme = darkColorScheme(
            primary = Amber, secondary = Green,
            background = Bg, surface = Surface,
            onBackground = TextPrimary, onSurface = TextPrimary
        )
    ) {
        var showSettings by remember { mutableStateOf(false) }
        var showSyncSettings by remember { mutableStateOf(false) }
        var debugTapCount by remember { mutableIntStateOf(0) }
        var lastDebugTapTime by remember { mutableLongStateOf(0L) }

        Scaffold(
            topBar = {
                Surface(
                    color = Surface,
                    shadowElevation = 4.dp
                ) {
                    Box(
                        modifier = Modifier
                            .fillMaxWidth()
                            .statusBarsPadding()
                            .height(56.dp)
                            .padding(horizontal = 16.dp)
                    ) {
                        // Mode toggle — left edge
                        PlaybackModeToggle(
                            mode = state.playbackMode,
                            onToggle = { newMode ->
                                onAction(CriAction.SetPlaybackMode(newMode))
                            },
                            modifier = Modifier.align(Alignment.CenterStart)
                        )
                        // CRI logo — true screen center (no group shift)
                        CriLogo(
                            onTap = {
                                val now = System.currentTimeMillis()
                                if (now - lastDebugTapTime > 1000L) {
                                    debugTapCount = 0
                                }
                                lastDebugTapTime = now
                                debugTapCount++
                                if (debugTapCount >= 5) {
                                    debugTapCount = 0
                                    onAction(CriAction.EnableDebug)
                                }
                            },
                            modifier = Modifier.align(Alignment.Center)
                        )
                        // Actions — right edge
                        Row(
                            modifier = Modifier.align(Alignment.CenterEnd),
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            val isActive = state.playbackState == PlaybackState.PLAYING
                                || state.playbackState == PlaybackState.LOADING
                                || state.playbackState == PlaybackState.PAUSED
                            if (isActive && state.connectionStatus == ConnectionStatus.DISCONNECTED
                                && state.playbackMode == PlaybackMode.LIVE_STREAMING) {
                                Surface(
                                    shape = RoundedCornerShape(8.dp),
                                    color = Color.Red.copy(alpha = 0.12f),
                                    modifier = Modifier.padding(end = 4.dp)
                                ) {
                                    Row(
                                        modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp),
                                        verticalAlignment = Alignment.CenterVertically
                                    ) {
                                        Box(
                                            modifier = Modifier
                                                .size(6.dp)
                                                .clip(CircleShape)
                                                .background(Color.Red)
                                        )
                                        Spacer(Modifier.width(4.dp))
                                        Text(
                                            "No subtitles",
                                            color = Color.Red.copy(alpha = 0.8f),
                                            fontSize = 11.sp
                                        )
                                    }
                                }
                            }
                            if (state.playbackMode == PlaybackMode.OFFLINE_SAVED && state.segments.isNotEmpty()) {
                                Surface(
                                    shape = RoundedCornerShape(8.dp),
                                    color = Color(0xFF1976D2).copy(alpha = 0.15f),
                                    modifier = Modifier.padding(end = 4.dp)
                                ) {
                                    Text(
                                        "${state.segments.size} offline",
                                        color = Color(0xFF64B5F6),
                                        fontSize = 11.sp,
                                        modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp)
                                    )
                                }
                            }
                            if (state.playbackMode == PlaybackMode.LIVE_STREAMING) {
                                val delay = state.subtitleDelaySec
                                if (delay in 1.0..3600.0 && state.segments.isNotEmpty()) {
                                    Surface(
                                        shape = RoundedCornerShape(8.dp),
                                        color = Amber.copy(alpha = 0.15f),
                                        modifier = Modifier.padding(end = 4.dp)
                                    ) {
                                        Text(
                                            "~${delay.toInt()}s",
                                            color = Amber,
                                            fontSize = 12.sp,
                                            modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp)
                                        )
                                    }
                                }
                            }
                            IconButton(onClick = { showSettings = true }) {
                                Icon(Icons.Default.Settings, "Settings",
                                    tint = TextSecondary)
                            }
                        }
                    }
                }
                if (showSettings) {
                    SettingsDialog(
                        currentFontSize = state.fontSizeSp,
                        showPinyin = state.showPinyin,
                        showWordBoundaries = state.showWordBoundaries,
                        onFontSize = { onAction(CriAction.SetFontSize(it)) },
                        onTogglePinyin = { onAction(CriAction.TogglePinyin) },
                        onToggleWordBoundaries = { onAction(CriAction.ToggleWordBoundaries) },
                        onDismiss = { showSettings = false },
                        debugEnabled = state.debugEnabled,
                        showAudioBoundaries = state.showAudioBoundaries,
                        onToggleAudioBoundaries = { onAction(CriAction.ToggleAudioBoundaries) },
                        pinyinFontSizeSp = state.pinyinFontSizeSp,
                        onPinyinFontSize = { onAction(CriAction.SetPinyinFontSize(it)) },
                        dictFontSizeSp = state.dictFontSizeSp,
                        onDictFontSize = { onAction(CriAction.SetDictFontSize(it)) },
                        metadataProtocol = state.metadataProtocol,
                        onMetadataProtocol = { onAction(CriAction.SetMetadataProtocol(it)) }
                    )
                }
            },
            bottomBar = {
                BottomControl(
                    playbackState = state.playbackState,
                    playbackMode = state.playbackMode,
                    offlinePositionMs = state.offlinePositionMs,
                    offlineDurationMs = state.offlineDurationMs,
                    onPlay = { onAction(CriAction.Play(ServerConfig.defaultUrl)) },
                    onPause = { onAction(CriAction.Pause) },
                    onResume = { onAction(CriAction.Resume) },
                    onRecenter = { recenterChannel.trySend(Unit) }
                )
            }
        ) { padding ->
            Box(modifier = Modifier.padding(padding)) {
                // ── Offline mode: no segments → show sync setup ──
                if (state.playbackMode == PlaybackMode.OFFLINE_SAVED && state.segments.isEmpty()
                    && state.error == null && state.playbackState != PlaybackState.LOADING) {
                    OfflineSetupScreen(
                        syncConfig = state.syncConfig,
                        archiveInfo = state.archiveInfo,
                        downloadProgress = state.downloadProgress,
                        onUpdateConfig = { onAction(CriAction.UpdateSyncConfig(it)) },
                        onSaveNow = { onAction(CriAction.StartInitialSync) },
                        onCancelDownload = { onAction(CriAction.CancelDownload) },
                        onLoadArchiveInfo = { onAction(CriAction.LoadArchiveInfo) }
                    )
                } else when {
                    state.error != null -> ErrorScreen(state.error)
                    state.playbackState == PlaybackState.IDLE && state.segments.isEmpty() ->
                        WelcomeScreen()
                    state.segments.isEmpty() && state.playbackState == PlaybackState.LOADING ->
                        LoadingScreen()
                    else -> {
                        Column {
                            // In offline mode with content: show sync bar above subtitle list
                            if (state.playbackMode == PlaybackMode.OFFLINE_SAVED) {
                                OfflineContentBar(
                                    segmentCount = state.segments.size,
                                    syncConfig = state.syncConfig,
                                    archiveInfo = state.archiveInfo,
                                    downloadProgress = state.downloadProgress,
                                    offlineLocalRangeSec = state.offlineLocalRangeSec,
                                    onOpenSync = { showSyncSettings = true },
                                    onOpenNav = { onAction(CriAction.OpenOfflineNavDialog) },
                                    onUpdateConfig = { onAction(CriAction.UpdateSyncConfig(it)) },
                                    onSaveNow = { onAction(CriAction.StartInitialSync) },
                                    onCancelDownload = { onAction(CriAction.CancelDownload) },
                                    onLoadArchiveInfo = { onAction(CriAction.LoadArchiveInfo) }
                                )
                            }
                            SubtitleList(
                                segments = state.segments,
                                activeWord = state.activeWord,
                                lastActiveWord = state.lastActiveWord,
                                playbackState = state.playbackState,
                                isPronouncing = state.isPronouncing,
                                showPinyin = state.showPinyin,
                                fontSizeSp = state.fontSizeSp,
                                showWordBoundaries = state.showWordBoundaries,
                                showAudioBoundaries = state.showAudioBoundaries,
                                pinyinFontSizeSp = state.pinyinFontSizeSp,
                                recenterChannel = recenterChannel,
                                onWordTapped = { onAction(CriAction.WordTapped(it)) }
                            )
                        }
                    }
                }
            }
        }

        // Offline navigation dialog
        if (state.showOfflineNavDialog) {
            OfflineNavDialog(
                sessions = state.offlineSessions,
                segments = state.offlineSessionSegments,
                selectedSessionId = state.selectedOfflineSessionId,
                onSelectSession = { onAction(CriAction.SelectOfflineSession(it)) },
                onSelectSegment = { onAction(CriAction.SelectOfflineSegment(it)) },
                onDismiss = { onAction(CriAction.DismissOfflineNavDialog) }
            )
        }

        // Sync settings dialog (opened from offline content bar)
        if (showSyncSettings) {
            SyncSettingsDialog(
                syncConfig = state.syncConfig,
                archiveInfo = state.archiveInfo,
                downloadProgress = state.downloadProgress,
                onUpdateConfig = { onAction(CriAction.UpdateSyncConfig(it)) },
                onSaveNow = { onAction(CriAction.StartInitialSync) },
                onCancelDownload = { onAction(CriAction.CancelDownload) },
                onLoadArchiveInfo = { onAction(CriAction.LoadArchiveInfo) },
                onDismiss = { showSyncSettings = false }
            )
        }

        // Word popup
        state.wordPopup?.let { popup ->
            WordPopupDialog(popup,
                onDismiss = { onAction(CriAction.DismissPopup) },
                onPronounce = { onAction(CriAction.PronounceWord) },
                onSave = { onAction(CriAction.SaveWord) },
                onPlayFromHere = {
                    onAction(CriAction.DismissPopup)
                    onAction(CriAction.Resume)
                },
                dictFontSizeSp = state.dictFontSizeSp
            )
        }
    }
}

@Composable
private fun BottomControl(
    playbackState: PlaybackState,
    playbackMode: PlaybackMode,
    offlinePositionMs: Long,
    offlineDurationMs: Long,
    onPlay: () -> Unit,
    onPause: () -> Unit,
    onResume: () -> Unit,
    onRecenter: () -> Unit
) {
    Surface(color = Surface, modifier = Modifier.fillMaxWidth().height(128.dp)) {
        BoxWithConstraints(
            modifier = Modifier.fillMaxSize().padding(horizontal = 16.dp),
            contentAlignment = Alignment.Center
        ) {
            // Play / Pause — always perfectly centered
            PlayPauseButton(playbackState, onPlay, onPause, onResume)
            // Recenter — equidistant: d(play.right → recenter.left) = d(recenter.right → edge)
            val d = maxWidth / 4 - 48.dp
            RecenterButton(
                onRecenter,
                modifier = Modifier
                    .align(Alignment.CenterStart)
                    .offset(x = maxWidth / 2 + 40.dp + d)
            )
            // Offline progress bar — above the buttons
            if (playbackMode == PlaybackMode.OFFLINE_SAVED && offlineDurationMs > 0L) {
                Column(
                    modifier = Modifier
                        .align(Alignment.TopCenter)
                        .fillMaxWidth()
                        .padding(top = 8.dp),
                    horizontalAlignment = Alignment.CenterHorizontally
                ) {
                    val progress = (offlinePositionMs.toFloat() / offlineDurationMs)
                        .coerceIn(0f, 1f)
                    val posSec = offlinePositionMs / 1000
                    val durSec = offlineDurationMs / 1000
                    Row(
                        modifier = Modifier.fillMaxWidth().padding(horizontal = 8.dp),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(8.dp)
                    ) {
                        Text(
                            formatHhMmSs(posSec),
                            color = TextSecondary,
                            fontSize = 11.sp,
                            modifier = Modifier.width(52.dp),
                            textAlign = TextAlign.Center
                        )
                        Surface(
                            shape = RoundedCornerShape(3.dp),
                            color = Color.Transparent,
                            border = BorderStroke(0.5.dp, Amber.copy(alpha = 0.2f)),
                            modifier = Modifier.weight(1f).height(8.dp).padding(vertical = 2.dp)
                        ) {
                            LinearProgressIndicator(
                                progress = { progress },
                                modifier = Modifier.fillMaxSize().height(4.dp),
                                color = Amber,
                                trackColor = Surface
                            )
                        }
                        Text(
                            formatHhMmSs(durSec),
                            color = TextSecondary,
                            fontSize = 11.sp,
                            modifier = Modifier.width(52.dp),
                            textAlign = TextAlign.Center
                        )
                    }
                }
            }
        }
    }
}

private fun formatHhMmSs(totalSec: Long): String {
    val h = totalSec / 3600
    val m = (totalSec % 3600) / 60
    val s = totalSec % 60
    return "%02d:%02d:%02d".format(h, m, s)
}

@Composable
private fun PlayPauseButton(
    state: PlaybackState,
    onPlay: () -> Unit,
    onPause: () -> Unit,
    onResume: () -> Unit
) {
    when (state) {
        PlaybackState.PLAYING -> {
            IconButton(onClick = onPause, modifier = Modifier.size(80.dp)) {
                Icon(Icons.Default.Pause, "Pause", Modifier.size(64.dp), tint = TextPrimary)
            }
        }
        PlaybackState.LOADING -> {
            CircularProgressIndicator(
                modifier = Modifier.size(48.dp),
                color = Amber, strokeWidth = 3.dp
            )
        }
        PlaybackState.IDLE, PlaybackState.PAUSED -> {
            IconButton(
                onClick = if (state == PlaybackState.IDLE) onPlay else onResume,
                modifier = Modifier.size(80.dp)
            ) {
                Icon(Icons.Default.PlayArrow, "Play", Modifier.size(64.dp), tint = TextPrimary)
            }
        }
        PlaybackState.ERROR -> {
            IconButton(onClick = onPlay, modifier = Modifier.size(80.dp)) {
                Icon(Icons.Default.Refresh, "Retry", Modifier.size(64.dp), tint = Color.Red)
            }
        }
    }
}

@Composable
private fun RecenterButton(onRecenter: () -> Unit, modifier: Modifier = Modifier) {
    IconButton(onClick = onRecenter, modifier = modifier.size(56.dp)) {
        Icon(
            painter = painterResource(id = R.drawable.ic_recenter),
            contentDescription = "Recenter",
            modifier = Modifier.size(40.dp),
            tint = TextSecondary
        )
    }
}

/**
 * Hours + minutes editor for the download duration.
 *
 * Uses a LOCAL text buffer per sub-field (not a value derived from
 * syncConfig each keystroke), so the user can freely type "00", clear the
 * field, or enter multi-digit values without the displayed text snapping
 * back mid-edit. The parsed value is committed to config on every valid
 * change; the 60s floor is applied to the STORED value only, never to the
 * on-screen text. Numeric keyboard is forced via keyboardOptions.
 */
@Composable
private fun DownloadDurationField(
    syncConfig: SyncConfig,
    onUpdateConfig: (SyncConfig) -> Unit,
) {
    var hText by remember { mutableStateOf((syncConfig.syncDurationSec / 3600).toString()) }
    var mText by remember { mutableStateOf(((syncConfig.syncDurationSec % 3600) / 60).toString()) }

    fun commit() {
        val h = hText.toIntOrNull() ?: 0
        val m = mText.toIntOrNull() ?: 0
        onUpdateConfig(syncConfig.copy(syncDurationSec = (h * 3600 + m * 60).coerceIn(60, 86400)))
    }

    val fieldColors = OutlinedTextFieldDefaults.colors(
        focusedBorderColor = Amber,
        unfocusedBorderColor = TextSecondary.copy(alpha = 0.3f)
    )
    val fieldTextStyle = MaterialTheme.typography.bodyLarge.copy(
        color = Amber, fontSize = 16.sp, textAlign = TextAlign.Center
    )
    val numericKeyboard = KeyboardOptions(keyboardType = KeyboardType.Number)

    Row(
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp)
    ) {
        OutlinedTextField(
            value = hText,
            onValueChange = { v ->
                val digits = v.filter { it.isDigit() }.take(2)
                val n = digits.toIntOrNull()
                if (digits.isEmpty() || (n != null && n in 0..99)) { hText = digits; commit() }
            },
            singleLine = true,
            keyboardOptions = numericKeyboard,
            textStyle = fieldTextStyle,
            colors = fieldColors,
            modifier = Modifier.width(56.dp)
        )
        Text("h", color = TextSecondary, fontSize = 14.sp)
        OutlinedTextField(
            value = mText,
            onValueChange = { v ->
                val digits = v.filter { it.isDigit() }.take(2)
                val n = digits.toIntOrNull()
                if (digits.isEmpty() || (n != null && n in 0..59)) { mText = digits; commit() }
            },
            singleLine = true,
            keyboardOptions = numericKeyboard,
            textStyle = fieldTextStyle,
            colors = fieldColors,
            modifier = Modifier.width(56.dp)
        )
        Text("m", color = TextSecondary, fontSize = 14.sp)
    }
}

@Composable
private fun CriLogo(onTap: (() -> Unit)? = null, modifier: Modifier = Modifier) {
    Image(
        painter = painterResource(id = R.drawable.cri_logo),
        contentDescription = "CRI China Radio International",
        modifier = modifier
            .height(72.dp).widthIn(max = 400.dp)
            .then(if (onTap != null) Modifier.clickable { onTap() } else Modifier),
        contentScale = ContentScale.FillHeight,
        colorFilter = ColorFilter.tint(TextPrimary)
    )
}

@Composable
private fun WelcomeScreen() {
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        Column(horizontalAlignment = Alignment.CenterHorizontally) {
            Spacer(Modifier.height(12.dp))
            Text("china radio international", color = TextSecondary, fontSize = 16.sp,
                fontWeight = FontWeight.Medium, letterSpacing = 2.sp)
            Spacer(Modifier.height(16.dp))
            Text("Live Chinese radio with subtitles", color = TextSecondary, fontSize = 14.sp)
            Spacer(Modifier.height(4.dp))
            Text("Press Play to start", color = TextSecondary, fontSize = 12.sp)
        }
    }
}

@Composable
private fun SettingsDialog(
    currentFontSize: Int,
    showPinyin: Boolean,
    showWordBoundaries: Boolean,
    onFontSize: (Int) -> Unit,
    onTogglePinyin: () -> Unit,
    onToggleWordBoundaries: () -> Unit,
    onDismiss: () -> Unit,
    debugEnabled: Boolean = false,
    showAudioBoundaries: Boolean = false,
    onToggleAudioBoundaries: () -> Unit = {},
    pinyinFontSizeSp: Int = 9,
    onPinyinFontSize: (Int) -> Unit = {},
    dictFontSizeSp: Int = 14,
    onDictFontSize: (Int) -> Unit = {},
    metadataProtocol: String = "HTTP",
    onMetadataProtocol: (String) -> Unit = {},
) {
    var editSize by remember { mutableStateOf(currentFontSize.toString()) }
    var editPinyinSize by remember { mutableStateOf(pinyinFontSizeSp.toString()) }
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = CardBg,
        title = { Text("Settings", color = TextPrimary, fontWeight = FontWeight.Bold) },
        text = {
            Column {
                Text("Font size", color = TextSecondary, fontSize = 14.sp)
                Spacer(Modifier.height(8.dp))
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    FilledIconButton(
                        onClick = {
                            val v = (editSize.toIntOrNull() ?: currentFontSize) - 2
                            if (v >= 10) { val s = v.toString(); editSize = s; onFontSize(v) }
                        },
                        modifier = Modifier.size(36.dp),
                        colors = IconButtonDefaults.filledIconButtonColors(containerColor = Surface)
                    ) { Text("−", color = TextPrimary, fontSize = 18.sp) }
                    OutlinedTextField(
                        value = editSize,
                        onValueChange = { newVal ->
                            editSize = newVal.filter { it.isDigit() }
                            val v = editSize.toIntOrNull()
                            if (v != null && v in 10..64) onFontSize(v)
                        },
                        singleLine = true,
                        textStyle = MaterialTheme.typography.bodyLarge.copy(
                            color = Amber, fontSize = 16.sp, textAlign = TextAlign.Center
                        ),
                        colors = OutlinedTextFieldDefaults.colors(
                            focusedBorderColor = Amber,
                            unfocusedBorderColor = TextSecondary.copy(alpha = 0.3f)
                        ),
                        modifier = Modifier.width(72.dp)
                    )
                    FilledIconButton(
                        onClick = {
                            val v = (editSize.toIntOrNull() ?: currentFontSize) + 2
                            if (v <= 64) { val s = v.toString(); editSize = s; onFontSize(v) }
                        },
                        modifier = Modifier.size(36.dp),
                        colors = IconButtonDefaults.filledIconButtonColors(containerColor = Surface)
                    ) { Text("+", color = TextPrimary, fontSize = 18.sp) }
                }
                Spacer(Modifier.height(16.dp))
                // Pinyin font size row
                Text("Pinyin size", color = TextSecondary, fontSize = 14.sp)
                Spacer(Modifier.height(8.dp))
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    FilledIconButton(
                        onClick = {
                            val v = (editPinyinSize.toIntOrNull() ?: pinyinFontSizeSp) - 2
                            if (v >= 8) { val s = v.toString(); editPinyinSize = s; onPinyinFontSize(v) }
                        },
                        modifier = Modifier.size(36.dp),
                        colors = IconButtonDefaults.filledIconButtonColors(containerColor = Surface)
                    ) { Text("−", color = TextPrimary, fontSize = 18.sp) }
                    OutlinedTextField(
                        value = editPinyinSize,
                        onValueChange = { newVal ->
                            editPinyinSize = newVal.filter { it.isDigit() }
                            val v = editPinyinSize.toIntOrNull()
                            if (v != null && v in 8..32) onPinyinFontSize(v)
                        },
                        singleLine = true,
                        textStyle = MaterialTheme.typography.bodyLarge.copy(
                            color = Amber, fontSize = 16.sp, textAlign = TextAlign.Center
                        ),
                        colors = OutlinedTextFieldDefaults.colors(
                            focusedBorderColor = Amber,
                            unfocusedBorderColor = TextSecondary.copy(alpha = 0.3f)
                        ),
                        modifier = Modifier.width(72.dp)
                    )
                    FilledIconButton(
                        onClick = {
                            val v = (editPinyinSize.toIntOrNull() ?: pinyinFontSizeSp) + 2
                            if (v <= 32) { val s = v.toString(); editPinyinSize = s; onPinyinFontSize(v) }
                        },
                        modifier = Modifier.size(36.dp),
                        colors = IconButtonDefaults.filledIconButtonColors(containerColor = Surface)
                    ) { Text("+", color = TextPrimary, fontSize = 18.sp) }
                }
                Spacer(Modifier.height(16.dp))
                // Dictionary font size row
                Text("Dictionary size", color = TextSecondary, fontSize = 14.sp)
                Spacer(Modifier.height(8.dp))
                var editDictSize by remember { mutableStateOf(dictFontSizeSp.toString()) }
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    FilledIconButton(
                        onClick = {
                            val v = (editDictSize.toIntOrNull() ?: dictFontSizeSp) - 2
                            if (v >= 10) { val s = v.toString(); editDictSize = s; onDictFontSize(v) }
                        },
                        modifier = Modifier.size(36.dp),
                        colors = IconButtonDefaults.filledIconButtonColors(containerColor = Surface)
                    ) { Text("−", color = TextPrimary, fontSize = 18.sp) }
                    OutlinedTextField(
                        value = editDictSize,
                        onValueChange = { newVal ->
                            editDictSize = newVal.filter { it.isDigit() }
                            val v = editDictSize.toIntOrNull()
                            if (v != null && v in 10..48) onDictFontSize(v)
                        },
                        singleLine = true,
                        textStyle = MaterialTheme.typography.bodyLarge.copy(
                            color = Amber, fontSize = 16.sp, textAlign = TextAlign.Center
                        ),
                        colors = OutlinedTextFieldDefaults.colors(
                            focusedBorderColor = Amber,
                            unfocusedBorderColor = TextSecondary.copy(alpha = 0.3f)
                        ),
                        modifier = Modifier.width(72.dp)
                    )
                    FilledIconButton(
                        onClick = {
                            val v = (editDictSize.toIntOrNull() ?: dictFontSizeSp) + 2
                            if (v <= 48) { val s = v.toString(); editDictSize = s; onDictFontSize(v) }
                        },
                        modifier = Modifier.size(36.dp),
                        colors = IconButtonDefaults.filledIconButtonColors(containerColor = Surface)
                    ) { Text("+", color = TextPrimary, fontSize = 18.sp) }
                }
                Spacer(Modifier.height(16.dp))
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("Show pinyin", color = TextPrimary, fontSize = 14.sp, modifier = Modifier)
                    Switch(
                        checked = showPinyin,
                        onCheckedChange = { onTogglePinyin() },
                        colors = SwitchDefaults.colors(checkedThumbColor = Amber, checkedTrackColor = Amber.copy(alpha = 0.4f))
                    )
                }
                Spacer(Modifier.height(8.dp))
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("Word boundaries", color = TextPrimary, fontSize = 14.sp, modifier = Modifier)
                    Switch(
                        checked = showWordBoundaries,
                        onCheckedChange = { onToggleWordBoundaries() },
                        colors = SwitchDefaults.colors(checkedThumbColor = Amber, checkedTrackColor = Amber.copy(alpha = 0.4f))
                    )
                }
                if (debugEnabled) {
                    Spacer(Modifier.height(8.dp))
                    HorizontalDivider(color = TextSecondary.copy(alpha = 0.2f))
                    Spacer(Modifier.height(8.dp))
                    Text("Debug", color = TextSecondary, fontSize = 12.sp, fontWeight = FontWeight.Bold)
                    Spacer(Modifier.height(8.dp))
                    // Metadata protocol toggle
                    Text("Metadata protocol", color = TextSecondary, fontSize = 12.sp)
                    Spacer(Modifier.height(4.dp))
                    Row(horizontalArrangement = Arrangement.spacedBy(0.dp)) {
                        val protocols = listOf("HTTP", "SSE")
                        protocols.forEachIndexed { idx, proto ->
                            val selected = metadataProtocol == proto
                            OutlinedButton(
                                onClick = { onMetadataProtocol(proto) },
                                modifier = Modifier.height(36.dp),
                                shape = when {
                                    idx == 0 -> RoundedCornerShape(topStart = 6.dp, bottomStart = 6.dp)
                                    idx == protocols.lastIndex -> RoundedCornerShape(topEnd = 6.dp, bottomEnd = 6.dp)
                                    else -> RoundedCornerShape(0.dp)
                                },
                                colors = ButtonDefaults.outlinedButtonColors(
                                    containerColor = if (selected) Amber.copy(alpha = 0.2f) else Color.Transparent,
                                    contentColor = if (selected) Amber else TextSecondary
                                ),
                                border = BorderStroke(1.dp, if (selected) Amber else TextSecondary.copy(alpha = 0.3f))
                            ) {
                                Text(proto, fontSize = 14.sp)
                            }
                        }
                    }
                    Spacer(Modifier.height(12.dp))
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text("Show audio boundaries", color = TextPrimary, fontSize = 14.sp, modifier = Modifier.weight(1f))
                        Switch(
                            checked = showAudioBoundaries,
                            onCheckedChange = { onToggleAudioBoundaries() },
                            colors = SwitchDefaults.colors(checkedThumbColor = Amber, checkedTrackColor = Amber.copy(alpha = 0.4f))
                        )
                    }
                }
            }
        },
        confirmButton = {
            TextButton(onClick = onDismiss) { Text("Close", color = Amber) }
        }
    )
}

@Composable
private fun LoadingScreen() {
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        Column(horizontalAlignment = Alignment.CenterHorizontally) {
            CircularProgressIndicator(color = Amber, modifier = Modifier.size(48.dp))
            Spacer(Modifier.height(16.dp))
            Text("Connecting to radio…", color = TextSecondary, fontSize = 16.sp)
            Spacer(Modifier.height(4.dp))
            Text("Subtitles will appear shortly", color = TextSecondary.copy(alpha = 0.6f), fontSize = 12.sp)
        }
    }
}

@Composable
private fun ErrorScreen(msg: String) {
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        Column(horizontalAlignment = Alignment.CenterHorizontally) {
            Icon(Icons.Default.ErrorOutline, null, tint = Color.Red, modifier = Modifier.size(48.dp))
            Spacer(Modifier.height(16.dp))
            Text("Connection Error", color = Color.Red, fontSize = 18.sp)
            Spacer(Modifier.height(8.dp))
            Text(msg, color = TextSecondary, fontSize = 14.sp, textAlign = TextAlign.Center,
                modifier = Modifier.padding(horizontal = 32.dp))
        }
    }
}

@Composable
private fun SubtitleList(
    segments: List<SubtitleSegment>,
    activeWord: WordEntry?,
    lastActiveWord: WordEntry?,
    playbackState: PlaybackState,
    isPronouncing: Boolean,
    showPinyin: Boolean,
    fontSizeSp: Int,
    showWordBoundaries: Boolean,
    showAudioBoundaries: Boolean = false,
    pinyinFontSizeSp: Int = 9,
    recenterChannel: Channel<Unit>,
    onWordTapped: (WordEntry) -> Unit
) {
    val listState = rememberLazyListState()
    val speedController = remember { KaraokeSpeedController() }
    val density = LocalDensity.current

    // Snapshot-aware: rememberUpdatedState даёт State-обёртки
    val currentWord by rememberUpdatedState(activeWord)
    val currentLastWord by rememberUpdatedState(lastActiveWord)
    val currentSegments by rememberUpdatedState(segments)
    val currentPlaybackState by rememberUpdatedState(playbackState)
    val currentIsPronouncing by rememberUpdatedState(isPronouncing)

    // ── Single scroll owner ───────────────────────────────────────────────
    // Exactly one coroutine ever calls listState.scroll*.  Recenter requests
    // arrive as data (Channel<Unit>), polled inside the loop — never as competing
    // scroll calls from other composables or Scaffold slots.
    val scrollMode = remember { mutableStateOf(ScrollMode.AUTO) }

    // Drag detector: a SEPARATE coroutine that only COLLECTS DragInteraction
    // events.  It never calls scroll*, so it cannot create MutatorMutex contention.
    LaunchedEffect(listState) {
        listState.interactionSource.interactions.collect { interaction ->
            when (interaction) {
                is DragInteraction.Start -> {
                    if (scrollMode.value != ScrollMode.MANUAL) {
                        scrollMode.value = ScrollMode.MANUAL
                        Log.i("CRIRadio:scroll", "FSM → MANUAL (user drag)")
                    }
                }
                is DragInteraction.Cancel, is DragInteraction.Stop -> {
                    // stays MANUAL until recenter
                }
            }
        }
    }

    // ── Main scroll loop (single owner) ───────────────────────────────────
    LaunchedEffect(Unit) {
        var initSpeedPxPerSec = 0f
        var lastTickNanos = 0L
        var totalScrolledPx = 0f
        var accumulatedPx = 0f
        var lastLogNanos = 0L
        var loopIterations = 0L
        var wasPaused = false

        // ── Diagnostics captured each tick, surfaced in the 2s heartbeat ──
        var dbgPosition: Float? = null      // active word vertical position [0,1] or null
        var dbgMultiplier = 0f              // speed multiplier for that position
        var dbgBaseSpeed = 0f               // base px/s used this tick
        var dbgRawPx = 0f                   // px requested this tick (pre-accumulation)
        var dbgActiveIdx = -1               // segment index of the active word (-1 = not found)
        var dbgReason = "init"              // why we scrolled / did not scroll this tick

        while (isActive) {
            loopIterations++

            // ── Recenter: non-blocking channel poll (Invariant #2) ──
            if (recenterChannel.tryReceive().getOrNull() != null) {
                scrollMode.value = ScrollMode.AUTO
                // Force re-centering on next pass
                lastTickNanos = 0L  // triggers re-init below
                Log.i("CRIRadio:scroll", "RECENTER → AUTO")
            }

            // ── Read current state into plain immutable locals ──
            // Plain snapshots of the current values, outside any snapshot
            // observation — NOT iterated as SnapshotStateList.
            val word = currentWord
            val lastWord = currentLastWord
            val segs = currentSegments
            val playing = currentPlaybackState == PlaybackState.PLAYING
            val pronouncing = currentIsPronouncing
            val mode = scrollMode.value

            // ── PAUSED check ──
            val shouldPause = !playing || pronouncing
            if (shouldPause && mode == ScrollMode.AUTO) {
                scrollMode.value = ScrollMode.PAUSED
                wasPaused = true
                lastTickNanos = 0L
                Log.d("CRIRadio:scroll", "FSM → PAUSED")
            }
            if (!shouldPause && mode == ScrollMode.PAUSED) {
                scrollMode.value = ScrollMode.AUTO
                lastTickNanos = 0L  // force re-center on resume
                Log.i("CRIRadio:scroll", "FSM PAUSED → AUTO (resume + recenter)")
            }
            val currentMode = scrollMode.value

            // ── Tick: frame-paced via withFrameNanos ──
            // Recompute position AND speed exactly once per rendered frame
            // (~60 fps, vsync-aligned). We take only the TIMESTAMP here; every
            // snapshot read and the scrollBy happen outside, wrapped in
            // Snapshot.withoutReadObservation / executed below — so the loop is
            // NOT coupled to snapshot invalidation (the old cancellation trap).
            // withFrameNanos self-throttles to the display and yields a true
            // frame dt, so speed * dt stays time-correct even across a hitch
            // (no fixed-16ms drift, no jerky catch-up).
            val tickNanos = withFrameNanos { it }
            val dt = if (lastTickNanos > 0) {
                ((tickNanos - lastTickNanos) / 1_000_000_000f).coerceIn(0.001f, 0.100f)
            } else {
                0.016f
            }

            // ── Snapshot-safe reads (Invariant #3) ──
            // ALL layoutInfo / index computations are wrapped so they never
            // observe Compose snapshot state or trigger recomposition.
            val snapshotResult: ScrollResult? = Snapshot.withoutReadObservation {
                if (segs.isEmpty()) return@withoutReadObservation null

                val viewportHeightPx = with(density) {
                    listState.layoutInfo.viewportSize.height.toFloat()
                }
                if (viewportHeightPx <= 0f) return@withoutReadObservation null

                val visibleItems = listState.layoutInfo.visibleItemsInfo
                if (visibleItems.isEmpty()) return@withoutReadObservation null

                // Effective word: fall back to lastActiveWord during silence gaps
                val effectiveWord = word ?: lastWord

                // Heartbeat every 5s
                if (tickNanos - lastLogNanos > 5_000_000_000L) {
                    Log.d("CRIRadio:scroll",
                        "alive segs=${segs.size} word=${effectiveWord?.text} " +
                        "mode=$currentMode loop=$loopIterations")
                }

                // ── MANUAL / PAUSED: no scroll; just skip ──
                if (currentMode != ScrollMode.AUTO) {
                    dbgReason = "mode=$currentMode"
                    return@withoutReadObservation ScrollResult.NoOp
                }

                // ── No active word → coast at initSpeed ──
                if (effectiveWord == null) {
                    dbgReason = "no-word"; dbgActiveIdx = -1; dbgPosition = null
                    if (initSpeedPxPerSec > 0f) {
                        dbgReason = "no-word-coast"; dbgRawPx = initSpeedPxPerSec * dt
                        return@withoutReadObservation ScrollResult.ScrollBy(initSpeedPxPerSec * dt)
                    }
                    return@withoutReadObservation ScrollResult.NoOp
                }

                // ── Find active word index ──
                val activeIdx = segs.indexOfFirst { it.words.any { w -> w === effectiveWord } }
                dbgActiveIdx = activeIdx
                if (activeIdx < 0) {
                    // Active word not present in the rendered list (e.g. segment churn /
                    // eviction during a large HTTP backlog fetch) → cannot scroll to it.
                    dbgReason = "activeIdx<0 word=${effectiveWord.text}"
                    return@withoutReadObservation ScrollResult.NoOp
                }

                // ── Re-init: center word ~25% from top (jump allowed) ──
                if (lastTickNanos == 0L || wasPaused) {
                    val offsetItems = (viewportHeightPx * 0.25f / (visibleItems.first().size.toFloat())).toInt()
                    val targetIdx = (activeIdx - offsetItems).coerceAtLeast(0)

                    // Compute init speed
                    val firstVisibleIdx = visibleItems.first().index
                    val lastVisibleIdx = visibleItems.last().index
                    if (firstVisibleIdx in segs.indices && lastVisibleIdx in segs.indices) {
                        val firstSeg = segs[firstVisibleIdx]
                        val lastSeg = segs[lastVisibleIdx]
                        val firstTime = firstSeg.words.firstOrNull()?.start_sec ?: firstSeg.timeline_start_sec
                        val lastTime = lastSeg.words.lastOrNull()?.end_sec ?: lastSeg.timeline_end_sec
                        val deltaSec = (lastTime - firstTime).toFloat()
                        val totalPx = visibleItems.sumOf { it.size }.toFloat()
                        if (deltaSec > 0f && totalPx > 0f) {
                            initSpeedPxPerSec = totalPx / deltaSec
                        }
                    }
                    Log.i("CRIRadio:scroll",
                        "INIT segs=${segs.size} activeIdx=$activeIdx targetIdx=$targetIdx initSpeed=%.1f".format(initSpeedPxPerSec))
                    return@withoutReadObservation ScrollResult.ScrollTo(targetIdx)
                }

                // ── Normal scroll: position → multiplier → speed → px ──
                val position = speedController.getActiveWordVerticalPosition(
                    listState, segs, effectiveWord, viewportHeightPx
                )
                dbgPosition = position

                return@withoutReadObservation when {
                    position == 0f || position == 1f -> {
                        // Word off-screen — jump
                        dbgReason = if (position == 0f) "offscreen-top→jump" else "offscreen-bottom→jump"
                        dbgMultiplier = 0f; dbgRawPx = 0f
                        ScrollResult.ScrollTo(activeIdx.coerceAtMost(segs.size - 1))
                    }
                    position != null -> {
                        val multiplier = speedController.getMultiplier(position)
                        val visibleSpeed = speedController.calculateBaseSpeed(segs, listState)
                        val baseSpeedPxPerSec = if (visibleSpeed > 0f) {
                            val lh = visibleItems.firstOrNull()?.size?.toFloat() ?: 0f
                            if (lh > 0f) visibleSpeed * lh else initSpeedPxPerSec
                        } else {
                            initSpeedPxPerSec
                        }
                        val rawPx = baseSpeedPxPerSec * multiplier * dt
                        dbgMultiplier = multiplier; dbgBaseSpeed = baseSpeedPxPerSec; dbgRawPx = rawPx
                        dbgReason = if (multiplier <= 0f) "at-or-above-target(hold)" else "scroll"
                        ScrollResult.ScrollBy(rawPx)
                    }
                    else -> {
                        dbgReason = "position=null"; dbgMultiplier = 0f; dbgRawPx = 0f
                        ScrollResult.NoOp
                    }
                }
            } // Snapshot.withoutReadObservation

            // ── Execute scroll action OUTSIDE snapshot observation ──
            when (val result = snapshotResult) {
                is ScrollResult.ScrollBy -> {
                    accumulatedPx += result.px
                    val wholePx = accumulatedPx.toInt()
                    if (wholePx != 0) {
                        try {
                            listState.scrollBy(wholePx.toFloat())
                        } catch (_: Exception) { }
                        totalScrolledPx += wholePx
                        accumulatedPx -= wholePx
                    }
                }
                is ScrollResult.ScrollTo -> {
                    try {
                        listState.scrollToItem(result.index, 0)
                    } catch (_: Exception) { }
                }
                is ScrollResult.NoOp, null -> { /* nothing to do */ }
            }

            // ── Log every 2s ──
            if (tickNanos - lastLogNanos > 2_000_000_000L) {
                lastLogNanos = tickNanos
                val posStr = dbgPosition?.let { "%.2f".format(it) } ?: "null"
                Log.i("CRIRadio:scroll",
                    ("mode=$currentMode reason=$dbgReason pos=$posStr mult=%.2f " +
                     "base=%.1f rawPx=%.2f/tick initSpeed=%.1f totalPx=%.0f " +
                     "activeIdx=$dbgActiveIdx segs=${segs.size} dt=%.0fms loop=$loopIterations").format(
                        dbgMultiplier, dbgBaseSpeed, dbgRawPx, initSpeedPxPerSec,
                        totalScrolledPx, dt * 1000))
            }

            lastTickNanos = tickNanos
            wasPaused = shouldPause
        }
    }

    // ScrollResult is a top-level sealed class (see above CriApp).
    // Snapshot-safe computation returns a ScrollResult; execution happens here.

    Box(modifier = Modifier.fillMaxSize()) {
        LazyColumn(
            state = listState, modifier = Modifier.fillMaxSize(),
            contentPadding = PaddingValues(horizontal = 12.dp, vertical = 8.dp)
        ) {
            itemsIndexed(segments, key = { _, s -> s.segment_id }) { index, segment ->
                val isTsBoundary = index > 0 && segments[index - 1].ts_file != segment.ts_file
                SegmentCard(segment, activeWord, showPinyin, fontSizeSp, showWordBoundaries, isTsBoundary, showAudioBoundaries, pinyinFontSizeSp, lastActiveWord, onWordTapped)
                Spacer(Modifier.height(6.dp))
            }
        }

        // Draggable amber scroll thumb.
        ScrollThumb(listState, modifier = Modifier.align(Alignment.TopEnd).padding(end = 4.dp))
    }
}

@Composable
private fun SegmentCard(
    segment: SubtitleSegment,
    activeWord: WordEntry?,
    showPinyin: Boolean,
    fontSizeSp: Int,
    showWordBoundaries: Boolean,
    isTsBoundary: Boolean = false,
    showAudioBoundaries: Boolean = false,
    pinyinFontSizeSp: Int = 9,
    lastActiveWord: WordEntry? = null,
    onWordTapped: (WordEntry) -> Unit
) {
    Card(
        colors = CardDefaults.cardColors(containerColor = CardBg),
        shape = RoundedCornerShape(8.dp),
        modifier = Modifier.fillMaxWidth()
    ) {
        Column(modifier = Modifier.padding(10.dp)) {
            // FlowRow: each character in its own Column, pinyin centered above.
            // FlowRow wraps to next line — no overflow off-screen.
            // CJK characters are naturally uniform-width — no weight() needed.
            val cells = buildCharCells(segment.words, showPinyin)
                .filter { !isPunctuationOnly(it.text) }

            @OptIn(ExperimentalLayoutApi::class)
            FlowRow(modifier = Modifier.fillMaxWidth()) {
                cells.forEachIndexed { cellIdx, charCell ->
                    val effectiveWord = activeWord ?: lastActiveWord
                    val isActive = charCell.word === effectiveWord
                    val isCJKChar = charCell.text.any { it.code in 0x4E00..0x9FFF }
                    val hasUnderline = showWordBoundaries && isCJKChar
                    // Word boundary detection for underline gaps
                    val isFirstInWord = cellIdx == 0 || cells[cellIdx - 1].word !== charCell.word
                    val isLastInWord = cellIdx == cells.lastIndex || cells[cellIdx + 1].word !== charCell.word
                    Column(
                        horizontalAlignment = Alignment.CenterHorizontally,
                        modifier = Modifier
                            .padding(horizontal = 1.5.dp)
                            .then(if (cellIdx == 0 && isTsBoundary && showAudioBoundaries) Modifier.drawBehind {
                                drawLine(Amber.copy(alpha = 0.55f), Offset(0f, 0f), Offset(0f, size.height), strokeWidth = 1.5.dp.toPx())
                            } else Modifier)
                            .then(if (hasUnderline) Modifier.drawBehind {
                                val strokeWidth = 2.dp.toPx()
                                val dashWidth = 4.dp.toPx()
                                val gapWidth = 3.dp.toPx()
                                val y = size.height - 2.dp.toPx()
                                // Gap at word boundaries: inset 6dp at first/last char → 12dp visible break
                                val x1 = if (isFirstInWord) 6.dp.toPx() else 0f
                                val x2 = if (isLastInWord) size.width - 6.dp.toPx() else size.width
                                if (x2 > x1) {
                                    val path = Path().apply {
                                        moveTo(x1, y)
                                        lineTo(x2, y)
                                    }
                                    drawPath(
                                        path, TextPrimary.copy(alpha = 0.25f),
                                        style = Stroke(
                                            width = strokeWidth,
                                            pathEffect = PathEffect.dashPathEffect(
                                                floatArrayOf(dashWidth, gapWidth), 0f
                                            )
                                        )
                                    )
                                }
                            } else Modifier)
                            .clickable {
                                if (!isPunctuationOnly(charCell.word.text)) {
                                    Log.i("CRIRadio:tap",
                                        "→ tapped \"${charCell.word.text}\" pinyin=${charCell.word.pinyin}")
                                    onWordTapped(charCell.word)
                                } else {
                                    Log.d("CRIRadio:tap",
                                        "→ skipped punctuation \"${charCell.text}\"")
                                }
                            }
                    ) {
                        // Pinyin slot — min height for alignment, but allows descenders
                        if (showPinyin) {
                            Box(modifier = Modifier.heightIn(min = 18.dp), contentAlignment = Alignment.Center) {
                                Text(charCell.syllable, fontSize = pinyinFontSizeSp.sp, color = TextPinyin,
                                    maxLines = 1, softWrap = false)
                            }
                        }
                        Text(
                            text = charCell.text,
                            color = if (isActive) Amber else TextPrimary,
                            fontSize = fontSizeSp.sp,
                            lineHeight = (fontSizeSp * 1.5).sp,
                            maxLines = 1, softWrap = false
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun ScrollThumb(
    listState: LazyListState,
    modifier: Modifier = Modifier
) {
    val density = LocalDensity.current
    val coroutineScope = rememberCoroutineScope()
    val totalItems = listState.layoutInfo.totalItemsCount
    if (totalItems <= 0) return

    val viewportH = listState.layoutInfo.viewportSize.height.toFloat()
    val firstVisible = listState.firstVisibleItemIndex
    val visibleCount = listState.layoutInfo.visibleItemsInfo.size
    val thumbH = with(density) { 40.dp.toPx() }
    val maxScroll = (totalItems - visibleCount).coerceAtLeast(1)
    val fraction = (firstVisible.toFloat() / maxScroll).coerceIn(0f, 1f)
    val thumbY = fraction * (viewportH - thumbH)

    var dragStartData by remember { mutableStateOf<Triple<Int, Float, Float>?>(null) } // (maxScroll, startFrac, totalDy)

    // Invisible wide touch target; visible thumb drawn inside at original width.
    Box(
        modifier = modifier
            .offset(y = with(density) { thumbY.toDp() })
            .width(24.dp) // wide touch target
            .height(with(density) { 40.dp })
            .pointerInput(Unit) {
                detectVerticalDragGestures(
                    onDragStart = {
                        val curTotal = listState.layoutInfo.totalItemsCount
                        val curVis = listState.layoutInfo.visibleItemsInfo.size
                        val curMax = (curTotal - curVis).coerceAtLeast(1)
                        val startFrac = listState.firstVisibleItemIndex.toFloat() / curMax
                        Log.i("CRIRadio:thumb", "start: first=${listState.firstVisibleItemIndex} total=$curTotal frac=$startFrac")
                        dragStartData = Triple(curMax, startFrac, 0f)
                    },
                    onDragEnd = {
                        Log.i("CRIRadio:thumb", "end: first=${listState.firstVisibleItemIndex}")
                        dragStartData = null
                    },
                    onDragCancel = {
                        Log.i("CRIRadio:thumb", "cancel")
                        dragStartData = null
                    },
                    onVerticalDrag = { _, dragAmount ->
                        val (curMax, startFrac, prevTotal) = dragStartData ?: return@detectVerticalDragGestures
                        // Accumulate total drag distance; dragAmount is delta since last event.
                        val totalDy = prevTotal + dragAmount
                        dragStartData = Triple(curMax, startFrac, totalDy)
                        val vpH = listState.layoutInfo.viewportSize.height.toFloat()
                        val range = vpH - thumbH
                        if (range <= 0) return@detectVerticalDragGestures
                        val newFraction = (startFrac + totalDy / range).coerceIn(0f, 1f)
                        val targetIdx = (newFraction * curMax).toInt().coerceIn(0, curMax)
                        Log.d("CRIRadio:thumb", "drag: dY=$dragAmount totalDy=$totalDy newF=$newFraction target=$targetIdx firstNow=${listState.firstVisibleItemIndex}")
                        coroutineScope.launch { listState.scrollToItem(targetIdx, 0) }
                    }
                )
            },
        contentAlignment = Alignment.CenterEnd
    ) {
        // Visible thumb — narrow amber pill.
        Box(
            modifier = Modifier
                .width(8.dp)
                .height(with(density) { 40.dp })
                .clip(RoundedCornerShape(4.dp))
                .background(Amber.copy(alpha = 0.5f))
        )
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun WordPopupDialog(
    popup: WordPopupState,
    onDismiss: () -> Unit,
    onPronounce: () -> Unit,
    onSave: () -> Unit,
    onPlayFromHere: () -> Unit = {},
    dictFontSizeSp: Int = 14
) {
    val dictFont = dictFontSizeSp.sp
    val clipboard = LocalClipboardManager.current
    val sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)

    ModalBottomSheet(
        onDismissRequest = onDismiss,
        containerColor = CardBg,
        sheetState = sheetState,
        dragHandle = { BottomSheetDefaults.DragHandle(color = TextSecondary) }
    ) {
        Column(modifier = Modifier.fillMaxWidth()) {
            // Scrollable content area
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .weight(1f)
                    .verticalScroll(rememberScrollState())
                    .padding(horizontal = 20.dp)
            ) {
                // Header: character + copy button
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(
                        popup.word.text,
                        fontSize = 36.sp,
                        fontWeight = FontWeight.Bold,
                        color = Amber
                    )
                    Spacer(Modifier.width(8.dp))
                    IconButton(onClick = {
                        clipboard.setText(AnnotatedString(popup.word.text))
                    }) {
                        Icon(
                            Icons.Default.ContentCopy, "Copy", tint = TextSecondary,
                            modifier = Modifier.size(20.dp)
                        )
                    }
                }

                // Pinyin with diacritics
                if (popup.pinyin.isNotBlank()) {
                    Spacer(Modifier.height(4.dp))
                    Text(
                        text = pinyinToDiacritic(popup.pinyin),
                        color = TextPinyin,
                        fontSize = dictFont
                    )
                }

                // Structured senses (BKRS) or flat translation (CC-CEDICT)
                if (popup.senses.isNotEmpty()) {
                    Spacer(Modifier.height(12.dp))
                    popup.senses.forEach { sense ->
                        Spacer(Modifier.height(8.dp))
                        Row {
                            if (sense.number > 0) {
                                Text(
                                    "${sense.number}. ",
                                    color = Amber,
                                    fontSize = 15.sp,
                                    fontWeight = FontWeight.Bold
                                )
                            }
                            Column {
                                if (sense.labels.isNotEmpty()) {
                                    Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                                        sense.labels.forEach { label ->
                                            Surface(
                                                shape = RoundedCornerShape(4.dp),
                                                color = TextSecondary.copy(alpha = 0.15f)
                                            ) {
                                                Text(
                                                    label,
                                                    color = TextSecondary,
                                                    fontSize = 12.sp,
                                                    modifier = Modifier.padding(
                                                        horizontal = 4.dp,
                                                        vertical = 1.dp
                                                    )
                                                )
                                            }
                                        }
                                    }
                                    Spacer(Modifier.height(2.dp))
                                }
                                Text(sense.text, color = TextPrimary, fontSize = dictFont)
                                if (sense.notes.isNotBlank()) {
                                    Text(
                                        sense.notes,
                                        color = TextSecondary,
                                        fontSize = 13.sp,
                                        fontStyle = androidx.compose.ui.text.font.FontStyle.Italic
                                    )
                                }
                            }
                        }
                    }
                } else {
                    Spacer(Modifier.height(8.dp))
                    Text(
                        "Translation",
                        color = TextSecondary,
                        fontSize = dictFont,
                        fontWeight = FontWeight.Medium
                    )
                    Spacer(Modifier.height(4.dp))
                    Text(popup.translation, color = TextPrimary, fontSize = dictFont)
                }

                Spacer(Modifier.height(8.dp))
            }

            // Fixed bottom action bar — always visible, no scrolling needed
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .requiredHeight(128.dp)
            ) {
                Surface(
                    color = Color(0xFF2A2A2A),
                    shadowElevation = 4.dp,
                    modifier = Modifier.fillMaxSize()
                ) {
                    Row(
                        modifier = Modifier
                            .fillMaxSize()
                            .padding(horizontal = 12.dp),
                        horizontalArrangement = Arrangement.SpaceEvenly,
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                    TextButton(onClick = onPlayFromHere) {
                        Icon(Icons.Default.PlayArrow, null, tint = Amber, modifier = Modifier.size(20.dp))
                        Spacer(Modifier.width(4.dp))
                        Text("Play", color = Amber, fontSize = 14.sp)
                    }
                    TextButton(onClick = onPronounce) {
                        Icon(Icons.AutoMirrored.Filled.VolumeUp, null, tint = Amber, modifier = Modifier.size(20.dp))
                        Spacer(Modifier.width(4.dp))
                        val durationSec = popup.word.end_sec - popup.word.start_sec
                        Text("Pron. ${"%.1f".format(durationSec)}s", color = Amber, fontSize = 14.sp)
                    }
                    TextButton(onClick = onSave) {
                        Icon(Icons.Default.Add, null, tint = Green, modifier = Modifier.size(20.dp))
                        Spacer(Modifier.width(4.dp))
                        Text("Save", color = Green, fontSize = 14.sp)
                    }
                    TextButton(onClick = onDismiss) {
                        Text("Close", color = TextSecondary, fontSize = 14.sp)
                    }
                }
            }
                }
            }
        }
    }

// ── Pinyin numbered → diacritic conversion ──────────────────────────────
// Ported from 001_omc_cri/internal/broadcast/enrich.go

private val TONE_VOWEL_MAP: Map<Pair<Char, Int>, Char> = mapOf(
    ('a' to 1) to 'ā', ('a' to 2) to 'á', ('a' to 3) to 'ǎ', ('a' to 4) to 'à',
    ('e' to 1) to 'ē', ('e' to 2) to 'é', ('e' to 3) to 'ě', ('e' to 4) to 'è',
    ('i' to 1) to 'ī', ('i' to 2) to 'í', ('i' to 3) to 'ǐ', ('i' to 4) to 'ì',
    ('o' to 1) to 'ō', ('o' to 2) to 'ó', ('o' to 3) to 'ǒ', ('o' to 4) to 'ò',
    ('u' to 1) to 'ū', ('u' to 2) to 'ú', ('u' to 3) to 'ǔ', ('u' to 4) to 'ù',
    ('ü' to 1) to 'ǖ', ('ü' to 2) to 'ǘ', ('ü' to 3) to 'ǚ', ('ü' to 4) to 'ǜ',
)

/** Converts pinyin with tone numbers (zhe4) to diacritic marks (zhè). */
fun pinyinToDiacritic(s: String): String {
    return s.split(" ").joinToString(" ") { syllableToDiacritic(it) }
}

private fun syllableToDiacritic(s: String): String {
    var syl = s.replace("u:", "ü").replace("v", "ü")

    // Find tone digit (1-5) scanning from right
    var tonePos = -1
    var tone = 0
    for (i in syl.lastIndex downTo 0) {
        val c = syl[i]
        if (c in '1'..'5') { tone = c - '0'; tonePos = i; break }
        if (c !in 'a'..'z') break
    }
    if (tone == 0 || tone == 5) {
        return if (tonePos >= 0) syl.removeRange(tonePos, tonePos + 1) else syl
    }

    val idx = findToneVowel(syl.substring(0, tonePos))
    if (idx < 0) return syl.removeRange(tonePos, tonePos + 1)

    val toned = TONE_VOWEL_MAP[syl[idx] to tone] ?: return syl.removeRange(tonePos, tonePos + 1)

    return syl.substring(0, idx) + toned + syl.substring(idx + 1, tonePos) + syl.substring(tonePos + 1)
}

// ── Offline setup screen (shown when offline + no content) ───────────

@Composable
private fun OfflineSetupScreen(
    syncConfig: SyncConfig,
    archiveInfo: com.crimobile.offline.ArchiveInfo?,
    downloadProgress: DownloadProgress?,
    onUpdateConfig: (SyncConfig) -> Unit,
    onSaveNow: () -> Unit,
    onCancelDownload: () -> Unit,
    onLoadArchiveInfo: () -> Unit
) {
    // Load archive info on first composition
    LaunchedEffect(Unit) {
        if (archiveInfo == null) {
            onLoadArchiveInfo()
        }
    }

    var editHour by remember { mutableStateOf(syncConfig.syncHourOfDay) }
    var editMinute by remember { mutableStateOf(syncConfig.syncMinute) }
    var editEnabled by remember { mutableStateOf(syncConfig.enabled) }
    var editWifiOnly by remember { mutableStateOf(syncConfig.wifiOnly) }
    val editDurationSec = syncConfig.syncDurationSec
    val editDurationH = editDurationSec / 3600.0

    LazyColumn(
        modifier = Modifier.fillMaxSize().padding(horizontal = 16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp)
    ) {
        // Header
        item {
            Spacer(Modifier.height(8.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(
                    Icons.Default.Sync,
                    contentDescription = null,
                    tint = Color(0xFF64B5F6),
                    modifier = Modifier.size(28.dp)
                )
                Spacer(Modifier.width(12.dp))
                Column {
                    Text(
                        "Offline Mode",
                        color = TextPrimary,
                        fontSize = 20.sp,
                        fontWeight = FontWeight.Bold
                    )
                    Text(
                        "Download audio + subtitles for listening without internet",
                        color = TextSecondary,
                        fontSize = 13.sp
                    )
                }
            }
        }

        // Scheduled sync toggle
        item {
            Card(
                colors = CardDefaults.cardColors(containerColor = CardBg),
                shape = RoundedCornerShape(8.dp)
            ) {
                Row(
                    modifier = Modifier.fillMaxWidth().padding(12.dp),
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text("Scheduled daily sync", color = TextPrimary, fontSize = 14.sp, modifier = Modifier)
                    Switch(
                        checked = editEnabled,
                        onCheckedChange = {
                            editEnabled = it
                            onUpdateConfig(syncConfig.copy(enabled = it))
                        },
                        colors = SwitchDefaults.colors(
                            checkedThumbColor = Color(0xFF64B5F6),
                            checkedTrackColor = Color(0xFF64B5F6).copy(alpha = 0.4f)
                        )
                    )
                }
            }
        }

        // Sync time (only when enabled)
        if (editEnabled) {
            item {
                Card(
                    colors = CardDefaults.cardColors(containerColor = CardBg),
                    shape = RoundedCornerShape(8.dp)
                ) {
                    Column(modifier = Modifier.padding(12.dp)) {
                        Text("Sync time", color = TextSecondary, fontSize = 12.sp)
                        Spacer(Modifier.height(8.dp))
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(8.dp)
                        ) {
                            OutlinedTextField(
                                value = editHour.toString().padStart(2, '0'),
                                onValueChange = { v ->
                                    val n = v.filter { it.isDigit() }.toIntOrNull()
                                    if (n != null && n in 0..23) {
                                        editHour = n
                                        onUpdateConfig(syncConfig.copy(syncHourOfDay = n))
                                    }
                                },
                                singleLine = true,
                                textStyle = MaterialTheme.typography.bodyLarge.copy(
                                    color = Amber, fontSize = 16.sp, textAlign = TextAlign.Center
                                ),
                                colors = OutlinedTextFieldDefaults.colors(
                                    focusedBorderColor = Amber,
                                    unfocusedBorderColor = TextSecondary.copy(alpha = 0.3f)
                                ),
                                modifier = Modifier.width(56.dp)
                            )
                            Text(":", color = TextSecondary, fontSize = 18.sp)
                            OutlinedTextField(
                                value = editMinute.toString().padStart(2, '0'),
                                onValueChange = { v ->
                                    val n = v.filter { it.isDigit() }.toIntOrNull()
                                    if (n != null && n in 0..59) {
                                        editMinute = n
                                        onUpdateConfig(syncConfig.copy(syncMinute = n))
                                    }
                                },
                                singleLine = true,
                                textStyle = MaterialTheme.typography.bodyLarge.copy(
                                    color = Amber, fontSize = 16.sp, textAlign = TextAlign.Center
                                ),
                                colors = OutlinedTextFieldDefaults.colors(
                                    focusedBorderColor = Amber,
                                    unfocusedBorderColor = TextSecondary.copy(alpha = 0.3f)
                                ),
                                modifier = Modifier.width(56.dp)
                            )
                        }
                    }
                }
            }
        }

        // Duration (HH:MM)
        item {
            Card(
                colors = CardDefaults.cardColors(containerColor = CardBg),
                shape = RoundedCornerShape(8.dp)
            ) {
                Column(modifier = Modifier.padding(12.dp)) {
                    Text("Download duration", color = TextSecondary, fontSize = 12.sp)
                    Spacer(Modifier.height(8.dp))
                    DownloadDurationField(syncConfig, onUpdateConfig)
                }
            }
        }

        // WiFi only
        item {
            Card(
                colors = CardDefaults.cardColors(containerColor = CardBg),
                shape = RoundedCornerShape(8.dp)
            ) {
                Row(
                    modifier = Modifier.fillMaxWidth().padding(12.dp),
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text("WiFi only", color = TextPrimary, fontSize = 14.sp, modifier = Modifier)
                    Switch(
                        checked = editWifiOnly,
                        onCheckedChange = {
                            editWifiOnly = it
                            onUpdateConfig(syncConfig.copy(wifiOnly = it))
                        },
                        colors = SwitchDefaults.colors(
                            checkedThumbColor = Color(0xFF64B5F6),
                            checkedTrackColor = Color(0xFF64B5F6).copy(alpha = 0.4f)
                        )
                    )
                }
            }
        }

        // Keep last N syncs
        item {
            Card(
                colors = CardDefaults.cardColors(containerColor = CardBg),
                shape = RoundedCornerShape(8.dp)
            ) {
                Column(modifier = Modifier.padding(12.dp)) {
                    Text("Keep last N syncs", color = TextSecondary, fontSize = 12.sp)
                    Spacer(Modifier.height(8.dp))
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        var editKeepN by remember {
                            mutableStateOf(syncConfig.keepLastNSyncs.toString())
                        }
                        OutlinedTextField(
                            value = editKeepN,
                            onValueChange = { v ->
                                editKeepN = v.filter { it.isDigit() }
                                val n = editKeepN.toIntOrNull()
                                if (n != null && n >= 1) {
                                    onUpdateConfig(syncConfig.copy(keepLastNSyncs = n))
                                }
                            },
                            singleLine = true,
                            textStyle = MaterialTheme.typography.bodyLarge.copy(
                                color = Amber, fontSize = 16.sp, textAlign = TextAlign.Center
                            ),
                            colors = OutlinedTextFieldDefaults.colors(
                                focusedBorderColor = Amber,
                                unfocusedBorderColor = TextSecondary.copy(alpha = 0.3f)
                            ),
                            modifier = Modifier.width(56.dp)
                        )
                        Spacer(Modifier.width(8.dp))
                        Text("sessions", color = TextSecondary, fontSize = 12.sp)
                    }
                }
            }
        }

        // Validation
        if (archiveInfo != null && archiveInfo.oldestStartSec > 0.0) {
            item {
                val archiveHours = (archiveInfo.newestEndSec - archiveInfo.oldestStartSec) / 3600.0
                val isValid = editDurationH <= archiveHours
                Surface(
                    shape = RoundedCornerShape(8.dp),
                    color = if (isValid) Green.copy(alpha = 0.1f) else Color.Red.copy(alpha = 0.1f)
                ) {
                    Column(modifier = Modifier.padding(12.dp)) {
                        Text(
                            "Server archive: %.1f hours".format(archiveHours),
                            color = TextSecondary, fontSize = 12.sp
                        )
                        Text(
                            "Requested: %.1f hours".format(editDurationH),
                            color = TextSecondary, fontSize = 12.sp
                        )
                        Text(
                            if (isValid) "✓ Fits in archive" else "⚠ Exceeds archive — will be clamped",
                            color = if (isValid) Green else Color.Red.copy(alpha = 0.8f),
                            fontSize = 12.sp,
                            fontWeight = FontWeight.Bold
                        )
                    }
                }
            }
        }

        // Download progress
        if (downloadProgress != null && downloadProgress.isRunning) {
            item {
                Card(
                    colors = CardDefaults.cardColors(containerColor = CardBg),
                    shape = RoundedCornerShape(8.dp)
                ) {
                    Column(modifier = Modifier.padding(12.dp)) {
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(8.dp)
                        ) {
                            CircularProgressIndicator(
                                modifier = Modifier.size(16.dp),
                                color = Color(0xFF64B5F6),
                                strokeWidth = 2.dp
                            )
                            Text(downloadProgress.currentAction, color = TextSecondary, fontSize = 12.sp)
                        }
                        if (downloadProgress.totalSegments > 0) {
                            Spacer(Modifier.height(8.dp))
                            LinearProgressIndicator(
                                progress = {
                                    downloadProgress.downloadedSegments.toFloat() /
                                        downloadProgress.totalSegments.coerceAtLeast(1)
                                },
                                modifier = Modifier.fillMaxWidth().height(4.dp),
                                color = Color(0xFF64B5F6),
                                trackColor = Surface
                            )
                            Text(
                                "${downloadProgress.downloadedSegments}/${downloadProgress.totalSegments} segments",
                                color = TextSecondary, fontSize = 11.sp
                            )
                        }
                        TextButton(onClick = onCancelDownload) {
                            Text("Cancel", color = Color.Red.copy(alpha = 0.8f), fontSize = 12.sp)
                        }
                    }
                }
            }
        } else if (downloadProgress?.error != null) {
            item {
                Surface(
                    shape = RoundedCornerShape(8.dp),
                    color = Color.Red.copy(alpha = 0.1f)
                ) {
                    Text(
                        downloadProgress.error,
                        color = Color.Red.copy(alpha = 0.8f),
                        fontSize = 12.sp,
                        modifier = Modifier.padding(12.dp)
                    )
                }
            }
        }

        // Save now button
        item {
            Button(
                onClick = onSaveNow,
                modifier = Modifier.fillMaxWidth(),
                colors = ButtonDefaults.buttonColors(containerColor = Color(0xFF1976D2)),
                enabled = downloadProgress?.isRunning != true
            ) {
                Icon(Icons.Default.Download, null, modifier = Modifier.size(18.dp))
                Spacer(Modifier.width(8.dp))
                Text(
                    if (syncConfig.initialSyncDone) "Download Now"
                    else "Save First Batch Now",
                    color = Color.White
                )
            }
        }

        // Last sync info
        item {
            if (syncConfig.lastSyncTimestamp > 0L) {
                val dateStr = SimpleDateFormat("yyyy-MM-dd HH:mm", Locale.getDefault())
                    .format(Date(syncConfig.lastSyncTimestamp))
                Text("Last sync: $dateStr", color = TextSecondary, fontSize = 11.sp)
            }
            Spacer(Modifier.height(16.dp))
        }
    }
}

// ── Offline content bar (shown above subtitle list when offline + has content) ──

@Composable
private fun OfflineContentBar(
    segmentCount: Int,
    syncConfig: SyncConfig,
    archiveInfo: com.crimobile.offline.ArchiveInfo?,
    downloadProgress: DownloadProgress?,
    offlineLocalRangeSec: Pair<Double, Double>?,
    onOpenSync: () -> Unit,
    onOpenNav: () -> Unit,
    onUpdateConfig: (SyncConfig) -> Unit,
    onSaveNow: () -> Unit,
    onCancelDownload: () -> Unit,
    onLoadArchiveInfo: () -> Unit
) {
    Surface(
        color = CardBg.copy(alpha = 0.6f),
        modifier = Modifier.fillMaxWidth()
    ) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 12.dp, vertical = 6.dp),
            verticalAlignment = Alignment.CenterVertically
        ) {
            // Segment count + date range (clickable → opens nav dialog)
            Column(
                modifier = Modifier.clickable { onOpenNav() }
            ) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Box(
                        modifier = Modifier
                            .size(6.dp)
                            .clip(CircleShape)
                            .background(Color(0xFF64B5F6))
                    )
                    Spacer(Modifier.width(4.dp))
                    Text(
                        "$segmentCount segments offline",
                        color = Color(0xFF64B5F6),
                        fontSize = 12.sp
                    )
                }
                if (offlineLocalRangeSec != null) {
                    val fmt = SimpleDateFormat("dd.MM.yyyy HH:mm", Locale.getDefault())
                    val from = fmt.format(Date((offlineLocalRangeSec.first * 1000).toLong()))
                    val to = fmt.format(Date((offlineLocalRangeSec.second * 1000).toLong()))
                    Text(
                        "$from – $to",
                        color = TextSecondary,
                        fontSize = 10.sp
                    )
                }
            }
            Spacer(Modifier.weight(1f))
            // Sync settings button
            TextButton(onClick = onOpenSync) {
                Icon(
                    Icons.Default.Settings,
                    contentDescription = "Sync settings",
                    tint = TextSecondary,
                    modifier = Modifier.size(16.dp)
                )
                Spacer(Modifier.width(4.dp))
                Text("Sync", color = TextSecondary, fontSize = 12.sp)
            }
        }
    }

    // Download progress inline
    if (downloadProgress != null && downloadProgress.isRunning) {
        Surface(color = Bg, modifier = Modifier.fillMaxWidth()) {
            Column(modifier = Modifier.padding(horizontal = 12.dp, vertical = 4.dp)) {
                LinearProgressIndicator(
                    progress = {
                        downloadProgress.downloadedSegments.toFloat() /
                            downloadProgress.totalSegments.coerceAtLeast(1)
                    },
                    modifier = Modifier.fillMaxWidth().height(3.dp),
                    color = Color(0xFF64B5F6),
                    trackColor = Surface
                )
                Text(
                    downloadProgress.currentAction,
                    color = TextSecondary,
                    fontSize = 10.sp
                )
            }
        }
    }
}

// ── Playback mode toggle (iOS-style pill with animated slide) ──────────

@Composable
private fun PlaybackModeToggle(
    mode: PlaybackMode,
    onToggle: (PlaybackMode) -> Unit,
    modifier: Modifier = Modifier
) {
    val isLive = mode == PlaybackMode.LIVE_STREAMING

    // Animate the sliding pill from Live (2dp) to Offline (64dp)
    val slideOffset by animateFloatAsState(
        targetValue = if (isLive) 2f else 64f,
        animationSpec = tween(durationMillis = 250),
        label = "toggleSlide"
    )

    Surface(
        shape = RoundedCornerShape(20.dp),
        color = CardBg,
        modifier = modifier.width(128.dp).height(32.dp)
    ) {
        Box(
            modifier = Modifier
                .fillMaxSize()
                .clickable { onToggle(if (isLive) PlaybackMode.OFFLINE_SAVED else PlaybackMode.LIVE_STREAMING) }
        ) {
            // Animated sliding pill
            Box(
                modifier = Modifier
                    .offset(x = slideOffset.dp)
                    .width(62.dp)
                    .height(28.dp)
                    .align(Alignment.CenterStart)
                    .clip(RoundedCornerShape(18.dp))
                    .background(if (isLive) Green else Color(0xFF1976D2))
            )

            // Live label
            Row(
                modifier = Modifier
                    .fillMaxHeight()
                    .width(64.dp)
                    .align(Alignment.CenterStart),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.Center
            ) {
                Text(
                    "Live",
                    color = if (isLive) Color.White else TextSecondary,
                    fontSize = 12.sp,
                    fontWeight = if (isLive) FontWeight.Bold else FontWeight.Normal
                )
            }

            // Offline label
            Row(
                modifier = Modifier
                    .fillMaxHeight()
                    .width(64.dp)
                    .align(Alignment.CenterEnd),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.Center
            ) {
                Text(
                    "Offline",
                    color = if (!isLive) Color.White else TextSecondary,
                    fontSize = 12.sp,
                    fontWeight = if (!isLive) FontWeight.Bold else FontWeight.Normal
                )
            }
        }
    }
}

// ── Sync settings dialog ──────────────────────────────────────────────

@Composable
private fun SyncSettingsDialog(
    syncConfig: SyncConfig,
    archiveInfo: com.crimobile.offline.ArchiveInfo?,
    downloadProgress: DownloadProgress?,
    onUpdateConfig: (SyncConfig) -> Unit,
    onSaveNow: () -> Unit,
    onCancelDownload: () -> Unit,
    onLoadArchiveInfo: () -> Unit,
    onDismiss: () -> Unit
) {
    // Load archive info on first show
    LaunchedEffect(Unit) {
        if (archiveInfo == null) {
            onLoadArchiveInfo()
        }
    }

    var editHour by remember { mutableStateOf(syncConfig.syncHourOfDay) }
    var editMinute by remember { mutableStateOf(syncConfig.syncMinute) }
    var editEnabled by remember { mutableStateOf(syncConfig.enabled) }
    var editWifiOnly by remember { mutableStateOf(syncConfig.wifiOnly) }
    val editDurationSec = syncConfig.syncDurationSec
    val editDurationH = editDurationSec / 3600.0

    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = CardBg,
        title = {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(Icons.Default.Settings, null, tint = TextSecondary)
                Spacer(Modifier.width(8.dp))
                Text("Offline Sync", color = TextPrimary, fontWeight = FontWeight.Bold)
            }
        },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                // ── Enabled toggle ──
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("Scheduled sync", color = TextPrimary, fontSize = 14.sp, modifier = Modifier)
                    Switch(
                        checked = editEnabled,
                        onCheckedChange = {
                            editEnabled = it
                            onUpdateConfig(syncConfig.copy(enabled = it))
                        },
                        colors = SwitchDefaults.colors(
                            checkedThumbColor = Color(0xFF64B5F6),
                            checkedTrackColor = Color(0xFF64B5F6).copy(alpha = 0.4f)
                        )
                    )
                }

                // ── Sync time ──
                if (editEnabled) {
                    Text("Daily sync time", color = TextSecondary, fontSize = 12.sp)
                    Row(
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(8.dp)
                    ) {
                        OutlinedTextField(
                            value = editHour.toString().padStart(2, '0'),
                            onValueChange = { v ->
                                val n = v.filter { it.isDigit() }.toIntOrNull()
                                if (n != null && n in 0..23) {
                                    editHour = n
                                    onUpdateConfig(syncConfig.copy(syncHourOfDay = n))
                                }
                            },
                            singleLine = true,
                            textStyle = MaterialTheme.typography.bodyLarge.copy(
                                color = Amber, fontSize = 16.sp, textAlign = TextAlign.Center
                            ),
                            colors = OutlinedTextFieldDefaults.colors(
                                focusedBorderColor = Amber,
                                unfocusedBorderColor = TextSecondary.copy(alpha = 0.3f)
                            ),
                            modifier = Modifier.width(56.dp)
                        )
                        Text(":", color = TextSecondary, fontSize = 18.sp)
                        OutlinedTextField(
                            value = editMinute.toString().padStart(2, '0'),
                            onValueChange = { v ->
                                val n = v.filter { it.isDigit() }.toIntOrNull()
                                if (n != null && n in 0..59) {
                                    editMinute = n
                                    onUpdateConfig(syncConfig.copy(syncMinute = n))
                                }
                            },
                            singleLine = true,
                            textStyle = MaterialTheme.typography.bodyLarge.copy(
                                color = Amber, fontSize = 16.sp, textAlign = TextAlign.Center
                            ),
                            colors = OutlinedTextFieldDefaults.colors(
                                focusedBorderColor = Amber,
                                unfocusedBorderColor = TextSecondary.copy(alpha = 0.3f)
                            ),
                            modifier = Modifier.width(56.dp)
                        )
                    }
                }

                // ── Duration (HH:MM) ──
                Text("Download duration", color = TextSecondary, fontSize = 12.sp)
                DownloadDurationField(syncConfig, onUpdateConfig)

                // ── WiFi only ──
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("WiFi only", color = TextPrimary, fontSize = 14.sp, modifier = Modifier)
                    Switch(
                        checked = editWifiOnly,
                        onCheckedChange = {
                            editWifiOnly = it
                            onUpdateConfig(syncConfig.copy(wifiOnly = it))
                        },
                        colors = SwitchDefaults.colors(
                            checkedThumbColor = Color(0xFF64B5F6),
                            checkedTrackColor = Color(0xFF64B5F6).copy(alpha = 0.4f)
                        )
                    )
                }

                // ── Keep last N syncs ──
                var editKeepN by remember {
                    mutableStateOf(syncConfig.keepLastNSyncs.toString())
                }
                Text("Keep last N syncs", color = TextSecondary, fontSize = 12.sp)
                Row(verticalAlignment = Alignment.CenterVertically) {
                    OutlinedTextField(
                        value = editKeepN,
                        onValueChange = { v ->
                            editKeepN = v.filter { it.isDigit() }
                            val n = editKeepN.toIntOrNull()
                            if (n != null && n >= 1) {
                                onUpdateConfig(syncConfig.copy(keepLastNSyncs = n))
                            }
                        },
                        singleLine = true,
                        textStyle = MaterialTheme.typography.bodyLarge.copy(
                            color = Amber, fontSize = 16.sp, textAlign = TextAlign.Center
                        ),
                        colors = OutlinedTextFieldDefaults.colors(
                            focusedBorderColor = Amber,
                            unfocusedBorderColor = TextSecondary.copy(alpha = 0.3f)
                        ),
                        modifier = Modifier.width(56.dp)
                    )
                    Spacer(Modifier.width(8.dp))
                    Text("sessions", color = TextSecondary, fontSize = 12.sp)
                }

                // ── Validation ──
                if (archiveInfo != null && archiveInfo.oldestStartSec > 0.0) {
                    val archiveHours = (archiveInfo.newestEndSec - archiveInfo.oldestStartSec) / 3600.0
                    val isValid = editDurationH <= archiveHours
                    Surface(
                        shape = RoundedCornerShape(8.dp),
                        color = if (isValid) Green.copy(alpha = 0.1f) else Color.Red.copy(alpha = 0.1f)
                    ) {
                        Column(modifier = Modifier.padding(8.dp)) {
                            Text(
                                "Server archive: %.1f hours".format(archiveHours),
                                color = TextSecondary, fontSize = 12.sp
                            )
                            Text(
                                "Requested: %.1f hours".format(editDurationH),
                                color = TextSecondary, fontSize = 12.sp
                            )
                            Text(
                                if (isValid) "✓ Valid" else "⚠ Exceeds archive — will be clamped",
                                color = if (isValid) Green else Color.Red.copy(alpha = 0.8f),
                                fontSize = 12.sp,
                                fontWeight = FontWeight.Bold
                            )
                        }
                    }
                }

                // ── Download progress ──
                if (downloadProgress != null && downloadProgress.isRunning) {
                    Column {
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(8.dp)
                        ) {
                            CircularProgressIndicator(
                                modifier = Modifier.size(16.dp),
                                color = Color(0xFF64B5F6),
                                strokeWidth = 2.dp
                            )
                            Text(
                                downloadProgress.currentAction,
                                color = TextSecondary, fontSize = 12.sp
                            )
                        }
                        if (downloadProgress.totalSegments > 0) {
                            Spacer(Modifier.height(4.dp))
                            LinearProgressIndicator(
                                progress = {
                                    downloadProgress.downloadedSegments.toFloat() /
                                        downloadProgress.totalSegments.coerceAtLeast(1)
                                },
                                modifier = Modifier.fillMaxWidth().height(4.dp),
                                color = Color(0xFF64B5F6),
                                trackColor = Surface
                            )
                            Text(
                                "${downloadProgress.downloadedSegments}/${downloadProgress.totalSegments} segments",
                                color = TextSecondary, fontSize = 11.sp
                            )
                        }
                        TextButton(onClick = onCancelDownload) {
                            Text("Cancel", color = Color.Red.copy(alpha = 0.8f), fontSize = 12.sp)
                        }
                    }
                } else if (downloadProgress?.error != null) {
                    Surface(
                        shape = RoundedCornerShape(8.dp),
                        color = Color.Red.copy(alpha = 0.1f)
                    ) {
                        Text(
                            downloadProgress.error,
                            color = Color.Red.copy(alpha = 0.8f),
                            fontSize = 12.sp,
                            modifier = Modifier.padding(8.dp)
                        )
                    }
                }

                // ── Save now button ──
                Button(
                    onClick = onSaveNow,
                    modifier = Modifier.fillMaxWidth(),
                    colors = ButtonDefaults.buttonColors(containerColor = Color(0xFF1976D2)),
                    enabled = downloadProgress?.isRunning != true
                ) {
                    Icon(Icons.Default.Download, null, modifier = Modifier.size(18.dp))
                    Spacer(Modifier.width(8.dp))
                    Text(
                        if (syncConfig.initialSyncDone) "Download Now"
                        else "Save First Batch Now",
                        color = Color.White
                    )
                }

                // ── Last sync info ──
                if (syncConfig.lastSyncTimestamp > 0L) {
                    val dateStr = java.text.SimpleDateFormat("yyyy-MM-dd HH:mm", java.util.Locale.getDefault())
                        .format(java.util.Date(syncConfig.lastSyncTimestamp))
                    Text("Last sync: $dateStr", color = TextSecondary, fontSize = 11.sp)
                }
            }
        },
        confirmButton = {
            TextButton(onClick = onDismiss) { Text("Close", color = Amber) }
        }
    )
}

// ── CJK punctuation-aware cell builder (extracted for testability) ──────

data class CharCell(val text: String, val word: WordEntry, val syllable: String)

/** Builds display cells. CJK punctuation is placed in separate zero-width
 *  cells so it visually sticks to the previous char without affecting pinyin
 *  alignment — pinyin always stays centered over its character.
 *
 *  Post-processing: if a punctuation cell would start a new line, it is
 *  merged into the previous cell's text (CJK typography rule). Otherwise
 *  it stays as a minimal-width cell next to the preceding character. */
fun buildCharCells(words: List<WordEntry>, showPinyin: Boolean): List<CharCell> {
    val cells = buildList<CharCell> {
        words.forEach { word ->
            val chars = word.text.toList()

            // Per-character pinyin from server (BKRS disambiguation).
            val charSyllables: List<String> = if (word.char_pinyin.isNotEmpty() && word.char_pinyin.size == chars.size) {
                word.char_pinyin.map { pinyinToDiacritic(it.lowercase()) }
            } else {
                // Fallback: split word pinyin by spaces (legacy / CC-CEDICT).
                val wp = pinyinToDiacritic(word.pinyin.lowercase())
                val syllables = wp.split(" ")
                if (showPinyin && syllables.size == chars.size) syllables
                else emptyList()
            }

            val pinyinAligned = showPinyin && charSyllables.isNotEmpty()
            var ci = 0
            while (ci < chars.size) {
                val ch = chars[ci]
                if (isCJKPunctuation(ch)) {
                    // Punctuation as separate zero-width cell — keeps pinyin on its char
                    add(CharCell(ch.toString(), word, ""))
                    ci++
                } else {
                    val syll = if (pinyinAligned) charSyllables.getOrElse(ci) { "" }
                        else if (ci == 0) pinyinToDiacritic(word.pinyin.lowercase()) else ""
                    if (ci + 1 < chars.size && isCJKPunctuation(chars[ci + 1])) {
                        // Char + following punct
                        add(CharCell(ch.toString(), word, syll))
                        add(CharCell(chars[ci + 1].toString(), word, ""))
                        ci += 2
                    } else {
                        add(CharCell(ch.toString(), word, syll))
                        ci++
                    }
                }
            }
        }
    }
    return cells
}

internal fun isCJKPunctuation(c: Char): Boolean {
    return c in "，。！？；：、\"\"''（）【】《》…—～·"
}

internal fun isPunctuationOnly(s: String): Boolean {
    return s.all { c ->
        val t = c.code
        // CJK punctuation ranges
        t in 0x3000..0x303F || t in 0xFF00..0xFF0F || t in 0xFF1A..0xFF20 ||
        t in 0xFF3B..0xFF40 || t in 0xFF5B..0xFF65 ||
        // ASCII punctuation
        t in 0x2000..0x206F || t in 0x20..0x2F || t in 0x3A..0x40 ||
        t in 0x5B..0x60 || t in 0x7B..0x7E ||
        // Other common punctuation
        c in "，。！？；：\"\"''（）【】《》…—～"
    }
}

private fun findToneVowel(s: String): Int {
    // Rule 1: 'a' or 'e' gets the mark
    s.forEachIndexed { i, c -> if (c == 'a' || c == 'e') return i }
    // Rule 2: 'ou' → 'o' gets the mark
    for (i in 0 until s.length - 1) { if (s[i] == 'o' && s[i + 1] == 'u') return i }
    // Rule 3: last vowel
    val vowels = "aeiouü"
    for (i in s.lastIndex downTo 0) { if (s[i] in vowels) return i }
    return -1
}

// ── Offline Navigation Dialog ─────────────────────────────────────────

@Composable
private fun OfflineNavDialog(
    sessions: List<com.crimobile.viewmodel.OfflineSessionInfo>,
    segments: List<SubtitleSegment>,
    selectedSessionId: String?,
    onSelectSession: (String) -> Unit,
    onSelectSegment: (Int) -> Unit,
    onDismiss: () -> Unit
) {
    val sessionsState = rememberLazyListState()
    val segmentsState = rememberLazyListState()
    Dialog(
        onDismissRequest = onDismiss,
        properties = DialogProperties(usePlatformDefaultWidth = false)
    ) {
        Surface(
            modifier = Modifier
                .fillMaxSize()
                .padding(16.dp),
            shape = RoundedCornerShape(12.dp),
            color = CardBg
        ) {
            Column(modifier = Modifier.fillMaxSize()) {
                // ── Header ──
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(horizontal = 16.dp, vertical = 12.dp),
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        "Offline Navigation",
                        color = TextPrimary,
                        fontSize = 18.sp,
                        fontWeight = FontWeight.Bold,
                        modifier = Modifier.weight(1f)
                    )
                    IconButton(onClick = onDismiss) {
                        Icon(Icons.Default.Close, "Close", tint = TextSecondary)
                    }
                }
                HorizontalDivider(color = TextSecondary.copy(alpha = 0.2f))

                // ── Two-panel body ──
                Row(modifier = Modifier.weight(1f)) {
                    // Left panel: sessions
                    Box(
                        modifier = Modifier
                            .weight(0.4f)
                            .fillMaxHeight()
                    ) {
                        LazyColumn(
                            state = sessionsState,
                            modifier = Modifier
                                .fillMaxSize()
                                .padding(8.dp)
                        ) {
                        items(sessions, key = { it.sessionId }) { session ->
                            val isSelected = session.sessionId == selectedSessionId
                            Surface(
                                modifier = Modifier
                                    .fillMaxWidth()
                                    .padding(vertical = 2.dp)
                                    .clickable { onSelectSession(session.sessionId) },
                                shape = RoundedCornerShape(8.dp),
                                color = if (isSelected) Amber.copy(alpha = 0.15f) else Color.Transparent
                            ) {
                                Column(modifier = Modifier.padding(10.dp)) {
                                    val dateStr = SimpleDateFormat(
                                        "yyyy.MMM.dd HH:mm",
                                        Locale.ENGLISH
                                    ).format(Date(session.startSec * 1000))
                                    Text(
                                        dateStr,
                                        color = if (isSelected) Amber else TextPrimary,
                                        fontSize = 13.sp,
                                        fontWeight = if (isSelected) FontWeight.Bold else FontWeight.Normal
                                    )
                                    Text(
                                        formatDuration(session.durationSec),
                                        color = TextSecondary,
                                        fontSize = 11.sp
                                    )
                                    Text(
                                        "${session.segmentCount} segments",
                                        color = TextSecondary.copy(alpha = 0.6f),
                                        fontSize = 10.sp
                                    )
                                }
                            }
                        }
                        }
                        ScrollThumb(sessionsState, modifier = Modifier.align(Alignment.TopEnd).padding(end = 4.dp))
                    }

                    // Vertical divider
                    VerticalDivider(
                        color = TextSecondary.copy(alpha = 0.15f),
                        modifier = Modifier.fillMaxHeight()
                    )

                    // Right panel: segments
                    Box(
                        modifier = Modifier
                            .weight(0.6f)
                            .fillMaxHeight()
                    ) {
                        LazyColumn(
                            state = segmentsState,
                            modifier = Modifier
                                .fillMaxSize()
                                .padding(8.dp)
                        ) {
                        if (segments.isEmpty()) {
                            item {
                                Box(
                                    modifier = Modifier
                                        .fillMaxWidth()
                                        .padding(16.dp),
                                    contentAlignment = Alignment.Center
                                ) {
                                    Text(
                                        "Select a session",
                                        color = TextSecondary,
                                        fontSize = 14.sp
                                    )
                                }
                            }
                        }
                        items(segments, key = { it.segment_id }) { seg ->
                            Surface(
                                modifier = Modifier
                                    .fillMaxWidth()
                                    .padding(vertical = 1.dp)
                                    .clickable { onSelectSegment(seg.segment_id) },
                                shape = RoundedCornerShape(6.dp),
                                color = Surface.copy(alpha = 0.5f)
                            ) {
                                Row(
                                    modifier = Modifier.padding(8.dp),
                                    verticalAlignment = Alignment.CenterVertically
                                ) {
                                    Icon(
                                        Icons.Default.PlayArrow,
                                        "Play",
                                        tint = Amber.copy(alpha = 0.6f),
                                        modifier = Modifier.size(16.dp)
                                    )
                                    Spacer(Modifier.width(6.dp))
                                    Text(
                                        "#${seg.segment_id} ${seg.text_zh.take(50)}",
                                        color = TextPrimary,
                                        fontSize = 13.sp,
                                        maxLines = 1,
                                        softWrap = false
                                    )
                                }
                            }
                        }
                        }
                        ScrollThumb(segmentsState, modifier = Modifier.align(Alignment.TopEnd).padding(end = 4.dp))
                    }
                }
            }
        }
    }
}

private fun formatDuration(durationSec: Int): String {
    val h = durationSec / 3600
    val m = (durationSec % 3600) / 60
    return if (h > 0) "[${h}h:${m.toString().padStart(2, '0')}m]"
    else "[${m}m]"
}
