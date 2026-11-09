# Development and agent documentation

Read [Architecture and memory ownership](architecture.md) before changing the Go, Swift, or Rust boundaries. Use [Verification](verification.md) to select checks for the affected behavior. Use [Debian packaging](debian.md) to build and validate local Debian packages. The root [README](../README.md) documents installation and application use.

## Language boundaries

- [Go, Swift, and Rust responsibilities](language-boundaries.md): session policy, versioned handles, resource ownership, and frame notifications.

## Renderer integration

- [MyGo renderer patch](mygo-renderer.md): build-local extension, sparse vendor overlay, and context-bound external images.
- [DMA-BUF formats](dmabuf-formats.md): raw format layouts, GPU conversion, fallback, and device validation.
- [NVIDIA EGL allocation leaks](egl-leaks.md): native root-cause controls, version-limited compatibility, and unsuppressed sanitizer verification.
- [GUI presentation](gui-presentation.md): GLES composition, per-window update scheduling, and native readback controls.
- [Native text ownership](font-memory.md): terminal Pango cleanup, transient GLib ownership, and Fontconfig relative-offset reports.
- [Native display and input-method ownership](native-memory.md): GDK/EGL, Wayland/Cairo, and Fcitx repairs with unsuppressed GUI checks.
- [Application-private native runtime build](native-runtime-build.md): pinned sources, ABI validation, startup selection, and Debian integration.

## Research

- [DMA-BUF feasibility](dmabuf-research.md): actual capture-to-GPU import results, renderer requirements, and buffer lifetime design.
- [DMA-BUF color validation](dmabuf-color-validation.md): production color comparisons, source cache-coherency diagnosis, and measured limits.

## Source layout

| Area | Location |
| --- | --- |
| MyGo native UI, settings, and capture window | `cmd/mygoframe/` |
| Go bridge and frame access API | `internal/core/` |
| Swift session control and application policies | `core/swift/` |
| Rust video and audio core | `core/rust/src/` |
| Native ABI declarations | `core/include/` |
| Retained Swift media regression implementation | `tests/legacy/swift/` |
| Build and verification targets | `Makefile` |

Paths are relative to the repository root. Keep user-facing instructions in the root README and implementation or agent guidance in this directory.

## Review requirements

Changes generated with coding assistance require human review and validation before release.
