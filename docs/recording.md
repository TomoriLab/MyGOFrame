# Recording implementation

Recording uses the original V4L2 capture data and selected PipeWire input. Go owns MyGo controls, Swift validates configuration and session transitions, and Rust owns recording buffers, codec contexts, packets, audio conversion, timestamps, and file output.

## Supported paths

| Area | Implementation |
| --- | --- |
| Output | MKV or MP4, no overwrite, exclusive `.part` file followed by no-replace publication |
| Video | AV1, HEVC/H.265, or H.264 with VBR/CBR target bitrate from 0.1 to 1000 Mbps |
| NVIDIA | NVENC on the selected CUDA UUID/PCI adapter; conditional low-delay NVDEC for baseline 8-bit 4:2:0 MJPEG |
| Intel | QSV/oneVPL through the selected DRM render node |
| AMD | VA-API on the selected VCE/VCN-capable device and driver |
| Capture conversion | NV12, P010, YUYV, UYVY, BGR24, RGB24, and MJPEG; unsupported MJPEG decoding falls back to software |
| Audio | Pre-monitor float32 stereo input converted to AAC, 48 kHz, 256 kbit/s |
| Monitoring | Independent enable, output selection, volume, and mute; monitor-off deactivates playback while input continues |
| Presets | Version 2 recording/monitor fields; version 1 remains readable; applying settings never starts recording |

The GPU catalog opens each encoder for a baseline configuration. Actual selected dimensions, rate control, and bit depth are validated again during asynchronous startup. Explicit GPU selection never falls back to another adapter. Auto tries available adapters. Unsupported hardware/codec combinations return an error. AMF and direct encoder DMA-BUF import are not implemented.

## Buffer and file ownership

The DMA capture worker publishes and notifies the latest preview frame before copying it into recording-owned storage. A local frame lease prevents source-slot reuse until that copy finishes, including when both preview windows release the frame first. Superseded dequeued frames are copied before requeue, so latest-frame preview discard does not discard recording input. The CPU fallback still copies raw data before conversion and requeue. Three preallocated recording video slots have a combined maximum capacity of 384 MiB. Slot leases prevent reuse until the recorder finishes reading. Recording retains no V4L2 or GPU preview lease through encoding. The initial path converts in recorder-owned CPU staging and uploads to the selected hardware encoder. Existing preview GPU fences remain responsible for preview readers.

The recording audio queue contains 64 fixed slots of up to 2048 stereo frames. Separate producer and consumer gates enforce exclusive access. Realtime input only copies samples and signals an eventfd; codec calls and file IO run on the recording worker. Audio overflow, sustained video overload, and missing input fail the recording explicitly. Queues do not grow to absorb a stalled disk or encoder.

Video and audio use monotonic capture timestamps. The audio resampler corrects bounded clock drift and rejects discontinuities over 100 ms. Stop drains queued samples, resampler delay, codec packets, and the container trailer. Closing the session joins recording before releasing input owners. Storage failures retain `.part` for diagnosis or recovery. Input failures attempt to finalize a valid partial recording and remain failed in status. Stopping with no submitted video cancels the file instead of publishing an empty success.

## Native runtime compatibility

NVIDIA 615.78.08 leaves legacy glibc condition-variable allocations when its codec library unloads. On this exact version, the Rust recording runtime loads the installed `libnvidia-encode.so.1` with `RTLD_NODELETE` and balances its loader reference. This preserves the library's process-global owners while encoder contexts, frames, worker threads, and descriptors still close normally. Other driver versions use normal loading. No leak suppression or new vendor binary is included.

The negative control reported 96 bytes per encoder lifecycle under ASan and Valgrind. The process-lifetime mapping removed the ASan report across all 21 tested format/codec combinations and the full two-window GUI recording lifecycle. Valgrind also removed the 96-byte definite-loss signature under a loader-lifetime control; it still reported unsupported NVIDIA ioctl and TLS classifications, so its overall run is not reported clean.

CUDA under ASan requires the [NVIDIA-documented](https://docs.nvidia.com/deeplearning/tensorrt/latest/reference/troubleshooting-analysis-tools.html) `protect_shadow_gap=0` option. This is an address-space compatibility setting. Leak detection remains enabled with no suppression file.

## Verification and boundaries

Local validation used FFmpeg 9.0.2, NVIDIA RTX 5080, driver 615.78.08, and the AVerMedia Live Gamer Ultra 2.1. Real 3840 × 2160 at 60 fps produced H.264, HEVC, and AV1 files, including AAC input. The native GUI exercised monitor-off, monitor-on, mute, output retargeting, stop with continuing preview, restart, and finalization on video stop with the separate capture window present. Short GUI recordings reported zero recording-queue drops. The source was silent during these checks, so audible capture and physical source A/V offset remain unverified.

Synthetic tests exercise all seven capture formats with all three NVENC codecs, P010 ten-bit HEVC/AV1, conditional NVDEC MJPEG, software MJPEG fallback, queue overflow and lease lifetime, disk-full callback propagation, no-overwrite publication, configuration validation, and two accelerated one-hour audio timelines with ±100 ppm drift. These timeline tests do not constitute a real-time one-hour capture test.

Intel/AMD hardware, cross-adapter transfers, HDR, GPU removal, physical end-to-end latency, and real-time one-hour stability require additional hardware or extended validation.

### Recording preview performance

The original DMA path copied each 12,441,600-byte 4K NV12 frame before notifying the UI. A real-device timing probe measured a 1.78 ms median and 2.97 ms p95 copy duration. Publishing before this bounded copy removes that dependency from preview wakeups. GPU visibility preparation, completion fences, capture-slot return, and encoder-owned staging remain in place. No additional worker, preview queue, or production dependency is required.

On 2026-11-10, three before/after recording/no-recording comparisons used NV12 3840 × 2160 at 60 fps, NVENC HEVC VBR at 40 Mbit/s, and both native windows. Each run lasted 20 seconds; recording started at three seconds. Window sizes remained 1280 × 800 and 960 × 540 logical pixels. The table reports medians across recording runs; GPU-ready collection excludes its initial two-second warmup.

| Measurement | Before | After |
| --- | ---: | ---: |
| Main texture-ready updates/s | 57.066 | 59.889 |
| Capture-window texture-ready updates/s | 59.817 | 59.999 |
| Main capture-to-texture-ready p95 | 26.191 ms | 23.775 ms |
| Capture-window capture-to-texture-ready p95 | 28.492 ms | 26.089 ms |
| Main submission intervals over 25 ms, total across three steady 13-second windows | 87 | 4 |
| Recording video-queue drops | 0 | 0 |

The steady submission interval uses capture timestamps from seconds five through eighteen. Main submission rates ranged from 54.922 to 59.380 updates/s before and 59.839 to 59.942 after. Capture-window rates after the change ranged from 59.997 to 60.019. The post-change no-recording medians were 59.994 and 59.996 updates/s. A pre-change no-recording capture-window run also fell to 54.198 updates/s, so individual baseline runs were variable. These measurements describe submitted or GPU-ready textures, not physical scanout or controller-to-display latency. They do not establish lower encoding cost or long-run stability.

The updated source passed Rust ASan and DMA/media TSan checks. A separate native two-window ASan/LSan run recorded HEVC and AV1 with AAC, recreated the capture window, resized and entered fullscreen, retargeted muted monitoring, stopped and restarted recording, and finalized on capture stop. All thirteen checkpoints passed without a sanitizer or GLib report. Separate device checks verified all three codecs, sampled color conversion, and forced CPU fallback.

Run the normal suites with `make test`, `make test-race`, `make test-sanitize`, and `make test-thread-sanitize`. Hardware conversion tests additionally require a 1280 × 720 JPEG fixture and an empty output directory:

```sh
RECORDING_TEST_OUTPUT=/absolute/empty-output-directory \
RECORDING_TEST_JPEG=/absolute/1280x720-baseline-jpeg \
CARGO_TARGET_DIR=build/rust-target \
cargo test --manifest-path src/rust/Cargo.toml \
  recording::infrastructure::tests::hardware_raw_and_mjpeg_recording -- \
  --ignored --exact --nocapture --test-threads=1
```

Use a 4:2:0 JPEG to exercise NVDEC and 4:2:2 to exercise software fallback. Set `RECORDING_TEST_GPU` to a catalog ID for an explicit device. Test-only Rust standard-library dependencies used for TSan do not change production Cargo dependencies. Debian build dependencies include libavformat-dev and libswresample-dev; runtime dependencies are derived from linked libraries.
