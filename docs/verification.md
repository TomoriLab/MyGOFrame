# Verification

Run these commands from the repository root after installing the [build requirements](../README.md#build-and-run). Consult the [ownership contracts](architecture.md) when changing native handles, frame leases, or stream lifetimes.

```sh
make test-architecture
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

`make test-architecture` rejects invalid dependency directions and compiles all Go domains with cgo disabled. Its isolated Swift policy tests use injected media and repository implementations without native headers or devices. They cover atomic preset mutations, failed persistence, stale fallback rejection, preset rollback, monitoring gain, recording guards, and resource retirement. Production session tests continue through the actual native adapters.

The optional session-port recording test captures NV12 at 3840 × 2160, 60 fps and records H.264, HEVC, and AV1 through Go → Swift → Rust. It uses no audio input and waits for asynchronous finalization before checking the result. It requires an available capture device, hardware support for all three codecs, and an empty exclusive output directory. Without the output variable, normal test runs skip device access.

```sh
LD_LIBRARY_PATH="$PWD/build" CGO_ENABLED=1 \
  MYGOFRAME_RECORDING_TEST_DEVICE=/dev/video0 \
  MYGOFRAME_RECORDING_TEST_OUTPUT="$PWD/build/recording-check" \
  python3 tools/go-mygoframe.py test ./src/internal/session/infrastructure/native \
  -run '^TestHardwareRecordingThroughSessionPorts$' -count=1 -v
```

The video smoke command temporarily configures and captures the selected device. The audio smoke command opens only the named source and defaults to silent monitoring. Replace `SOURCE_NODE_NAME` with an `id` returned by `--list`. The UI video smoke command starts the selected video mode with audio disabled and fails if a live frame was not displayed before the window closed. The pattern command displays a white border and four colored corners without opening a capture device; all four corners and the border should remain visible when resizing the window.

The Rust tests cover frame leases, bounded storage, pixel conversion at a guarded memory boundary, malformed JPEG input, and concurrent audio-buffer access. The retained Swift media sources in `tests/legacy/swift` guard the original short-buffer regression and compare the legacy conversion path during migration; production capture runs in Rust. Production Swift tests cover mode reconciliation, device removal policy, mute gain, monotonic handles, command ordering, stale renderer rejection, and concurrent teardown. Rust notification tests cover coalescing and independent descriptor lifetime. Go tests cover bridge failure recovery, typed snapshot precision, notification cancellation, UI mode selection, theme contrast and transitions, and rendered preview pixels across window sizes and display scales. Capture-window tests check clean content, full-frame fitting, independent frame consumption, clearing, and fullscreen state. `--smoke-capture-window-cycles` closes and reopens the window during a timed UI check.

DMA-BUF tests also cover fence-held capture leases, exactly-once slot return, post-stop storage lifetime, bounded submission failure, and shared Go context ownership. A guarded recording-copy regression releases the preview and signals a simulated GPU fence before copying under the capture worker's remaining frame lease, then checks that recorder-owned bytes and padding survive source retirement.

Recording performance checks must compare recording and idle runs with both windows visible, preserve window geometry, identify contexts by their reported roles, and separate steady submission intervals from texture-ready metrics that include recording startup. See [Recording preview performance](recording.md#recording-preview-performance) for the measured boundary.

Rust and Go reject truncated planes, invalid strides, subsampling violations, unknown formats, and excessive allocations. The hardware color tool accepts `--format NV12|P010|YUYV|UYVY|BGR3|RGB3` and overwrites only a held capture lease with 24 known patches before its first GPU read. It compares 8 × 8 interior samples per patch with the production FFmpeg converter and analytic source values for limited and full range. The default tolerance is three 8-bit RGB code values to account for coefficient and rounding differences. A separate `--alternating-luma` check compares pixel parity against analytic source values and retains FFmpeg differences separately; P010 patterns include all four values of the two bits that an eight-bit truncation would lose. Pack-state poisoning and trailing canaries check bounded readback and GL state restoration. It does not measure HDMI signal encoding, desktop color management, display calibration, or whole-frame color error. Close other viewers before using the device and inspect the JSON `passed` result. See [DMA-BUF formats](dmabuf-formats.md) for acceptance rules and the observed CPU-reference difference.

`MYGOFRAME_FORCE_GPU_FAILURE=1` rejects GPU initialization for fallback checks. `--video-backend dma` still permits CPU fallback, so a successful UI smoke exit alone does not prove DMA-BUF presentation. Require `videoBackend=dmabuf`, nonempty GPU preparation metrics, and frames rendered in each requested window. The benchmark tool checks the actual backend automatically and documents its measurement limits.

`make test-sanitize` runs production Swift session tests, retained Swift media tests, and Rust unit tests under AddressSanitizer. `make test-thread-sanitize` exercises production Swift session concurrency and the retained Swift media implementation. The Rust sanitizer targets use `RUSTC_BOOTSTRAP` for the compiler's sanitizer flag and do not affect production builds. `make test-audio-integration` requires a running PipeWire session and uses AddressSanitizer with leak detection enabled. Without `AUDIO_SOURCE`, it checks the retained Swift implementation's enumeration and missing-source startup without opening a microphone. Set `AUDIO_SOURCE` to an explicit node name to include repeated muted monitoring and Rust C ABI lifecycle tests:

```sh
AUDIO_SOURCE=SOURCE_NODE_NAME make test-audio-integration
```

The Rust integration test repeats enumeration, rejected source startup, muted monitoring, status reads, and resource release five times. It checks that descriptors and threads return to their warmed-up baseline. Hardware smoke checks require an available device and active session. Neither frame counters nor graph statistics measure input-to-display or input-to-speaker latency.

`make test-egl-integration` runs an optional native EGL lifecycle check under ASan/LSan without opening a capture device. It includes repeated main-thread and joined-worker ownership and applies the same NVIDIA startup policy as the application. See [NVIDIA EGL allocation leaks](egl-leaks.md) for negative controls, driver limits, and the separate full-GUI allocation results. The policy's Go and Python tests share 32 fixtures covering explicit overrides, malformed manifests, backend selection, and unsupported driver versions.

`make test-font-integration` runs isolated Rust Pango/Fontconfig ownership and detector controls with ASan/LSan enabled. It verifies normal owner release, deliberately lost owners, Fontconfig's relative-offset reachability signature, and terminal current-configuration release while independent configuration/font references remain live. Some controls intentionally return the configured leak exit code; the tool checks that expected result and retains the complete report. See [Native text ownership](font-memory.md) for interpretation. Required `make test` and `make test-race` also run focused MyGo shutdown/reference and transient GLib ownership regressions without opening native windows or a capture device.

## Rust recording test boundaries

Recording configuration tests are in `src/rust/src/recording/domain/configuration/tests.rs`; codec unit tests are in `src/rust/src/recording/infrastructure/codec/tests.rs`. The separate `src/rust/tests/recording.rs` crate uses generated bindings to the existing public C ABI, without importing private implementation modules. `make test` and `make test-sanitize` compile this crate and run its device-independent rejection check. Real capture/encoding is opt-in; [Recording](recording.md) documents the environment and exact Cargo command. Existing white-box GPU resource checks remain codec unit tests.

The split was validated on 2026-11-11 with the RTX 5080 and AVerMedia Live Gamer Ultra 2.1. The public-API test recorded and decoded H.264, HEVC, and AV1 from 4K60 NV12 through DMA capture and `vulkan-upload-cuda`, and from 1080p60 MJPEG through CPU capture. All six files matched negotiated dimensions, nominal frame rate, reported encoded-frame count, and zero recording queue drops. A 720p MJPEG request was rejected by the test when the device negotiated 1080p; reopening a supported mode then succeeded. The separate internal conversion test preserved all 21 format/codec combinations using a synthetic 720p JPEG fixture. Evidence is under `build/test-separation/`.

`make test` and `make test-sanitize` passed after the split, including 44 unit tests and the public-API rejection test; hardware tests remain explicitly ignored in those default runs. The first direct hardware ASan run completed recording but exited 23 with the previously identified NVIDIA D-Bus allocation (2,533 bytes across three allocations). The executable bypasses Go's graphics startup policy. A repeat on driver 615.78.08 uses the same process-scoped `__GL_DisableDBus=1` compatibility setting, not a sanitizer suppression.
 The compatible 4K60 NV12/Vulkan HEVC repeat recorded and decoded 129 frames with zero queue drops and exited 0 without ASan/LSan reports. Both raw and compatible reports are retained in `build/test-separation/hardware-asan*.log`.
