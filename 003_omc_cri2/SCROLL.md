# Debug History: Recenter Button + Auto-Scroll

## Context
Android Jetpack Compose app (CRI Radio). `SubtitleList` composable with `LaunchedEffect(Unit)` + `withFrameNanos` loop for continuous karaoke-style scrolling. Goal: add a button in `BottomControl` (right of Play) that recenters on the active word and resumes auto-scroll.

## Architecture
```
CriApp (Scaffold)
├── bottomBar: BottomControl (button → sets flag)
└── content: SubtitleList
    ├── LaunchedEffect(recenterCount.intValue) — recenter handler (later removed)
    ├── LaunchedEffect(Unit) — main scroll loop
    │   while(isActive):
    │     check forceReinit flag
    │     withFrameNanos { ... scroll logic ... }
    └── LazyColumn(items=segments)
```

## Attempts Timeline

### Attempt 1: MutableState<Boolean> flag
- `val pendingRecenter = remember { mutableStateOf(false) }` in CriApp
- Button: `pendingRecenter.value = true`
- Loop: `if (pendingRecenter.value) { scrollToItem; initialized = false }`
- **Result:** Button logged "BUTTON pressed" but loop never saw it.
- **Root cause:** `MutableState.value` read in coroutine inside content slot doesn't observe changes from `bottomBar` slot (Scaffold snapshot isolation).

### Attempt 2: Channel<Unit>
- `val recenterChannel = remember { Channel<Unit>(Channel.CONFLATED) }`
- Button: `recenterChannel.trySend(Unit)`
- Loop: `recenterChannel.tryReceive().isSuccess`
- **Result:** "BUTTON pressed" appeared, no "REQUEST" log. Channel delivery failed across Scaffold slots.
- **Root cause:** Same snapshot isolation; channel operations in different composition subtrees.

### Attempt 3: AtomicBoolean
- `val pendingRecenter = remember { AtomicBoolean(false) }`
- Button: `pendingRecenter.set(true)`
- Loop: `if (pendingRecenter.getAndSet(false)) { ... }`
- **Result:** Same hash code confirmed (same object), but `getAndSet` always returned `false`.
- **Root cause:** Unknown — possibly ART/DEX optimizer issue with atomic operations, or snapshot system interference.

### Attempt 4: synchronized + BooleanArray
- `val lock = Any()` (NOT in `remember`!)
- `val pendingFlag = remember { BooleanArray(1) }`
- Button: `synchronized(lock) { pendingFlag[0] = true }`
- Loop: `synchronized(lock) { val v = pendingFlag[0]; pendingFlag[0] = false; v }`
- **Result:** `flag=false` in every alive log.
- **Root cause:** `lock = Any()` created a NEW lock object on every CriApp recomposition (~10/sec). Button captured new lock; LaunchedEffect(Unit) captured old lock. Different locks = no synchronization.

### Attempt 5: synchronized + BooleanArray + remember lock
- Same as #4 but `lock = remember { Any() }`
- **Result:** Same as #4. Lock hash matched, but `synchronized` still didn't work.
- **Root cause:** Unknown.

### Attempt 6: recenterCount (mutableIntStateOf) + LaunchedEffect(recenterCount)
- `val recenterCount = remember { mutableIntStateOf(0) }`
- Button: `recenterCount.intValue++`
- SubtitleList: `LaunchedEffect(recenterCount.intValue) { scrollToItem; initialized = false }`
- **Result:** LaunchedEffect FIRED! `EFFECT count=1 ... scrollToItem OK`. But main scroll loop stopped after that.
- **Root cause:** Two `LaunchedEffect`s in same composable — recenter effect's `scrollToItem` killed the main scroll loop's coroutine (CancellationException propagation).

### Attempt 7: forceReinit AtomicBoolean + main loop + LaunchedEffect(recenterCount)
- Recenter LaunchedEffect: `scrollToItem; forceReinit.set(true)`
- Main loop: `if (forceReinit.getAndSet(false)) { ... reinit ... }`
- **Result:** `scrollToItem OK` + `forceReinit SET` logged. But main loop never saw it (no `REINIT`). Then main loop died.
- **Root cause:** LaunchedEffect(recenterCount) killed the scroll loop. Same as #6.

### Attempt 8: forceReinit in ONE coroutine (no second LaunchedEffect)
- Button: `recenterCount.intValue++; forceReinit.set(true)`
- Main loop ONLY: `if (forceReinit.getAndSet(false)) { scrollToItem; initialized = false }`
- **Result:** `BUTTON pressed` logged but no `REINIT`. Loop still died. Then we discovered...
- **Root cause:** `CancellationException("Current mutation had a higher priority")` from Compose snapshot system. It kills the `LaunchedEffect(Unit)` coroutine because `withFrameNanos` reads `SnapshotStateList` (segments) which conflicts with ViewModel updates.

### Attempt 9: try-catch CancellationException around withFrameNanos
- ```kotlin
  try { withFrameNanos { ... } } 
  catch (e: CancellationException) { Log.w("Snapshot conflict — ignored") }
  ```
- **Result:** `COROUTINE CANCELLED` logged. Loop still died.
- **Root cause:** `CancellationException` caught in try-catch still sets `isActive = false` on the coroutine. `while(isActive)` exits.

### Attempt 10: try-catch on recenter block too
- Added CancellationException catch around the `forceReinit.getAndSet(false)` block with retry.
- **Result:** Same — no `REINIT` log, loop died.

### Attempt 11: withContext(NonCancellable) around entire loop
- ```kotlin
  LaunchedEffect(Unit) {
      withContext(NonCancellable) {
          while (isActive) { ... }
      }
  }
  ```
- **Result:** Loop still died.
- **Root cause:** `CancellationException` propagates to parent coroutine (LaunchedEffect) even though child (withContext) is NonCancellable. Parent gets cancelled → `isActive` becomes false.

## Confirmed Working Behavior (logged evidence)

### Version that worked (Attempt 6 variant):
```
BUTTON pressed count=0→1
EFFECT count=1 word=很 segs=21 playing=PLAYING
  SCROLL to targetIdx=16 (activeIdx=18)
  scrollToItem OK
INIT segs=21 activeIdx=18 initSpeed=77.1 px/sec
alive word=... init=true wasPlaying=true
```

### Version that worked (Attempt 7/8 variant):
```
BUTTON pressed count=0→1
REINIT in main loop word=很 segs=21 playing=PLAYING
  SCROLL to targetIdx=16 (activeIdx=18)
  scrollToItem OK
recenter done — init scheduled
INIT segs=21 activeIdx=18 initSpeed=77.1 px/sec
```

## Root Cause Summary

**Primary killer:** `java.util.concurrent.CancellationException: Current mutation had a higher priority`
- Thrown by: Compose snapshot system (`SnapshotStateList.mutate`)
- Conflict: ViewModel updates `state.segments` (via `StateFlow.copy()`) while scroll loop reads `listState.layoutInfo` inside `withFrameNanos`
- Effect: Kills `LaunchedEffect(Unit)` coroutine permanently (key=Unit, no restart)
- Environment: Android emulator with ART lock verification warnings (`SnapshotStateList.conditionalUpdate failed lock verification`)

**Secondary issues:**
- `MutableState` read in coroutine doesn't observe changes from different Scaffold slots
- `Channel` same issue across Scaffold composition subtrees
- `AtomicBoolean.getAndSet()` inexplicably returned `false` after `set(true)` on same object
- `val lock = Any()` without `remember` — recreated on every recomposition
- Two `LaunchedEffect`s in same composable — one's `scrollToItem` kills the other

## Proposed Fix (NOT YET IMPLEMENTED)

Replace `withFrameNanos` with `delay(16)`-based loop:

```kotlin
LaunchedEffect(Unit) {
    while (isActive) {
        // Read all state BEFORE any suspend calls
        val word = currentWord
        val segs = currentSegments
        val playing = currentPlaybackState == PlaybackState.PLAYING
        
        // Recenter check
        if (forceReinit.getAndSet(false)) {
            // scrollToItem + initialized = false
        }
        
        if (playing && word != null && segs.isNotEmpty()) {
            // Calculate scroll using speedController
            // listState.scrollBy(px)
        }
        
        delay(16) // ~60fps, no snapshot callbacks
    }
}
```

Rationale: `delay(16)` doesn't trigger Compose frame callbacks, avoiding snapshot conflicts. Trade-off: less precise timing than `withFrameNanos`.
