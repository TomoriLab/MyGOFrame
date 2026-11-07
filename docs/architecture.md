# Architecture and memory ownership

Go implements the MyGo native UI. Swift owns video and audio sessions and exposes the control API to Go. Rust owns V4L2 capture, FFmpeg conversion, frame storage, and PipeWire monitoring. The UI uses native Go views without HTML, JavaScript, or a WebView.

Paths in this document are relative to the repository root. Build and runtime requirements are in the [user guide](../README.md#build-and-run). Validation commands are in [Verification](verification.md).

## Video and frame ownership

Swift selects the video backend and owns session restart control. `auto` tries the Rust DMA-BUF backend for progressive NV12 SDR and retains the CPU backend for unsupported formats or failures. Go invokes GPU preparation only from the native MyGo renderer's current context. Presentation failures retire the external image and request a CPU restart through Swift.

### CPU backend

V4L2 requests three MMAP buffers. Each read drains available completed buffers and converts only the newest one. The drain is bounded by the allocated driver-buffer count so a high-frame-rate device cannot indefinitely monopolize the capture thread. The driver may allocate more buffers than requested. See the [V4L2 streaming documentation](https://docs.kernel.org/userspace-api/media/v4l/mmap.html).

Malformed startup JPEG packets and truncated raw frames are discarded so switching formats can recover. Persistent decode corruption stops capture with an error after 120 consecutive invalid frames. The skipped-frame counter records observed software drops; it cannot account for frames discarded inside the capture hardware.

The Rust media core keeps three reusable RGBA buffers. Each buffer is limited to 128 MiB, so one active pool uses at most 384 MiB for RGBA storage. Requested and negotiated dimensions are checked before pool allocation; 3840 × 2160 and 7680 × 4320 fit this limit. Other allocations, including V4L2 buffers, decoding, MyGo scenes, and pools retained by outstanding frame leases, are outside this figure.

A displayed frame holds a lease on its buffer. The producer cannot overwrite that buffer until Go releases the lease. The latest frame replaces older unpublished frames; the application does not queue frames for timestamp-based playback. Frame leases remain valid after capture stops or the engine is destroyed. The UI coalesces redraw requests and updates one MyGo bitmap. It reuses a Go-owned blank buffer when detaching a scene from native pixels before releasing its frame. This backend requires CPU color conversion and a GPU texture upload.

### DMA-BUF backend

The DMA backend requests six V4L2 MMAP slots and exports their allocations with `VIDIOC_EXPBUF`. The driver may return a different count within the checked range. It requires orphaned-buffer support so outstanding GPU reads can keep exported storage valid after `STREAMOFF`. Each source allocation is limited to 128 MiB and the complete capture pool to 512 MiB. The backend does not allocate the CPU RGBA pool or run the CPU converter in its capture loop.

The ABI passes a borrowed descriptor, checked plane layout, linear modifier, color metadata, session generation, sequence, timestamp, and opaque frame token. The token owns an `Arc` lease that pins the MMAP region and exported `OwnedFd`. Only the capture worker issues `QBUF`. A frame's final lease queues its slot for return to that worker. A stopped session discards returns while its frame storage remains valid through outstanding leases.

Rust imports the Y plane and raw interleaved UV bytes as separate R8 EGLImages with pixel preservation enabled. A GLES 3 shader applies limited/full normalization and the negotiated Rec.601 or Rec.709 matrix explicitly. This avoids relying on driver YUV range hints. The shader writes a renderer-local full-size RGBA texture for the MyGo compositor. Each renderer caches imports by session generation and slot index. It restores the caller's GL state after preparation and destroys its resources only with the matching context current. Context loss uses an abandonment path without issuing GL deletions on a different context.

Actual-device tests exposed stale 64-byte regions when NVIDIA sampled CPU-written `videobuf2_vmalloc` exports. This exporter's DMA-BUF operations do not provide CPU-access synchronization callbacks in the tested kernel. The NVIDIA/vmalloc path therefore writes back the leased source mapping's CPU cache lines once per frame before GPU access. On x86-64 it selects checked CLFLUSHOPT plus SFENCE or CLFLUSH plus MFENCE support; unsupported platforms return a presentation error and use CPU fallback. A shared once-initialized result prevents duplicate work across windows. This is a platform-specific coherency workaround, not a claim that all DMA-BUF exporters require userspace cache maintenance. Source storage remains leased throughout synchronization.

After conversion, an exported native GPU fence owns an additional frame lease. An independent reaper releases that lease only after completion. Both windows can submit reads of the same source frame; every read has its own fence lease. Capture stops without waiting on UI leases, and the reaper outlives the session until pending reads finish. Submission failures complete outstanding GPU work before releasing their frame lease. The normal presentation path does not use `glFinish` or read back GPU pixels. See the [native fence contract](https://registry.khronos.org/EGL/extensions/ANDROID/EGL_ANDROID_native_fence_sync.txt) and [V4L2 export API](https://docs.kernel.org/userspace-api/media/v4l/vidioc-expbuf.html).

The MyGo extension is maintained as a small patch under `patches/mygo/`. Normal builds prepare the pinned official module in ignored build output; Debian builds use a sparse Go source overlay. No copied MyGo tree or permanent root module replacement is tracked. The extension adds context-bound external images and bypasses CPU bitmap mip generation for them. Native UI composition, fitting, clipping, and the separate sharing window continue through MyGo.

### Language boundaries

Before GTK initializes graphics, the Go launcher applies [version-limited NVIDIA compatibility settings](egl-leaks.md). For tested driver `615.78.08`, it selects Mesa Zink/Vulkan allocation when available, limits the NVIDIA frame queue to one, and disables NVIDIA's own D-Bus integration to avoid two vendor allocation leaks. It retains the legacy Wayland fallback when the preferred allocator components are unavailable. Other driver versions and explicit environment overrides retain their selection. Diagnostics mirror the policy and share its fixtures. The renderer does not terminate GTK's borrowed EGL display or manipulate foreign allocations.

The Go bridge calls Swift through the C ABI in `core/include/mygoframe.h`; Swift calls Rust through `core/include/rust_core.h`. Swift session owners destroy Rust handles in `deinit`. Rust owns device descriptors and MMAP regions through RAII and keeps frame storage alive with reference-counted leases. Cgo passes opaque engine and frame tokens; it never lends Go-owned pixels to Swift or Rust. Every acquired frame token and returned string has one explicit release operation. Engine and frame value copies share their lock and release state, so a copied wrapper cannot destroy the same native resource twice. Both native boundaries validate frame dimensions and byte length before Go accesses pixels.

Go callers use `Frame.CopyImage()` to obtain independently owned pixels. The presenter uses `Frame.UnsafeBorrowedImage()` for zero-copy display and must stop every access to the image and its pixel slice before `Frame.Release()`. Go cannot revoke a pixel slice retained separately from that unsafe borrow. The application replaces MyGo's retained pixel reference before releasing a displayed frame.

The production source contains no `.c` files and adds no Rust crate dependencies. Cargo runs the installed `bindgen` CLI at build time to generate Rust bindings from Linux, FFmpeg, and PipeWire headers. The C headers declare the two language boundaries. Small inline adapters in `core/include/video.h` are used only by the retained Swift regression tests. FFmpeg and PipeWire remain external C libraries. Cgo also generates bridge code during the Go build.

## Audio and runtime ownership

Audio requests 128 frames at 48 kHz, about 2.67 ms per requested graph quantum. The capture and playback streams exchange float32 stereo samples through a bounded Rust ring. Playback drops stale backlog and inserts silence on underrun. Gain is atomic and applies to this application's monitor only. Rust realtime callbacks use preallocated storage and atomic operations; they do not call Go or Swift. PipeWire and the devices determine the actual quantum and buffering; the request is not a measured end-to-end latency guarantee. See [PipeWire stream properties](https://docs.pipewire.org/page_man_pipewire-props_7.html).

The PipeWire runtime retains an optional loader reference to the existing `libdbus-1.so.3` with `RTLD_NODELETE`. PipeWire can unload its realtime module when a context closes, while D-Bus retains process-global caches. Keeping the library resident preserves those cache roots across repeated contexts. Stream, context, thread, descriptor, and PipeWire initialization ownership is released when the last session or enumeration ends. This uses the existing library without adding a linked dependency, disabling realtime scheduling, or suppressing sanitizer checks. [SDL uses the same library-lifetime policy](https://github.com/libsdl-org/SDL/blob/release-3.2.4/src/core/linux/SDL_dbus.c#L176-L186).

## Native windows and branding

After the application event loop returns and application cleanup finishes, `ui.ShutdownText()` releases MyGo's owned Pango context, map, native fonts, and text caches. A terminal engine state prevents delayed preload work from reopening the native engine. Individual native font references have an exactly-once registry so late Go cleanup callbacks cannot double-unref released fonts. Shared GTK/Fontconfig globals retain their library ownership. See [Native text ownership](font-memory.md) for the lifetime contract and unsuppressed detector controls.

`mygo.App.ShutdownNative()` then clears the current graphics context and closes the framework-owned GDK display. It rejects active native owners. Corrected private GDK releases its EGL initialization and Wayland resources before disconnect; Rust never terminates the borrowed display. See [native display and input-method ownership](native-memory.md) and the [private runtime build](native-runtime-build.md).

Both windows share the same latest-frame bitmap and frame lease. The extra window requires another GPU presentation but does not start a second V4L2 stream or add a frame queue.

The supplied artwork in `cmd/mygoframe/assets/logo.png` remains unchanged. The toolbar logo and icons use rounded corners with a radius of 20% of their width. Native window icons are rendered at 256 × 256 to fit GTK's X11 icon limits.

The native styling follows Fluent 2's [neutral and brand colors](https://fluent2.microsoft.design/color), [type hierarchy](https://fluent2.microsoft.design/typography), and [spacing and responsive layout guidance](https://fluent2.microsoft.design/layout). System fonts and native keyboard navigation remain in use.
