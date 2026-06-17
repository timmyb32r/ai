package com.crimobile.model

/**
 * Finite-state machine states for playback.
 *
 * ```
 *         play()
 * IDLE ──────────→ LOADING ──(first segment)──→ PLAYING ⇄ PAUSED
 *   ↑                │                                  │
 *   └──── stop() ────┴────────── stop() ────────────────┘
 * ```
 *
 * ## State contracts
 * - [IDLE]: Nothing happening.  Play button visible.  No audio, no text.
 * - [LOADING]: Initial buffering.  Loader visible.  NO audio, NO text.
 * - [PLAYING]: Audio + text active.  Pause button visible.
 * - [PAUSED]: Audio stopped, text STAYS visible.  Play button visible.
 */
enum class PlaybackState {
    IDLE,
    LOADING,
    PLAYING,
    PAUSED,
}
