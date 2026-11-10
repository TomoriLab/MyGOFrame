# Settings preset storage

The MyGo native Presets page lists named capture configurations. Users can save a new preset, update the selected preset, apply it while video is stopped, and delete it after confirmation. Reload presets rereads changes made by another application instance. Selection within the Presets editor chooses the entry to edit. The top-level quick selector applies its selected entry.

## Stored settings

Each preset records a normalized name, video device path and name, FourCC, dimensions, exact rational frame interval, color range, requested processing backend, audio source and output IDs, monitoring volume and mute, and capture-window visibility. It does not record a frame pointer, session handle, renderer ID, negotiated fallback result, or live capture state. Applying a preset starts its selected audio input with the saved monitoring policy; video still requires Start video when capture is stopped. Startup restores the last selected name and never starts video or recording automatically.

Swift re-enumerates the devices and modes before applying. The video device path and saved name must match. The format, dimensions, and rational frame interval must be available; equivalent rational intervals are accepted without floating-point conversion. A missing source or an explicitly selected missing output rejects the preset. An empty source means monitoring is off; an empty output follows the current system default. Validation failures leave the session unchanged. A later audio-open failure is reported and restores the previous gain policy, but the old audio stream has already stopped.

Device paths follow the existing V4L2 enumeration. If device numbering changes, select the correct device manually and update the preset. The loader does not substitute another capture device or microphone.

## Quick start and switching

When saved presets exist, the command area shows a preset selector and Start video. Start video validates the selected preset, applies its audio/settings policy, and starts capture through one Swift session transition. Stop video stops capture and finalizes an active recording through the existing stop policy.

Changing the quick selector while video is running validates the new devices and mode before stopping the old session, then resumes capture with the new settings. A prevalidation failure preserves the current session. A later open failure attempts to restore the previous audio policy/input and video configuration; restoration failures are included in the error. Changing selection while stopped applies configuration and audio without starting video. Recording never starts automatically. The selector is disabled while recording or finalizing.

The selected name is saved with the document after successful activation. Configuration I/O uses the existing storage worker, so recording and frame delivery do not wait for the file write. Swift owns activation, restart, validation, and recovery policy; Go projects the result and resets presentation leases around transitions.

## File format and ownership

The file is `$XDG_CONFIG_HOME/mygoframe/presets.json`, falling back to `$HOME/.config/mygoframe/presets.json`. Relative XDG paths are ignored, as required by the [XDG Base Directory Specification](https://specifications.freedesktop.org/basedir/latest/). Newly created directories request mode 0700; existing directory permissions are preserved. `presets.lock` is a persistent lock file and may remain after exit.

| Limit | Value |
| --- | --- |
| Document schema | 2, with version 1 read compatibility |
| Presets per document | 64 |
| Name | 1 to 80 characters, at most 320 UTF-8 bytes |
| JSON file | 1 MiB |
| FFI request | 64 KiB |

Names are trimmed and canonically normalized. Duplicate names require the explicit Update preset operation. The name is JSON data and never becomes a file path. Invalid JSON, duplicate names, invalid settings, oversized files, and unsupported schema versions reject both reads and mutations. The application does not silently reset a damaged file. Back up and repair or move that file before saving again, then select Reload presets.

`src/swift/presets/domain/` owns validated values and device/mode invariants. `PresetService` applies operations through its injected `PresetRepository` port. `src/swift/presets/infrastructure/FilePresetRepository.swift` owns XDG path discovery, serialization, and file locking. A process lock serializes local calls; a POSIX record lock covers each complete read-modify-write transaction across cooperating instances. Reads are bounded before decoding. Foundation's atomic write replaces the document through a temporary file; it does not edit the existing JSON in place. Lock descriptors and read handles close on every return or error path. The [Foundation implementation](https://github.com/swiftlang/swift-corelibs-foundation/blob/main/Sources/Foundation/NSData.swift) documents the corresponding file-writing behavior. Atomic replacement is not a guarantee against every filesystem or power-loss failure.

Go projects the document into native controls and forwards owned JSON through the existing Swift string-release convention. Initial loading precedes capture. Later storage operations use a separate goroutine so configuration I/O never occupies the capture worker or renderer. Completion runs on the native UI thread and ignores results after the application closes. Shutdown joins any in-flight storage operation before returning. Apply requests use the existing serialized control worker. Rust's frame leases, GPU fences, callback ownership, and media pipeline are unchanged. No new runtime dependency or C implementation file is added.

## Verification

`make test` covers storage round trips, update/delete, Unicode names, rational intervals, malformed and future-version files, concurrent Swift calls, multiple application processes, unavailable-device rejection, native input controls, deletion confirmation, and compact-window overview and detail access. `make test-race`, `make test-sanitize`, and `make test-thread-sanitize` exercise the same bridge and production Swift store. Tests use temporary configuration directories.

Real GUI verification should save a preset while capture is stopped, change controls, apply it, start video, update its volume, close the application, and apply the saved preset after a new launch. Check muted audio transfer, capture-window visibility, deletion persistence, and unsuppressed process-exit memory reports. Diagnostic JSON and hardware display measurements remain separate from a complete memory-safety proof.
