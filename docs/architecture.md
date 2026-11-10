# Architecture and memory ownership

Go implements the MyGo native UI. Swift owns application policies and video and audio sessions and exposes the control API to Go. Rust owns V4L2 capture, FFmpeg conversion, frame storage, and PipeWire monitoring. The UI uses native Go views without HTML, JavaScript, or a WebView.

Paths in this document are relative to the repository root. Build and runtime requirements are in the [user guide](../README.md#build-and-run). Validation commands are in [Verification](verification.md).

## Directory map

All production source lives under `src/`. Toolchain roots remain separate, and source within each root is grouped by domain.

```text
src/
  cmd/{mygoframe,install-desktop,package-assets}/
  internal/
    bootstrap/                       # Adapter composition and startup.
    capture/{domain,application}/     # Video values, leased frames, GPU ports.
    audio/domain/
    recording/domain/
    presets/domain/
    session/{domain,application}/     # Aggregate contracts and session port.
    session/infrastructure/native/   # Cgo, frame ownership, native GPU adapter.
    desktop/{application,domain,presentation}/
    about/{domain,infrastructure}/
    diagnostics/{domain,infrastructure}/ # Bounded diagnostic text and preferences.
    version/domain/                  # Generated release/build identity.
    platform/infrastructure/
  swift/
    bootstrap/                       # Native composition and exported ABI.
    capture/domain/
    audio/domain/
    recording/{domain,infrastructure}/
    presets/{domain,application,infrastructure}/
    session/{domain,application,infrastructure}/
    platform/infrastructure/
  rust/
    Cargo.toml
    src/
      capture/{domain,infrastructure}/
      audio/infrastructure/
      recording/{domain,infrastructure}/
      rendering/infrastructure/
      media/infrastructure/          # Shared bounded taps and callback gate.
      platform/infrastructure/
  abi/                               # C declarations and Swift module map.
```

`src/cmd/mygoframe/main.go` is a thin entry point. MyGo views, controls, the frame pump, and diagnostic CLI are in `src/internal/desktop/presentation/`. Tests stay in `tests/` or beside their Go/Rust implementation. Build and verification scripts stay in `tools/`. External patches retain their existing packaging locations.

## Dependency rules

The following rules adapt [Kucheat's dependency rules](https://github.com/TomoriLab/Kucheat/blob/main/docs/architecture.md#dependency-rules) to this native Go, Swift, and Rust application:

- Domain code contains values, validation, and invariants. It does not discover paths, access files, call native APIs, or depend on outer layers. Lexical path validation and in-memory encoding remain pure operations.
- Application code depends on domain contracts and its own ports. It does not import infrastructure, presentation, or MyGo implementations or select concrete adapters.
- Infrastructure implements ports and owns native protocols, filesystem behavior, descriptors, allocations, and operating-system integration.
- Presentation renders state and translates user actions into injected service calls. GUI and CLI share Swift session policies through the same native adapter.
- Go and Swift bootstrap code assembles concrete adapters. Services receive their media runtime, preset repository, state factory, and recording path provider through explicit dependencies.
- Pure transformations remain functions or value methods. Ports represent replaceable effects or resource lifetimes rather than wrapping stateless helpers in objects.

Capture and audio publish into shared bounded media transports in Rust; they do not depend on the recording implementation. Layout checks are independent of pool allocation and generated bindings. The recording adapter converts C metadata into domain values at its boundary.

Go aggregate snapshots alias domain-owned values without importing adapters. Presentation receives frame and renderer interfaces; native tokens and their locks remain inside the adapter. The session port transfers ownership of an event descriptor without allowing domain code to discover or open files.

Swift retains one ABI library and its noncopyable owners. All domain/application sources also compile as an isolated policy executable without `NativeCore`, native headers, Rust, or devices. The ABI adapter borrows the private video owner synchronously inside the registry lock. Production composition remains outside application services.

`make test-architecture` checks dependency directions, builds Go domains with cgo disabled, and runs injected Swift policy tests. Rejected dependency fixtures are in `tests/test_architecture.py`. Ownership, race, sanitizer, and device checks remain required for changes to those boundaries.

## Video and frame ownership

Swift selects the video backend and owns session restart control. `auto` tries the Rust DMA-BUF backend for progressive NV12, P010, YUYV, UYVY, BGR24, and RGB24 SDR and retains the CPU backend for unsupported formats or failures. Rust owns the format allowlist and negotiated-layout validation; Swift forwards rejected requests to the CPU backend with the reported reason. Go invokes GPU preparation only from the native MyGo renderer's current context. Presentation failures retire the external image and request a CPU restart through Swift.

### CPU backend

V4L2 requests three MMAP buffers. Each read drains available completed buffers and converts only the newest one. The drain is bounded by the allocated driver-buffer count so a high-frame-rate device cannot indefinitely monopolize the capture thread. The driver may allocate more buffers than requested. See the [V4L2 streaming documentation](https://docs.kernel.org/userspace-api/media/v4l/mmap.html).

Malformed startup JPEG packets and truncated raw frames are discarded so switching formats can recover. Persistent decode corruption stops capture with an error after 120 consecutive invalid frames. The skipped-frame counter records observed software drops; it cannot account for frames discarded inside the capture hardware.

The Rust media core keeps three reusable RGBA buffers. Each buffer is limited to 128 MiB, so one active pool uses at most 384 MiB for RGBA storage. Requested and negotiated dimensions are checked before pool allocation; 3840 × 2160 and 7680 × 4320 fit this limit. Other allocations, including V4L2 buffers, decoding, MyGo scenes, and pools retained by outstanding frame leases, are outside this figure.

A displayed frame holds a lease on its buffer. The producer cannot overwrite that buffer until Go releases the lease. The latest frame replaces older unpublished frames; the application does not queue frames for timestamp-based playback. Frame leases remain valid after capture stops or the engine is destroyed. Rust signals a shared `eventfd` after publication. Go coalesces wakeups and redraw requests and updates one MyGo bitmap without periodic frame polling. It reuses a Go-owned blank buffer when detaching a scene from native pixels before releasing its frame. This backend requires CPU color conversion and a GPU texture upload.

### DMA-BUF backend

The DMA backend requests six V4L2 MMAP slots and exports their allocations with `VIDIOC_EXPBUF`. The driver may return a different count within the checked range. It requires orphaned-buffer support so outstanding GPU reads can keep exported storage valid after `STREAMOFF`. Each source allocation is limited to 128 MiB and the complete capture pool to 512 MiB. The backend does not allocate the CPU RGBA pool or run the CPU converter in its capture loop.

The ABI passes a borrowed descriptor, checked plane layout, linear modifier, color metadata, session generation, sequence, timestamp, and opaque frame token. The token owns an `Arc` lease that pins the MMAP region and exported `OwnedFd`. Only the capture worker issues `QBUF`. A frame's final lease queues its slot for return to that worker. A stopped session discards returns while its frame storage remains valid through outstanding leases.

Rust imports raw source bytes as R8 EGLImages with pixel preservation enabled: two images for NV12/P010 and one for packed YUV/RGB. A GLES 3 shader unpacks the selected format, including the significant ten bits in each little-endian P010 word. It applies limited/full normalization and the negotiated Rec.601 or Rec.709 YUV matrix explicitly. RGB has no YUV matrix and defaults to full range when the device leaves quantization unspecified. This avoids relying on driver YUV range hints or 16-bit image import support. The shader writes a renderer-local full-size RGBA8 texture for the MyGo compositor. Each renderer caches imports by session generation and slot index. It restores the caller's GL state after preparation and destroys its resources only with the matching context current. Context loss uses an abandonment path without issuing GL deletions on a different context. [DMA-BUF formats](dmabuf-formats.md) specifies source bounds and sampling limits.

Actual-device tests exposed stale 64-byte regions when NVIDIA sampled CPU-written `videobuf2_vmalloc` exports. This exporter's DMA-BUF operations do not provide CPU-access synchronization callbacks in the tested kernel. The NVIDIA/vmalloc path therefore writes back the leased source mapping's CPU cache lines once per frame before GPU access. On x86-64 it selects checked CLFLUSHOPT plus SFENCE or CLFLUSH plus MFENCE support; unsupported platforms return a presentation error and use CPU fallback. A shared once-initialized result prevents duplicate work across windows. This is a platform-specific coherency workaround, not a claim that all DMA-BUF exporters require userspace cache maintenance. Source storage remains leased throughout synchronization.

After conversion, an exported native GPU fence owns an additional frame lease. An independent reaper releases that lease only after completion. Both windows can submit reads of the same source frame; every read has its own fence lease. Capture stops without waiting on UI leases, and the reaper outlives the session until pending reads finish. Submission failures complete outstanding GPU work before releasing their frame lease. The normal presentation path does not use `glFinish` or read back GPU pixels. See the [native fence contract](https://registry.khronos.org/EGL/extensions/ANDROID/EGL_ANDROID_native_fence_sync.txt) and [V4L2 export API](https://docs.kernel.org/userspace-api/media/v4l/vidioc-expbuf.html).

The MyGo extension is maintained as a small patch under `patches/mygo/`. Normal builds prepare the pinned official module in ignored build output; Debian builds use a sparse Go source overlay. No copied MyGo tree or permanent root module replacement is tracked. The extension adds context-bound external images and bypasses CPU bitmap mip generation for them. Native UI composition, fitting, clipping, and the separate sharing window continue through MyGo.

### Language boundaries

Before GTK initializes graphics, the Go launcher applies [version-limited NVIDIA compatibility settings](egl-leaks.md). For tested driver `615.78.08`, it selects Mesa Zink/Vulkan allocation when available, limits the NVIDIA frame queue to one, and disables NVIDIA's own D-Bus integration to avoid two vendor allocation leaks. It retains the legacy Wayland fallback when the preferred allocator components are unavailable. Other driver versions and explicit environment overrides retain their selection. Diagnostics mirror the policy and share its fixtures. The renderer does not terminate GTK's borrowed EGL display or manipulate foreign allocations.

The Go bridge calls Swift through session ABI version 2 in `src/abi/mygoframe.h`; Swift calls Rust through `src/abi/rust_core.h`. Swift serializes control inside its registry and session locks. Monotonic integer engine handles reject stale calls without dereferencing retired Swift objects. Noncopyable Swift handle owners destroy Rust handles in `deinit`. Rust owns device descriptors and MMAP regions through RAII and keeps frame storage alive with reference-counted leases. Cgo passes integer engine handles and opaque frame tokens; it never lends Go-owned pixels to Swift or Rust. Every acquired frame token and returned string has one explicit release operation. Engine and frame value copies share their lock and release state, so a copied wrapper cannot destroy the same native resource twice. Both native boundaries validate frame dimensions and byte length before Go accesses pixels.

Go callers use `Frame.CopyImage()` to obtain independently owned pixels. The presenter uses `Frame.UnsafeBorrowedImage()` for zero-copy display and must stop every access to the image and its pixel slice before `Frame.Release()`. Go cannot revoke a pixel slice retained separately from that unsafe borrow. The application replaces MyGo's retained pixel reference before releasing a displayed frame.

The production source contains no `.c` files and adds no Rust crate dependencies. Cargo runs the installed `bindgen` CLI at build time to generate Rust bindings from Linux, FFmpeg, and PipeWire headers. The C headers declare the two language boundaries. Small inline adapters in `src/abi/video.h` are used only by the retained Swift regression tests. FFmpeg and PipeWire remain external C libraries. Cgo also generates bridge code during the Go build.

## Audio and runtime ownership

Audio requests 128 frames at 48 kHz, about 2.67 ms per requested graph quantum. The capture and playback streams exchange float32 stereo samples through a bounded Rust ring. Playback drops stale backlog and inserts silence on underrun. Gain is atomic and applies to this application's monitor only. Rust realtime callbacks use preallocated storage and atomic operations; they do not call Go or Swift. PipeWire and the devices determine the actual quantum and buffering; the request is not a measured end-to-end latency guarantee. See [PipeWire stream properties](https://docs.pipewire.org/page_man_pipewire-props_7.html).

The PipeWire runtime retains an optional loader reference to the existing `libdbus-1.so.3` with `RTLD_NODELETE`. PipeWire can unload its realtime module when a context closes, while D-Bus retains process-global caches. Keeping the library resident preserves those cache roots across repeated contexts. Stream, context, thread, descriptor, and PipeWire initialization ownership is released when the last session or enumeration ends. This uses the existing library without adding a linked dependency, disabling realtime scheduling, or suppressing sanitizer checks. [SDL uses the same library-lifetime policy](https://github.com/libsdl-org/SDL/blob/release-3.2.4/src/core/linux/SDL_dbus.c#L176-L186).

## Native windows and branding

After the application event loop returns and application cleanup finishes, `ui.ShutdownText()` releases MyGo's owned Pango context, map, native fonts, and text caches. A terminal engine state prevents delayed preload work from reopening the native engine. Individual native font references have an exactly-once registry so late Go cleanup callbacks cannot double-unref released fonts. After native clients and the display stop, `App.ShutdownNative()` also removes Fontconfig's current-configuration reference through `FcConfigSetCurrent(NULL)`. Explicit configuration/font owners and the cache subsystem keep their independent lifetimes. See [Native text ownership](font-memory.md) for the lifetime contract and unsuppressed detector controls.

`mygo.App.ShutdownNative()` then clears the current graphics context and closes the framework-owned GDK display. It rejects active native owners. Corrected private GDK releases its EGL initialization and Wayland resources before disconnect; Rust never terminates the borrowed display. See [native display and input-method ownership](native-memory.md) and the [private runtime build](native-runtime-build.md).

Both windows share the same latest-frame bitmap and frame lease. The extra window requires another GPU presentation but does not start a second V4L2 stream or add a frame queue.

The supplied artwork in `src/internal/desktop/presentation/assets/logo.png` remains unchanged. The toolbar logo and icons use rounded corners with a radius of 20% of their width. Native window icons are rendered at 256 × 256 to fit GTK's X11 icon limits.

The native styling follows Fluent 2's [neutral and brand colors](https://fluent2.microsoft.design/color), [type hierarchy](https://fluent2.microsoft.design/typography), and [spacing and responsive layout guidance](https://fluent2.microsoft.design/layout). System fonts and native keyboard navigation remain in use.

## Application policy and control verification

[Language boundaries](language-boundaries.md) defines mode selection, device refresh, audio gain, generation-checked fallback, typed snapshots, and notification ownership. Production Swift session tests exercise these policies and concurrent teardown independently of the retained Swift media regression implementation.
