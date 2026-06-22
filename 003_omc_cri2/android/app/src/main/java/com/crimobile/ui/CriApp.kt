package com.crimobile.ui

import android.util.Log
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.gestures.scrollBy
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.Image
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.ClickableText
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.VolumeUp
import androidx.compose.material.icons.filled.*
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import androidx.compose.material3.*
import androidx.compose.runtime.*
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
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalDensity
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
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
                TopAppBar(
                    title = {
                        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.Center) {
                            // ── Mode toggle pill ──
                            PlaybackModeToggle(
                                mode = state.playbackMode,
                                onToggle = { newMode ->
                                    onAction(CriAction.SetPlaybackMode(newMode))
                                }
                            )
                            Spacer(Modifier.width(8.dp))
                            CriLogo(onTap = {
                                val now = System.currentTimeMillis()
                                if (now - lastDebugTapTime > 1000L) {
                                    debugTapCount = 0  // reset after 1s gap
                                }
                                lastDebugTapTime = now
                                debugTapCount++
                                if (debugTapCount >= 5) {
                                    debugTapCount = 0
                                    onAction(CriAction.EnableDebug)
                                }
                            })
                        }
                    },
                    colors = TopAppBarDefaults.topAppBarColors(containerColor = Surface),
                    actions = {
                        // Subtitle connection indicator
                        val isActive = state.playbackState == PlaybackState.PLAYING
                            || state.playbackState == PlaybackState.LOADING
                            || state.playbackState == PlaybackState.PAUSED
                        // Show "No subtitles" only in live mode when SSE is disconnected
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
                        // Offline segment count badge
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
                        // Subtitle delay badge (live mode only)
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
                        // Settings
                        IconButton(onClick = { showSettings = true }) {
                            Icon(Icons.Default.Settings, "Settings",
                                tint = TextSecondary)
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
                                onPinyinFontSize = { onAction(CriAction.SetPinyinFontSize(it)) }
                            )
                        }
                    }
                )
            },
            bottomBar = {
                BottomControl(state.playbackState,
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
                    && state.error == null) {
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
                                    onOpenSync = { showSyncSettings = true },
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
                }
            )
        }
    }
}

@Composable
private fun BottomControl(
    state: PlaybackState,
    onPlay: () -> Unit,
    onPause: () -> Unit,
    onResume: () -> Unit,
    onRecenter: () -> Unit
) {
    Surface(color = Surface, modifier = Modifier.fillMaxWidth().height(64.dp)) {
        BoxWithConstraints(
            modifier = Modifier.fillMaxSize().padding(horizontal = 16.dp),
            contentAlignment = Alignment.Center
        ) {
            val totalW = maxWidth
            val playW = 80.dp
            // d = distance(play.right, recenter.left) = distance(recenter.right, screen.right)
            // Derivation: totalW/2 + 96dp + 2d = totalW  →  d = totalW/4 − 48dp
            val d = totalW / 4 - 48.dp
            val spaceLeft = totalW / 2 - playW / 2  // to center play button

            Row(verticalAlignment = Alignment.CenterVertically) {
                if (d > 0.dp) {
                    Spacer(Modifier.width(spaceLeft))
                    // Play / Pause — centered
                    PlayPauseButton(state, onPlay, onPause, onResume)
                    Spacer(Modifier.width(d))
                    // Recenter — equidistant: play.right→recenter.left = recenter.right→screen.right
                    RecenterButton(onRecenter)
                } else {
                    // Narrow screen fallback: center the group
                    Spacer(Modifier.weight(1f))
                    PlayPauseButton(state, onPlay, onPause, onResume)
                    Spacer(Modifier.width(8.dp))
                    RecenterButton(onRecenter)
                    Spacer(Modifier.weight(1f))
                }
            }
        }
    }
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
private fun RecenterButton(onRecenter: () -> Unit) {
    IconButton(onClick = onRecenter, modifier = Modifier.size(56.dp)) {
        Icon(
            painter = painterResource(id = R.drawable.ic_recenter),
            contentDescription = "Recenter",
            modifier = Modifier.size(40.dp),
            tint = TextSecondary
        )
    }
}

@Composable
private fun CriLogo(onTap: (() -> Unit)? = null) {
    Image(
        painter = painterResource(id = R.drawable.cri_logo),
        contentDescription = "CRI China Radio International",
        modifier = Modifier
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

    // LaunchedEffect with Unit key — NEVER restarts during the lifecycle of this
    // composable. Recenter signals arrive via Channel<Unit> (non-blocking poll).
    LaunchedEffect(Unit) {
        var initialized = false
        var initSpeedPxPerSec = 0f
        var lastFrameNanos = 0L
        var totalScrolledPx = 0f
        var accumulatedPx = 0f
        var lastLogNanos = 0L
        var wasPlaying = false

        var scrollAction: (suspend () -> Unit)?

        var loopIterations = 0L
        while (isActive) {
            // ── Recenter: non-blocking channel poll ──
            if (recenterChannel.tryReceive().getOrNull() != null) {
                initialized = false
                Log.i("CRIRadio:scroll", "RECENTER triggered (channel)")
            }

            scrollAction = null
            loopIterations++

            withFrameNanos { frameNanos ->
                val word = currentWord
                val segs = currentSegments

                if (segs.isEmpty()) return@withFrameNanos

                val playing = currentPlaybackState == PlaybackState.PLAYING

                // Heartbeat: log that scroll loop is alive (every 5s)
                if (frameNanos - lastLogNanos > 5_000_000_000L) {
                    lastLogNanos = frameNanos
                    Log.d("CRIRadio:scroll", "alive word=${word?.text} segs=${segs.size} init=$initialized wasPlaying=$wasPlaying")
                }

                val viewportHeightPx = with(density) {
                    listState.layoutInfo.viewportSize.height.toFloat()
                }
                if (viewportHeightPx <= 0f) return@withFrameNanos

                val visibleItems = listState.layoutInfo.visibleItemsInfo
                if (visibleItems.isEmpty()) return@withFrameNanos

                // ── Effective word: fall back to lastActiveWord during silence gaps ──
                val effectiveWord = word ?: currentLastWord

                if (effectiveWord == null) {
                    // No word at all — smooth scroll with initSpeed if playing
                    if (playing && initialized && initSpeedPxPerSec > 0f) {
                        val rawDt = if (lastFrameNanos > 0) (frameNanos - lastFrameNanos) / 1_000_000_000f else 0.016f
                        val dt = rawDt
                        lastFrameNanos = frameNanos
                        val rawPx = initSpeedPxPerSec * dt
                        accumulatedPx += rawPx
                        val wholePx = accumulatedPx.toInt()
                        if (wholePx != 0) {
                            scrollAction = { listState.scrollBy(wholePx.toFloat()); totalScrolledPx += wholePx }
                            accumulatedPx -= wholePx
                        }
                    }
                    wasPlaying = playing
                    return@withFrameNanos
                }

                val activeIdx = segs.indexOfFirst { it.words.any { w -> w === effectiveWord } }
                if (activeIdx < 0) {
                    wasPlaying = playing
                    return@withFrameNanos
                }

                // ── INIT PHASE: center word (~25% from top), regardless of play state ──
                // Runs on: first frame after LaunchedEffect start (recenter or app launch),
                // and on pause→play transitions within the same coroutine.
                if (!initialized) {
                    val firstIdx = (activeIdx - (viewportHeightPx * 0.25f / (visibleItems.first().size.toFloat())).toInt())
                        .coerceAtLeast(0)
                    scrollAction = {
                        try { listState.scrollToItem(firstIdx, 0) } catch (_: Exception) { }
                    }

                    // init_speed: total_visible_pixel_height / delta_t
                    val firstVisibleIdx = visibleItems.first().index
                    val lastVisibleIdx = visibleItems.last().index
                    if (firstVisibleIdx in segs.indices && lastVisibleIdx in segs.indices) {
                        val firstSeg = segs[firstVisibleIdx]
                        val lastSeg = segs[lastVisibleIdx]
                        val firstWordTime = firstSeg.words.firstOrNull()?.start_sec ?: firstSeg.timeline_start_sec
                        val lastWordTime = lastSeg.words.lastOrNull()?.end_sec ?: lastSeg.timeline_end_sec
                        val deltaSec = (lastWordTime - firstWordTime).toFloat()
                        val totalVisiblePx = visibleItems.sumOf { it.size }.toFloat()
                        if (deltaSec > 0f && totalVisiblePx > 0f) {
                            initSpeedPxPerSec = totalVisiblePx / deltaSec
                        }
                    }
                    Log.i("CRIRadio:scroll",
                        "INIT segs=${segs.size} activeIdx=$activeIdx initSpeed=%.1f px/sec firstVis=$firstVisibleIdx lastVis=$lastVisibleIdx".format(
                            initSpeedPxPerSec))
                    initialized = true
                    lastFrameNanos = 0L
                    wasPlaying = playing
                    return@withFrameNanos
                }

                // ── PAUSE / PRONOUNCING: stop scrolling, wait for resume ──
                if (!playing || currentIsPronouncing) {
                    if (wasPlaying) {
                        Log.i("CRIRadio:scroll", "PAUSE — stopping scroll (loop=$loopIterations)")
                    }
                    wasPlaying = false
                    lastFrameNanos = 0L
                    return@withFrameNanos
                }

                // ── Pause→Play transition (within same coroutine): force re-init ──
                if (!wasPlaying) {
                    Log.i("CRIRadio:scroll", "RESUME — reinitializing scroll")
                    initialized = false
                    wasPlaying = true
                    return@withFrameNanos
                }

                // ── Normal scroll dt, sub-pixel accumulation for smooth scroll ──
                val rawDt = if (lastFrameNanos > 0) (frameNanos - lastFrameNanos) / 1_000_000_000f else 0.016f
                val dt = rawDt
                lastFrameNanos = frameNanos

                // ── Active word position on screen → scroll correction ──
                val position = speedController.getActiveWordVerticalPosition(
                    listState, segs, effectiveWord, viewportHeightPx
                )

                when {
                    position == 0f || position == 1f -> {
                        // Word off-screen — instant jump to bring it back
                        scrollAction = {
                            try {
                                listState.scrollToItem(activeIdx.coerceAtMost(segs.size - 1), 0)
                            } catch (_: Exception) { }
                        }
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
                        accumulatedPx += rawPx
                        val wholePx = accumulatedPx.toInt()
                        if (wholePx != 0) {
                            scrollAction = {
                                listState.scrollBy(wholePx.toFloat())
                                totalScrolledPx += wholePx
                            }
                            accumulatedPx -= wholePx
                        }
                    }
                }

                // Log every 2s
                if (frameNanos - lastLogNanos > 2_000_000_000L) {
                    lastLogNanos = frameNanos
                    val posStr = if (position != null) "%.2f".format(position) else "null"
                    val mult = if (position != null) speedController.getMultiplier(position) else 0f
                    Log.i("CRIRadio:scroll",
                        "pos=$posStr mult=%.2f speed=%.1f px/s pxFrame=%.2f dt=%.0fms totalPx=%.0f initSpeed=%.1f".format(
                            mult, initSpeedPxPerSec * mult, initSpeedPxPerSec * mult * dt, dt * 1000, totalScrolledPx, initSpeedPxPerSec))
                }
            }

            // Execute suspend actions outside withFrameNanos to avoid snapshot conflicts
            scrollAction?.invoke()
        }
    }

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
                                drawLine(Amber.copy(alpha = 0.15f), Offset(0f, 0f), Offset(0f, size.height), strokeWidth = 1.dp.toPx())
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
                        // Pinyin slot — always same height for alignment
                        if (showPinyin) {
                            Box(modifier = Modifier.height(18.dp).padding(bottom = 2.dp), contentAlignment = Alignment.Center) {
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
private fun WordPopupDialog(
    popup: WordPopupState,
    onDismiss: () -> Unit,
    onPronounce: () -> Unit,
    onSave: () -> Unit,
    onPlayFromHere: () -> Unit = {}
) {
    val clipboard = LocalClipboardManager.current
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = CardBg,
        title = {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(popup.word.text, fontSize = 36.sp, fontWeight = FontWeight.Bold, color = Amber)
                IconButton(onClick = {
                    clipboard.setText(AnnotatedString(popup.word.text))
                }) {
                    Icon(Icons.Default.ContentCopy, "Copy", tint = TextSecondary,
                        modifier = Modifier.size(20.dp))
                }
            }
        },
        text = {
            Column {
                DetailRow("Pinyin", pinyinToDiacritic(popup.pinyin))
                Spacer(Modifier.height(8.dp))
                DetailRow("Translation", popup.translation)
            }
        },
        confirmButton = {
            val durationSec = popup.word.end_sec - popup.word.start_sec
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                TextButton(onClick = onPlayFromHere) {
                    Icon(Icons.Default.PlayArrow, null, tint = Amber)
                    Spacer(Modifier.width(4.dp))
                    Text("Play", color = Amber)
                }
                TextButton(onClick = onPronounce) {
                    Icon(Icons.AutoMirrored.Filled.VolumeUp, null, tint = Amber)
                    Spacer(Modifier.width(4.dp))
                    Text("Pronounce (${"%.2f".format(durationSec)})", color = Amber)
                }
                TextButton(onClick = onSave) {
                    Icon(Icons.Default.Add, null, tint = Green)
                    Spacer(Modifier.width(4.dp))
                    Text("Save", color = Green)
                }
            }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Close", color = TextSecondary) } }
    )
}

@Composable
private fun DetailRow(label: String, value: String) {
    Row {
        Text("$label: ", color = TextSecondary, fontSize = 16.sp, fontWeight = FontWeight.Medium)
        Text(value, color = TextPrimary, fontSize = 16.sp)
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
    var editDurationH by remember { mutableStateOf(syncConfig.syncDurationSec / 3600.0) }
    var editDurationStr by remember {
        val h = syncConfig.syncDurationSec / 3600.0
        mutableStateOf(if (h == h.toInt().toDouble()) h.toInt().toString() else "%.1f".format(h))
    }

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

        // Duration
        item {
            Card(
                colors = CardDefaults.cardColors(containerColor = CardBg),
                shape = RoundedCornerShape(8.dp)
            ) {
                Column(modifier = Modifier.padding(12.dp)) {
                    Text("Download duration", color = TextSecondary, fontSize = 12.sp)
                    Spacer(Modifier.height(8.dp))
                    Row(
                        horizontalArrangement = Arrangement.spacedBy(6.dp)
                    ) {
                        listOf(1.0 to "1h", 2.0 to "2h", 2.5 to "2.5h", 3.0 to "3h").forEach { (hours, label) ->
                            val isSelected = kotlin.math.abs(editDurationH - hours) < 0.01
                            FilledTonalButton(
                                onClick = {
                                    editDurationH = hours
                                    editDurationStr = label.dropLast(1)
                                    onUpdateConfig(syncConfig.copy(syncDurationSec = (hours * 3600).toInt()))
                                },
                                modifier = Modifier.height(32.dp),
                                colors = ButtonDefaults.filledTonalButtonColors(
                                    containerColor = if (isSelected) Amber.copy(alpha = 0.2f) else Surface
                                )
                            ) {
                                Text(label, fontSize = 12.sp,
                                    color = if (isSelected) Amber else TextSecondary)
                            }
                        }
                    }
                    Spacer(Modifier.height(8.dp))
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        OutlinedTextField(
                            value = editDurationStr,
                            onValueChange = { v ->
                                editDurationStr = v
                                val h = v.toDoubleOrNull()
                                if (h != null && h >= 0.1 && h <= 24) {
                                    editDurationH = h
                                    onUpdateConfig(syncConfig.copy(syncDurationSec = (h * 3600).toInt()))
                                }
                            },
                            singleLine = true,
                            textStyle = MaterialTheme.typography.bodyLarge.copy(
                                color = Amber, fontSize = 14.sp, textAlign = TextAlign.Center
                            ),
                            colors = OutlinedTextFieldDefaults.colors(
                                focusedBorderColor = Amber,
                                unfocusedBorderColor = TextSecondary.copy(alpha = 0.3f)
                            ),
                            modifier = Modifier.width(72.dp),
                            label = { Text("Custom", color = TextSecondary, fontSize = 10.sp) }
                        )
                        Spacer(Modifier.width(4.dp))
                        Text("hours", color = TextSecondary, fontSize = 12.sp)
                    }
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
    onOpenSync: () -> Unit,
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
            // Segment count
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
            Spacer(Modifier.weight(1f))
            // Sync settings button
            TextButton(onClick = onOpenSync) {
                Icon(
                    Icons.Default.Sync,
                    contentDescription = "Sync settings",
                    tint = Color(0xFF64B5F6),
                    modifier = Modifier.size(16.dp)
                )
                Spacer(Modifier.width(4.dp))
                Text("Sync", color = Color(0xFF64B5F6), fontSize = 12.sp)
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
    onToggle: (PlaybackMode) -> Unit
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
        modifier = Modifier.width(128.dp).height(32.dp)
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
    var editDurationH by remember { mutableStateOf(syncConfig.syncDurationSec / 3600.0) }
    var editDurationStr by remember {
        val h = syncConfig.syncDurationSec / 3600.0
        mutableStateOf(if (h == h.toInt().toDouble()) h.toInt().toString() else "%.1f".format(h))
    }

    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = CardBg,
        title = {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(Icons.Default.Sync, null, tint = Color(0xFF64B5F6))
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

                // ── Duration ──
                Text("Download duration", color = TextSecondary, fontSize = 12.sp)
                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(6.dp)
                ) {
                    // Preset buttons
                    listOf(1.0 to "1h", 2.0 to "2h", 2.5 to "2.5h", 3.0 to "3h").forEach { (hours, label) ->
                        val isSelected = kotlin.math.abs(editDurationH - hours) < 0.01
                        FilledTonalButton(
                            onClick = {
                                editDurationH = hours
                                editDurationStr = label.dropLast(1)
                                onUpdateConfig(syncConfig.copy(syncDurationSec = (hours * 3600).toInt()))
                            },
                            modifier = Modifier.height(32.dp),
                            colors = ButtonDefaults.filledTonalButtonColors(
                                containerColor = if (isSelected) Amber.copy(alpha = 0.2f) else Surface
                            )
                        ) {
                            Text(label, fontSize = 12.sp,
                                color = if (isSelected) Amber else TextSecondary)
                        }
                    }
                }
                // Custom duration
                Row(verticalAlignment = Alignment.CenterVertically) {
                    OutlinedTextField(
                        value = editDurationStr,
                        onValueChange = { v ->
                            editDurationStr = v
                            val h = v.toDoubleOrNull()
                            if (h != null && h >= 0.1 && h <= 24) {
                                editDurationH = h
                                onUpdateConfig(syncConfig.copy(syncDurationSec = (h * 3600).toInt()))
                            }
                        },
                        singleLine = true,
                        textStyle = MaterialTheme.typography.bodyLarge.copy(
                            color = Amber, fontSize = 14.sp, textAlign = TextAlign.Center
                        ),
                        colors = OutlinedTextFieldDefaults.colors(
                            focusedBorderColor = Amber,
                            unfocusedBorderColor = TextSecondary.copy(alpha = 0.3f)
                        ),
                        modifier = Modifier.width(72.dp),
                        label = { Text("Custom", color = TextSecondary, fontSize = 10.sp) }
                    )
                    Spacer(Modifier.width(4.dp))
                    Text("hours", color = TextSecondary, fontSize = 12.sp)
                }

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
            val pinyin = pinyinToDiacritic(word.pinyin.lowercase())
            val syllables = pinyin.split(" ")
            val chars = word.text.toList()
            val pinyinAligned = showPinyin && syllables.size == chars.size
            var ci = 0
            while (ci < chars.size) {
                val ch = chars[ci]
                if (isCJKPunctuation(ch)) {
                    // Punctuation as separate zero-width cell — keeps pinyin on its char
                    add(CharCell(ch.toString(), word, ""))
                    ci++
                } else {
                    val syll = if (pinyinAligned) syllables.getOrElse(ci) { "" }
                        else if (ci == 0) pinyin else ""
                    if (ci + 1 < chars.size && isCJKPunctuation(chars[ci + 1])) {
                        // Char + following punct: punct gets minimal width
                        add(CharCell(ch.toString(), word, syll))  // char with pinyin
                        add(CharCell(chars[ci + 1].toString(), word, ""))  // punct, no pinyin
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
