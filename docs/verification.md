# Verification

Run these commands from the repository root after installing the [build requirements](../README.md#build-and-run). Consult the [ownership contracts](architecture.md) when changing native handles, frame leases, or stream lifetimes.

```sh
make test
make test-race
make test-sanitize
make test-thread-sanitize
make test-audio-integration
make test-egl-integration
make test-font-integration
./build/mygoframe --list
./build/mygoframe --smoke-video /dev/video0 --format NV12 --width 640 --height 480 --fps 60 --seconds 2
./build/mygoframe --smoke-audio SOURCE_NODE_NAME --seconds 2 --volume 0
./build/mygoframe --smoke-ui-video /dev/video0 --format NV12 --width 1920 --height 1080 --fps 60 --smoke-ui-seconds 10
./build/mygoframe --smoke-ui-pattern --width 3840 --height 2160 --window-width 320 --window-height 240 --smoke-ui-seconds 15
./build/mygoframe --smoke-ui-pattern --capture-window --smoke-capture-window-cycles 2 --smoke-ui-seconds 6
./build/mygoframe --smoke-ui-video /dev/video0 --format NV12 --width 3840 --height 2160 --fps 60 --video-backend dma --capture-window --smoke-capture-window-cycles 3 --smoke-ui-seconds 10 --measure-latency
python3 tools/validate-dma-colors.py --width 3840 --height 2160 --repeat-frames 24 --poison-pack-state --output build/dma-colors.json
python3 tools/validate-dma-colors.py --format P010 --width 3840 --height 2160 --fps 30 --repeat-frames 12 --poison-pack-state --output build/p010-colors.json
python3 tools/benchmark-video.py --device /dev/video0 --capture-window --output build/video-benchmark.json
python3 tools/benchmark-video.py --format YUYV --width 1920 --height 1080 --fps 60 --capture-window --output build/yuyv-benchmark.json
```

The video smoke command temporarily configures and captures the selected device. The audio smoke command opens only the named source and defaults to silent monitoring. Replace `SOURCE_NODE_NAME` with an `id` returned by `--list`. The UI video smoke command starts the selected video mode with audio disabled and fails if a live frame was not displayed before the window closed. The pattern command displays a white border and four colored corners without opening a capture device; all four corners and the border should remain visible when resizing the window.

The Rust tests cover frame leases, bounded storage, pixel conversion at a guarded memory boundary, malformed JPEG input, and concurrent audio-buffer access. The retained Swift media sources in `tests/legacy/swift` guard the original short-buffer regression and compare the legacy conversion path during migration; production capture runs in Rust. Production Swift tests cover mode reconciliation, device removal policy, mute gain, monotonic handles, command ordering, stale renderer rejection, and concurrent teardown. Rust notification tests cover coalescing and independent descriptor lifetime. Go tests cover bridge failure recovery, typed snapshot precision, notification cancellation, UI mode selection, theme contrast and transitions, and rendered preview pixels across window sizes and display scales. Capture-window tests check clean content, full-frame fitting, independent frame consumption, clearing, and fullscreen state. `--smoke-capture-window-cycles` closes and reopens the window during a timed UI check.

DMA-BUF tests also cover fence-held capture leases, exactly-once slot return, post-stop storage lifetime, bounded submission failure, and shared Go context ownership. Rust and Go reject truncated planes, invalid strides, subsampling violations, unknown formats, and excessive allocations. The hardware color tool accepts `--format NV12|P010|YUYV|UYVY|BGR3|RGB3` and overwrites only a held capture lease with 24 known patches before its first GPU read. It compares 8 × 8 interior samples per patch with the production FFmpeg converter and analytic source values for limited and full range. The default tolerance is three 8-bit RGB code values to account for coefficient and rounding differences. A separate `--alternating-luma` check compares pixel parity against analytic source values and retains FFmpeg differences separately; P010 patterns include all four values of the two bits that an eight-bit truncation would lose. Pack-state poisoning and trailing canaries check bounded readback and GL state restoration. It does not measure HDMI signal encoding, desktop color management, display calibration, or whole-frame color error. Close other viewers before using the device and inspect the JSON `passed` result. See [DMA-BUF formats](dmabuf-formats.md) for acceptance rules and the observed CPU-reference difference.

`MYGOFRAME_FORCE_GPU_FAILURE=1` rejects GPU initialization for fallback checks. `--video-backend dma` still permits CPU fallback, so a successful UI smoke exit alone does not prove DMA-BUF presentation. Require `videoBackend=dmabuf`, nonempty GPU preparation metrics, and frames rendered in each requested window. The benchmark tool checks the actual backend automatically and documents its measurement limits.

`make test-sanitize` runs production Swift session tests, retained Swift media tests, and Rust unit tests under AddressSanitizer. `make test-thread-sanitize` exercises production Swift session concurrency and the retained Swift media implementation. The Rust sanitizer targets use `RUSTC_BOOTSTRAP` for the compiler's sanitizer flag and do not affect production builds. `make test-audio-integration` requires a running PipeWire session and uses AddressSanitizer with leak detection enabled. Without `AUDIO_SOURCE`, it checks the retained Swift implementation's enumeration and missing-source startup without opening a microphone. Set `AUDIO_SOURCE` to an explicit node name to include repeated muted monitoring and Rust C ABI lifecycle tests:

```sh
AUDIO_SOURCE=SOURCE_NODE_NAME make test-audio-integration
```

The Rust integration test repeats enumeration, rejected source startup, muted monitoring, status reads, and resource release five times. It checks that descriptors and threads return to their warmed-up baseline. Hardware smoke checks require an available device and active session. Neither frame counters nor graph statistics measure input-to-display or input-to-speaker latency.

`make test-egl-integration` runs an optional native EGL lifecycle check under ASan/LSan without opening a capture device. It includes repeated main-thread and joined-worker ownership and applies the same NVIDIA startup policy as the application. See [NVIDIA EGL allocation leaks](egl-leaks.md) for negative controls, driver limits, and the separate full-GUI allocation results. The policy's Go and Python tests share 32 fixtures covering explicit overrides, malformed manifests, backend selection, and unsupported driver versions.

`make test-font-integration` runs isolated Rust Pango/Fontconfig ownership and detector controls with ASan/LSan enabled. It verifies normal owner release, deliberately lost owners, Fontconfig's relative-offset reachability signature, and terminal current-configuration release while independent configuration/font references remain live. Some controls intentionally return the configured leak exit code; the tool checks that expected result and retains the complete report. See [Native text ownership](font-memory.md) for interpretation. Required `make test` and `make test-race` also run focused MyGo shutdown/reference and transient GLib ownership regressions without opening native windows or a capture device.
