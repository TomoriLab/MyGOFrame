# Architecture and memory ownership

Go implements the MyGo native UI. Swift owns video and audio sessions and exposes the control API to Go. Rust owns V4L2 capture, FFmpeg conversion, frame storage, and PipeWire monitoring. The UI uses native Go views without HTML, JavaScript, or a WebView.

Paths in this document are relative to the repository root. Build and runtime requirements are in the [user guide](../README.md#build-and-run). Validation commands are in [Verification](verification.md).

## Video and frame ownership

V4L2 requests three MMAP buffers. Each read drains available completed buffers and converts only the newest one. The drain is bounded by the allocated driver-buffer count so a high-frame-rate device cannot indefinitely monopolize the capture thread. The driver may allocate more buffers than requested. See the [V4L2 streaming documentation](https://docs.kernel.org/userspace-api/media/v4l/mmap.html).

Malformed startup JPEG packets and truncated raw frames are discarded so switching formats can recover. Persistent decode corruption stops capture with an error after 120 consecutive invalid frames. The skipped-frame counter records observed software drops; it cannot account for frames discarded inside the capture hardware.

The Rust media core keeps three reusable RGBA buffers. Each buffer is limited to 128 MiB, so one active pool uses at most 384 MiB for RGBA storage. Requested and negotiated dimensions are checked before pool allocation; 3840 × 2160 and 7680 × 4320 fit this limit. Other allocations, including V4L2 buffers, decoding, MyGo scenes, and pools retained by outstanding frame leases, are outside this figure.

A displayed frame holds a lease on its buffer. The producer cannot overwrite that buffer until Go releases the lease. The latest frame replaces older unpublished frames; the application does not queue frames for timestamp-based playback. Frame leases remain valid after capture stops or the engine is destroyed. The UI coalesces redraw requests and updates one MyGo bitmap. It reuses a Go-owned blank buffer when detaching a scene from native pixels before releasing its frame. CPU color conversion and a GPU texture upload are still required; this is not a DMA-BUF rendering path.

The Go bridge calls Swift through the C ABI in `core/include/mygoframe.h`; Swift calls Rust through `core/include/rust_core.h`. Swift session owners destroy Rust handles in `deinit`. Rust owns device descriptors and MMAP regions through RAII and keeps frame storage alive with reference-counted leases. Cgo passes opaque engine and frame tokens; it never lends Go-owned pixels to Swift or Rust. Every acquired frame token and returned string has one explicit release operation. Engine and frame value copies share their lock and release state, so a copied wrapper cannot destroy the same native resource twice. Both native boundaries validate frame dimensions and byte length before Go accesses pixels.

Go callers use `Frame.CopyImage()` to obtain independently owned pixels. The presenter uses `Frame.UnsafeBorrowedImage()` for zero-copy display and must stop every access to the image and its pixel slice before `Frame.Release()`. Go cannot revoke a pixel slice retained separately from that unsafe borrow. The application replaces MyGo's retained pixel reference before releasing a displayed frame.

The production source contains no `.c` files and adds no Rust crate dependencies. Cargo runs the installed `bindgen` CLI at build time to generate Rust bindings from Linux, FFmpeg, and PipeWire headers. The C headers declare the two language boundaries. Small inline adapters in `core/include/video.h` are used only by the retained Swift regression tests. FFmpeg and PipeWire remain external C libraries. Cgo also generates bridge code during the Go build.

## Audio and runtime ownership

Audio requests 128 frames at 48 kHz, about 2.67 ms per requested graph quantum. The capture and playback streams exchange float32 stereo samples through a bounded Rust ring. Playback drops stale backlog and inserts silence on underrun. Gain is atomic and applies to this application's monitor only. Rust realtime callbacks use preallocated storage and atomic operations; they do not call Go or Swift. PipeWire and the devices determine the actual quantum and buffering; the request is not a measured end-to-end latency guarantee. See [PipeWire stream properties](https://docs.pipewire.org/page_man_pipewire-props_7.html).

The PipeWire runtime retains an optional loader reference to the existing `libdbus-1.so.3` with `RTLD_NODELETE`. PipeWire can unload its realtime module when a context closes, while D-Bus retains process-global caches. Keeping the library resident preserves those cache roots across repeated contexts. Stream, context, thread, descriptor, and PipeWire initialization ownership is released when the last session or enumeration ends. This uses the existing library without adding a linked dependency, disabling realtime scheduling, or suppressing sanitizer checks. [SDL uses the same library-lifetime policy](https://github.com/libsdl-org/SDL/blob/release-3.2.4/src/core/linux/SDL_dbus.c#L176-L186).

## Native windows and branding

Both windows share the same latest-frame bitmap and frame lease. The extra window requires another GPU presentation but does not start a second V4L2 stream or add a frame queue.

The supplied artwork in `cmd/mygoframe/assets/logo.png` remains unchanged. The toolbar logo and icons use rounded corners with a radius of 20% of their width. Native window icons are rendered at 256 × 256 to fit GTK's X11 icon limits.

The native styling follows Fluent 2's [neutral and brand colors](https://fluent2.microsoft.design/color), [type hierarchy](https://fluent2.microsoft.design/typography), and [spacing and responsive layout guidance](https://fluent2.microsoft.design/layout). System fonts and native keyboard navigation remain in use.
