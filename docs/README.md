# Development and agent documentation

Read [Architecture and memory ownership](architecture.md) before changing the Go, Swift, or Rust boundaries. Use [Verification](verification.md) to select checks for the affected behavior. The root [README](../README.md) documents installation and application use.

## Source layout

| Area | Location |
| --- | --- |
| MyGo native UI, settings, and capture window | `cmd/mygoframe/` |
| Go bridge and frame access API | `internal/core/` |
| Swift session control | `core/swift/Engine.swift` |
| Rust video and audio core | `core/rust/src/` |
| Native ABI declarations | `core/include/` |
| Retained Swift media regression implementation | `tests/legacy/swift/` |
| Build and verification targets | `Makefile` |

Paths are relative to the repository root. Keep user-facing instructions in the root README and implementation or agent guidance in this directory.

## Review requirements

Changes generated with coding assistance require human review and validation before release.
