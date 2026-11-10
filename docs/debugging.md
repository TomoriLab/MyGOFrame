# Debug mode

Open **Settings → App settings → Debug mode** to enable or disable diagnostics. App settings follows Presets and appears directly above About. The native switch saves the preference in `$XDG_CONFIG_HOME/mygoframe/app-settings.json`, with `~/.config` as the default configuration directory. Debug mode defaults to disabled.

```sh
./build/mygoframe --debug
./build/mygoframe --debug=false
```

An explicit CLI value overrides the preference for that launch. Changing the native switch persists the new choice. Preference writes run outside the GUI thread; closing the application waits for a pending write. Files use mode 0600 and atomic replacement. Invalid, oversized, or unsupported preference files remain intact and produce a visible settings error.

## Diagnostic history

When enabled, the application records command requests and completion, command failures, session-phase changes, GPU fallback, preset operations, recording-GPU discovery, capture-window lifetime, and distinct frame-notification/acquisition errors. Diagnostic identifiers and technical details stay in English independently of the UI language.

The recorder keeps the latest 256 events in memory. Each name is limited to 128 bytes and each detail to 4096 bytes, without splitting a valid UTF-8 sequence. Events indicate truncation. Copies prevent a short diagnostic substring from retaining a larger caller allocation. Overflow replaces the oldest event. Contended writes are discarded instead of waiting, and the discarded-event counter includes both cases.

The regular frame path does not emit events. Recording an event performs no file I/O. Disabled recording returns without allocations. Debug mode does not turn on GPU timing fences, change the video backend, restart capture, or enable automatic uploads. `--measure-latency` remains a separate diagnostic option.

## Reports and cleanup

**Copy diagnostic report** writes JSON to the clipboard. It includes release/build identity, the current session snapshot, recorder state, and retained events. The report can contain device names, paths, and error details from the current session. It contains no video frames or audio samples.

**Clear diagnostic history** releases retained event text and resets counters while preserving the mode. Disabling collection retains existing entries for review; process exit discards the history. Only the enable preference is written to disk. Timed GUI smoke checks include diagnostics in their JSON output when debug mode is enabled.

## Verification

```sh
LD_LIBRARY_PATH="$PWD/build" python3 tools/go-mygoframe.py test -race ./src/internal/diagnostics/... ./src/internal/desktop/presentation/...
./build/mygoframe --debug --smoke-ui-pattern --width 3840 --height 2160 --capture-window --smoke-ui-seconds 5
```

Tests cover preference restoration and overrides, preservation of invalid files, bounded Unicode events, snapshot ownership, contention, concurrent clearing/recording, zero-allocation disabled calls, and native switch/copy/clear controls in English, Korean, and Japanese. Runtime GUI checks validate native window startup and sharing-window recreation; synthetic patterns do not establish capture-device throughput or physical latency.
