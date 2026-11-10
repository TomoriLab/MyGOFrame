# Development and agent documentation

Read [Architecture and memory ownership](architecture.md) before changing the Go, Swift, or Rust boundaries. Use [Verification](verification.md) to select checks for the affected behavior. Use [Debian packaging](debian.md) to build and validate local Debian packages. The root [README](../README.md) documents installation and application use.

## Language boundaries

- [Go, Swift, and Rust responsibilities](language-boundaries.md): session policy, versioned handles, resource ownership, and frame notifications.
- [Recording](recording.md): GPU encoding, capture audio, file ownership, and validation.
- [Settings preset storage](presets.md): native controls, persistent configuration, validation, and recovery.

- [Native settings design](ui-design.md): Fluent styling, overview cards, and hierarchical detail navigation.
- [Debug mode](debugging.md): bounded diagnostics, saved enable preference, and report copying.
- [Localization](l10n.md): translation catalogs, native app-language selection, and fallback verification.
- [Release version management](versioning.md): canonical release metadata, synchronized manifests, and build identity.
- [App information and licenses](about-and-licenses.md): native modal navigation, embedded notices, and build-time component inventory.

## Renderer integration

- [MyGo renderer patch](mygo-renderer.md): build-local extension, sparse vendor overlay, and context-bound external images.
- [DMA-BUF formats](dmabuf-formats.md): raw format layouts, GPU conversion, fallback, and device validation.
- [NVIDIA EGL allocation leaks](egl-leaks.md): native root-cause controls, version-limited compatibility, and unsuppressed sanitizer verification.
- [GUI presentation](gui-presentation.md): GLES composition, per-window update scheduling, and native readback controls.
- [Native text ownership](font-memory.md): terminal Pango cleanup, transient GLib ownership, and Fontconfig relative-offset reports.
- [Native display and input-method ownership](native-memory.md): GDK/EGL, Wayland/Cairo, and Fcitx repairs with unsuppressed GUI checks.
- [Application-private native runtime build](native-runtime-build.md): pinned sources, ABI validation, startup selection, and Debian integration.

## Research

- [Hardware-accelerated recording plan](recording-plan.md): GPU selection, codecs, audio routing, ownership, and staged validation.
- [DMA-BUF feasibility](dmabuf-research.md): actual capture-to-GPU import results, renderer requirements, and buffer lifetime design.
- [DMA-BUF color validation](dmabuf-color-validation.md): production color comparisons, source cache-coherency diagnosis, and measured limits.

## Source layout

| Area | Location |
| --- | --- |
| Executable entry point | `src/cmd/mygoframe/` |
| MyGo native UI, settings, capture window, and diagnostic CLI | `src/internal/desktop/presentation/` |
| Go domain contracts and injectable ports | `src/internal/<domain>/domain/`, `src/internal/<domain>/application/` |
| Production adapter composition | `src/internal/bootstrap/`, `src/swift/bootstrap/` |
| Go bridge and frame access API | `src/internal/session/infrastructure/native/` |
| Swift domain rules, application services, and native adapters | `src/swift/<domain>/` |
| Rust capture, audio, recording, and rendering domains | `src/rust/src/<domain>/` |
| Native ABI declarations | `src/abi/` |
| Retained Swift media regression implementation | `tests/legacy/swift/` |
| Build and verification targets | `Makefile` |

Paths are relative to the repository root. Keep user-facing instructions in the root README and implementation or agent guidance in this directory.

## Review requirements

Changes generated with coding assistance require human review and validation before release.
