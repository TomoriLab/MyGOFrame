# MyGOFrame

MyGOFrame is a Linux capture-card viewer for playing console games through a capture preview. Its [MyGo native UI](https://mygo.egoist.dev/docs/ui) follows Fluent 2-inspired styling and the system's light or dark appearance. Settings are grouped into Video, Audio, and Sharing tabs.

## Features

- Select a V4L2 device, pixel format, resolution, and exact rational frame interval.
- Select automatic, limited, or full input color range.
- Use an optional DMA-BUF GPU path for progressive NV12 SDR capture, with automatic CPU fallback.
- Monitor a selected PipeWire audio input through the default or a selected output.
- Adjust monitoring volume, mute, and stop audio independently of video.
- Fit the complete video inside the available viewport while preserving its aspect ratio.
- Open a separate video-only window for Discord or other window-sharing applications.
- Enter fullscreen with F11. Escape returns to the controls.

Supported video formats are YUYV, UYVY, NV12, P010, BGR24, RGB24, and MJPEG. The UI lists combinations reported by the device that the core can decode. Video capture uses the V4L2 single-planar streaming API. Multi-planar-only devices, interlaced capture, and other compressed codecs are not supported.

For devices exposing continuous or stepwise capabilities, the menus include supported common modes and valid range endpoints. They do not enumerate every possible resolution or frame interval.

## Debian package

A local Debian sid amd64 package can be built with `make deb`. Install the result with:

```sh
sudo apt install ./build/debian/mygoframe_0.1.0-1_amd64.deb
```

The package includes the Swift runtime and installs a desktop launcher. Build requirements, source rebuilding, and verification are documented in [Debian packaging](docs/debian.md).

## Build and run

Requirements:

| Component | Requirement |
| --- | --- |
| Platform | Linux with V4L2 and a graphical GTK 3 session |
| Swift | Swift 6 toolchain; validated with 6.3.3 |
| Go | 1.27.3 or newer, required by MyGo 0.8.1 |
| Rust | Rust toolchain with Cargo and the `bindgen` CLI; validated with Rust 1.99.1 and bindgen 0.72.1 |
| Build tools | GNU Make, Python 3, GNU patch, pkg-config, Linux headers, libclang, and a C compiler for cgo; LLD for sanitizer tests |
| Video libraries | FFmpeg libavcodec, libavutil, libswscale development packages |
| Audio libraries | PipeWire libpipewire-0.3 development package and a running session manager |
| Rendering | GTK 3 and OpenGL runtime libraries; EGL, GLES 3 external textures, linear R8 DMA-BUF import, and native fence export for the DMA-BUF path |

On Debian, the native development packages are `build-essential`, `pkg-config`, `libclang-dev`, `libavcodec-dev`, `libavutil-dev`, `libswscale-dev`, and `libpipewire-0.3-dev`. Install `lld` to run the sanitizer targets. Install the [Swift Linux toolchain](https://www.swift.org/install/linux/) and [Rust toolchain](https://www.rust-lang.org/tools/install) separately. Install the `bindgen` CLI with `cargo install bindgen-cli`. The `swift` executable from Python's OpenStack Swift client is unrelated to the Swift compiler.

```sh
make check-dependencies
make -j
./build/mygoframe
```

`make` discovers `swiftc` on `PATH` or in the standard Swiftly installation. Override it when needed:

```sh
make SWIFTC=/path/to/swift/usr/bin/swiftc
```

The build applies a small [MyGo renderer patch](docs/mygo-renderer.md) to generated files under `build/`. The official Go module and root `go.mod` remain unchanged. A complete MyGo source tree is not vendored in this repository.

On tested NVIDIA driver 615.78.08, startup applies [process-scoped compatibility settings](docs/egl-leaks.md) to avoid known EGL/GBM and NVIDIA D-Bus allocation leaks. It prefers the installed Mesa Zink/Vulkan allocator with the NVIDIA GLES renderer and retains a legacy Wayland fallback. Explicit environment overrides are preserved. Other driver versions retain their normal configuration.

Keep `build/mygoframe`, `build/libmygoframe_core.so`, and `build/libmygoframe_media.so` together. The application also requires the Swift runtime installed with its build toolchain. This build is intended for local execution; it does not bundle the Swift, FFmpeg, or PipeWire runtimes for distribution.

Register the launcher and Wayland taskbar icon for the current user with `make install-desktop`. This renders a 512 × 512 PNG with transparent rounded corners under `$XDG_DATA_HOME/mygoframe` and creates `$XDG_DATA_HOME/applications/mygoframe.desktop`, using `~/.local/share` when `XDG_DATA_HOME` is unset. Run the command again after moving the project because the launcher uses the absolute executable path.

Select the capture device and video settings, then start the preview. Select the capture card's audio source to enable monitoring. Audio starts disabled so an unrelated microphone is never selected automatically. Refresh the device list after connecting or removing hardware. Device access follows the session's existing permissions.

The preview scales to the available space and adds black borders when the window and video have different aspect ratios. Capture resolution does not determine the minimum window size. Windows smaller than 800 logical pixels wide or 480 pixels high move the controls into a scrollable **Settings** dialog so the sidebar cannot consume the video viewport. The minimum window content size is 320 × 240. Use `--window-width` and `--window-height` to select the initial size.

## Capture window and screen sharing

Select **Capture window** in the toolbar or **Settings → Sharing → Open capture window**. In Discord's window picker, select **MyGOFrame Capture**. This separate native window contains only the video and black borders; device names, settings, and status messages remain in the controls window. It stays visible and receives frames when the controls window is minimized. Closing it leaves capture running, and opening it again reuses the same capture stream. Closing the controls window closes both windows and releases the devices.

The capture window can be resized independently, with a minimum content size of 320 × 180. F11 and Escape control fullscreen for the focused window.

To open it when the application starts:

```sh
./build/mygoframe --capture-window
```

Window sharing is handled by Discord and the desktop's capture backend. This option does not start a broadcast or select Discord's audio source. Audio monitoring still uses the selected PipeWire output.

## Latency and color

The default `auto` backend tries DMA-BUF for supported NV12 SDR modes. Rust exports V4L2 buffers and converts their Y and UV planes to RGBA on the GPU. Swift owns session selection and restart control. Both native MyGo windows retain independent GPU presentation resources while sharing one capture stream. GPU completion fences keep a capture slot leased until every submitted GPU read has finished.

Unsupported formats, export failures, incompatible graphics contexts, and GPU preparation failures restart capture through the CPU path. The status displays the active backend and fallback reason. Select the CPU path explicitly with `./build/mygoframe --video-backend cpu`. `--video-backend dma` requests DMA-BUF and still permits CPU fallback; inspect the active backend when validating it. The [benchmark guide](docs/benchmark-video.md) rejects fallback runs when comparing DMA-BUF with CPU rendering.

Video and audio run independently without an added synchronization delay. Capture-card buffering, USB transport, conversion cost, display refresh, the compositor, and audio hardware remain part of end-to-end latency. Use an uncompressed mode that fits the card's USB bandwidth and measure the complete path before relying on it for timing-sensitive play.

Color range controls how captured samples are interpreted. Automatic mode uses negotiated V4L2 metadata and decoded JPEG metadata. Conversion accounts for the reported YCbCr matrix. P010 is converted to an 8-bit RGBA preview; HDR tone mapping and HDR display output are not implemented. Supply an SDR console signal for predictable display color.

## Development documentation

Architecture, memory ownership, verification commands, and agent guidance are in [docs/](docs/README.md).

## License

MyGOFrame is licensed under the [MIT License](LICENSE). Third-party dependencies retain their respective licenses.

## AI-Generated Code Notice

Parts of this project were created with assistance from AI tools (e.g. large language models). All AI-assisted contributions were reviewed and adapted by maintainers before inclusion. If you need provenance for specific changes, please refer to the Git history and commit messages.
