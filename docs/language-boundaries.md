# Go, Swift, and Rust responsibilities

Go owns the MyGo native interface and renderer integration. Swift owns application policy and session control. Rust owns capture, conversion, playback, native storage, and GPU completion. Native UI composition and both top-level windows remain in MyGo.

Production sources are under `src/` and grouped by domain within each toolchain. The [dependency rules](architecture.md#dependency-rules) define inward dependencies, injected ports, and bootstrap composition.

## Control and presentation

| Layer | Responsibilities | Owned resources |
| --- | --- | --- |
| Go | Native controls, display labels, shortcuts, viewport fitting, window lifecycle, renderer callbacks | MyGo windows, views, presentation adapters |
| Swift | Mode reconciliation, device selection, audio gain policy, session transitions, backend recovery, status snapshots | Session registry, requested configuration, Rust session owners |
| Rust | V4L2, FFmpeg, PipeWire, DMA-BUF, color conversion, frame storage, GPU fences, frame notification | Descriptors, mappings, buffers, threads, GPU resources |

Control requests follow Go → Swift → Rust. Renderer callbacks call Rust directly while MyGo's matching GL context is current. Frame storage remains leased throughout presentation. Audio realtime callbacks do not enter Swift or Go.

Swift's session state is noncopyable. A `NativeHandle` has one destruction owner and releases its Rust allocation in `deinit`; temporary session references cannot duplicate that owner. The registry returns monotonically increasing integer handles instead of exposing Swift object addresses. Registry removal prevents new calls. An already-resolved call retains its owner until it finishes under the session lock. Destroying an engine then marks it closed and stops its sessions. Retired handles cannot alias replacement engines.

`LockedState` confines mutable state behind `NSLock`. The closure is synchronous, takes `inout sending` state, and returns a `Sendable` result. Its narrowly scoped `@unchecked Sendable` conformance relies on private storage and locking every access. The synchronous FFI output wrapper has a separate explicit borrowing contract: the caller's output pointer remains valid until the call returns and is never stored in a session.

Swift 6.3.3's Linux `Synchronization.Mutex` produced TSan reports in a standalone protected-counter control. The equivalent `NSLock` control and the production session concurrency tests passed. Production and sanitizer builds both use the same pthread-backed `NSLock`; no sanitizer annotations, exclusions, or suppression files are used to hide those reports.

## State and policy

The Go-to-Swift session ABI is version 2 and uses `uint64_t` handles. The launcher checks the API version before creating a session. The executable and both core libraries must be deployed together. The Rust frame descriptor ABI remains version 1.

Status snapshots use schema version 1 and preserve integer counters. Requested video configuration is separate from the negotiated device result. Video and audio each have a phase: stopped, starting, running, stopping, or failed. Starting and stopping occur inside synchronous control operations; UI busy feedback remains a Go concern. A command ID orders accepted mutations. Video start and stop advance the session generation. This control generation is independent of Rust's allocation generation in a DMA descriptor. CPU recovery preserves the original requested configuration while reporting the CPU backend as the negotiated result.

Mode reconciliation operates on numeric dimensions and rational frame intervals in Swift. Go maps the returned choices to native control labels. Device refresh retains an existing device when present and selects an available replacement when necessary. An audio enumeration failure does not itself prove device removal; confirmed source or output removal stops monitoring. Swift preserves requested volume during mute and computes the effective gain sent to Rust.

A renderer failure includes the session generation it observed. Go first retires displayed references and reports the failure. Swift restarts with CPU conversion only if that generation still owns a DMA session. Delayed errors from stopped or superseded sessions cannot restart the current capture. Outstanding Rust GPU fence leases retain source storage independently of this transition.

## Storage and notification

The CPU pool exposes a writable slice only through a `FrameReservation`. Publishing consumes the reservation. Dropping it cancels an unpublished write, including early-return and unwind paths. Reader leases prevent a slot from being reserved again. Raw token recovery stays at the Rust FFI boundary; GPU rendering receives a retained `Arc<DmaFrame>` internally. Audio callback entry uses an allocation-free atomic guard that releases its gate on scope exit.

Each Swift engine owns a Rust `eventfd` notification source. Capture sessions retain it and notify after publishing a frame or terminating. Attachment signals once so a frame published before attachment is not missed. The Go worker owns a duplicated descriptor and uses a pollable blocking read with a single pending wakeup. The event counter and UI wakeup channel coalesce notifications; neither queues video frames. Both windows share this worker. There must be only one notification consumer per engine.

The descriptor survives video restart. Worker shutdown closes its duplicate to cancel a pending read, joins the reader, releases pending frames, and closes the session. `EFD_NONBLOCK` prevents a stalled consumer from blocking the producer when the counter saturates. Status refresh remains a separate 200 ms timer. The GUI worker has no 2 ms frame polling timer; CLI capture smoke checks retain their independent diagnostic polling loop. See the [Linux eventfd contract](https://man7.org/linux/man-pages/man2/eventfd.2.html).

## Verification boundaries

`make test` includes production Swift policy and registry tests, Go bridge and UI tests, Rust frame-reservation and notification tests, and existing media regressions. `make test-race` exercises Go scheduling and adapters. `make test-sanitize` additionally instruments the production Swift session tests and Rust unit tests with ASan. `make test-thread-sanitize` instruments the production Swift control layer and retained Swift audio regressions with TSan.

Full GUI checks separately exercise live DMA capture, CPU fallback, two windows, resize and fullscreen transitions, repeated window recreation, video restart, stale renderer errors, and muted audio transfer. Go/cgo ASan plus native allocation tracking does not imply that every instruction in Rust, Swift, and vendor libraries was instrumented in the same GUI process. Color validation and performance comparisons remain actual-device checks; see [Verification](verification.md). Human review and validation remain required before release.
