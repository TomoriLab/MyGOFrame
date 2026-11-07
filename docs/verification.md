# Verification

Run these commands from the repository root after installing the [build requirements](../README.md#build-and-run). Consult the [ownership contracts](architecture.md) when changing native handles, frame leases, or stream lifetimes.

```sh
make test
make test-race
make test-sanitize
make test-thread-sanitize
make test-audio-integration
./build/mygoframe --list
./build/mygoframe --smoke-video /dev/video0 --format NV12 --width 640 --height 480 --fps 60 --seconds 2
./build/mygoframe --smoke-audio SOURCE_NODE_NAME --seconds 2 --volume 0
./build/mygoframe --smoke-ui-video /dev/video0 --format NV12 --width 1920 --height 1080 --fps 60 --smoke-ui-seconds 10
./build/mygoframe --smoke-ui-pattern --width 3840 --height 2160 --window-width 320 --window-height 240 --smoke-ui-seconds 15
./build/mygoframe --smoke-ui-pattern --capture-window --smoke-capture-window-cycles 2 --smoke-ui-seconds 6
```

The video smoke command temporarily configures and captures the selected device. The audio smoke command opens only the named source and defaults to silent monitoring. Replace `SOURCE_NODE_NAME` with an `id` returned by `--list`. The UI video smoke command starts the selected video mode with audio disabled and fails if a live frame was not displayed before the window closed. The pattern command displays a white border and four colored corners without opening a capture device; all four corners and the border should remain visible when resizing the window.

The Rust tests cover frame leases, bounded storage, pixel conversion at a guarded memory boundary, malformed JPEG input, and concurrent audio-buffer access. The retained Swift media sources in `tests/legacy/swift` guard the original short-buffer regression and compare the legacy conversion path during migration; production capture runs in Rust. Go tests cover bridge failure recovery, UI mode selection, theme contrast and transitions, and rendered preview pixels across window sizes and display scales. Capture-window tests check clean content, full-frame fitting, independent frame consumption, clearing, and fullscreen state. `--smoke-capture-window-cycles` closes and reopens the window during a timed UI check.

`make test-sanitize` runs the retained Swift media tests and Rust unit tests under AddressSanitizer. `make test-thread-sanitize` currently exercises the retained Swift media implementation. The Rust sanitizer targets use `RUSTC_BOOTSTRAP` for the compiler's sanitizer flag and do not affect production builds. `make test-audio-integration` requires a running PipeWire session and uses AddressSanitizer with leak detection enabled. Without `AUDIO_SOURCE`, it checks the retained Swift implementation's enumeration and missing-source startup without opening a microphone. Set `AUDIO_SOURCE` to an explicit node name to include repeated muted monitoring and Rust C ABI lifecycle tests:

```sh
AUDIO_SOURCE=SOURCE_NODE_NAME make test-audio-integration
```

The Rust integration test repeats enumeration, rejected source startup, muted monitoring, status reads, and resource release five times. It checks that descriptors and threads return to their warmed-up baseline. Hardware smoke checks require an available device and active session. Neither frame counters nor graph statistics measure input-to-display or input-to-speaker latency.
