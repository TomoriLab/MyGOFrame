# Experimental CUDA and Vulkan recording transfers

The opt-in NV12 SDR recorder supports CUDA host upload, CUDA `DMABUF_FD` import, and Vulkan-to-CUDA transfer into NVENC surfaces. It uses the selected NVIDIA GPU. H.264, HEVC, and AV1 remain encoded by NVENC. This does not add a Vulkan Video encoder or change the Intel/AMD recording backends.

## Selection

Start the application with one of these process-local settings:

```sh
MYGOFRAME_EXPERIMENTAL_RECORDING_TRANSFER=auto ./build/mygoframe
MYGOFRAME_EXPERIMENTAL_RECORDING_TRANSFER=cuda ./build/mygoframe
MYGOFRAME_EXPERIMENTAL_RECORDING_TRANSFER=vulkan ./build/mygoframe
```

The default is `off`, which preserves the CPU staging recorder. Experimental modes require NV12 input and an NVENC device. Invalid values or incompatible configurations produce an explicit recording error. The earlier `MYGOFRAME_EXPERIMENTAL_CUDA_RECORDING=1` switch aliases `cuda` when the new setting is absent.

| Mode | Selection order |
| --- | --- |
| `off` | Existing CPU conversion/staging and hardware encoding |
| `auto` | CUDA DMA-BUF import, Vulkan DMA-BUF import and CUDA transfer, CUDA host upload |
| `cuda` | CUDA DMA-BUF import, CUDA host upload |
| `vulkan` | Vulkan DMA-BUF import and CUDA transfer, Vulkan host upload and CUDA transfer, CUDA host upload |

`DMABUF_FD` is enabled only after importing an actual capture allocation, mapping it as a linear CUDA buffer, and copying both planes successfully. A GPU name, driver version, or host-allocation export attribute is not sufficient. Failed import/mapping disables that candidate for the recording session. A new session probes again. Explicit `vulkan` bypasses direct CUDA import so the Vulkan path can be tested independently.

The Vulkan device is derived from the CUDA device through FFmpeg, which matches the physical GPU UUID and requests separate plane images for CUDA interop. Imported DMA-BUF storage is described as linear R8 and GR88 layers. The driver must support that allocation and its memory type. The fallback uploads host pixels into Vulkan-owned images, then transfers them through FFmpeg's external-memory/semaphore CUDA interop. Neither fallback is a zero-copy capture-to-encoder claim.

The recording snapshot's `transfer` field reports the active path: `cuda-dmabuf`, `vulkan-dmabuf-cuda`, `vulkan-upload-cuda`, or `cuda-host-upload`. Startup reports `experimental-pending`. Candidate failures are logged once per session; the fallback is not reported as direct DMA-BUF.

## Ownership and synchronization

The DMA capture producer passes an immutable reference-counted frame lease through the existing three-slot bounded media transport. The recording domain does not own capture implementation types. Superseded preview frames are also passed to recording before their final lease permits `QBUF`. Queue overflow releases the rejected lease and increments the existing drop counter. The default recorder still copies into its preallocated slots.

The recording worker retains the source lease until transfer completion. CUDA operations run in the FFmpeg encoder's CUDA context. Direct import maps the complete allocation and uses checked plane offsets to copy into an independent CUDA encoder surface. Host upload attempts portable host registration and uses a pageable upload when registration is unavailable. Registration is released only after completion. Source mappings are never retained by NVENC after upload; FFmpeg's reference-counted hardware surfaces remain owned through encoding.

Vulkan imports retain the capture lease in an `AVBufferRef`. FFmpeg owns imported images, external memory, semaphores, and their completion waits. The CUDA completion boundary precedes release of transferred source frames. A poisoned CUDA context or failed registration/external-memory cleanup terminates the process rather than allowing storage with an uncertain DMA lifetime to be reused. These terminal failures are not treated as ordinary capability fallbacks.

Vulkan initialization, initial surface allocation, and a neutral-frame upload run before attaching the recording input. This avoids filling the capture queue while discovering the Vulkan device. Transfer completion is synchronous in this experiment. It does not implement an asynchronous event-reaped pipeline, and per-frame registration/import overhead has not been optimized. No unbounded queue, timeout-based lease release, or additional production C source is introduced.

## Verification

The tests require an idle capture device for session checks. Use a new output directory for every run because recording never overwrites an existing file.

```sh
make test-architecture
CARGO_TARGET_DIR="$PWD/build/rust-target" cargo test --manifest-path src/rust/Cargo.toml --lib --offline

MYGOFRAME_EXPERIMENTAL_RECORDING_TRANSFER=vulkan \
  CARGO_TARGET_DIR="$PWD/build/rust-target" \
  cargo test --manifest-path src/rust/Cargo.toml experimental_nv12_upload_roundtrip -- --ignored --nocapture

LD_LIBRARY_PATH="$PWD/build" CGO_ENABLED=1 \
  MYGOFRAME_EXPERIMENTAL_RECORDING_TRANSFER=auto \
  MYGOFRAME_RECORDING_TEST_OUTPUT="$PWD/build/recording-transfer-check" \
  python3 tools/go-mygoframe.py test ./src/internal/session/infrastructure/native \
  -run '^TestHardwareRecordingThroughSessionPorts$' -count=1 -v
```

The roundtrip test checks every active NV12 byte at 3840 × 2160 with a padded 4096-byte row stride. In Vulkan mode it asserts that Vulkan actually ran rather than passing through CUDA fallback. This checks transfer integrity before lossy encoding; it does not measure physical display latency or source A/V alignment.

## Local results and remaining limits

Validation on 2026-11-11 used an RTX 5080, NVIDIA 615.78.08, CUDA driver API 13.4, FFmpeg 9.0.2, and an AVerMedia Live Gamer Ultra 2.1 at NV12 3840 × 2160, 60 fps.

- `make all`, `make test`, and `make test-architecture` passed. Rust reported 43 passing tests and four opt-in hardware tests skipped in the ordinary suite.
- CUDA and Vulkan hardware roundtrips preserved every active byte with 4096-byte padded rows. H.264, HEVC, and AV1 session recordings completed through both fallback paths and decoded without FFmpeg errors.
- The final two-window checks ran for 12 seconds, started HEVC recording after 120 captured frames, and recreated the separate window once. Automatic selection recorded 526 frames and explicit Vulkan recorded 527 frames. Both reported zero recording-queue drops. Steady preview submissions were approximately 60 per second in both windows. These are short functional checks, not a long-run performance or physical latency guarantee.
- Direct CUDA import returned 801 (`CUDA_ERROR_NOT_SUPPORTED`) on the actual capture allocation. FFmpeg Vulkan import rejected its memory type with `No memory type found for flags 0x1`. Automatic selection therefore used `cuda-host-upload`; explicit Vulkan used `vulkan-upload-cuda`. Successful direct DMA-BUF operation still needs validation on hardware/exporters that accept it.
- Concurrent capture/recording startup is covered by the regression below. Codec discovery no longer creates experimental transfer devices. Three final 4K60 runs recorded 677, 674, and 675 frames with zero recording-queue drops and a maximum encoded timestamp interval of 17 ms.
- Rust ASan/LSan unit tests and both GPU roundtrips passed with leak detection enabled and no suppression file. GPU checks used `protect_shadow_gap=0`, as required for CUDA address-space compatibility. The standalone Vulkan test also used the existing application's `__GL_DisableDBus=1` compatibility setting for this driver. Without launcher preparation, the negative control reproduced the previously documented NVIDIA D-Bus loss of 2,521 bytes; it is not reported as a clean run.

The GUI harness must run as a separate process because MyGo owns a process-global event loop. `TestMain` runs that loop on the main goroutine, and the test verifies successful recording, zero queue drops, retained DMA preview, and both windows through the existing smoke assertions:

```sh
CGO_ENABLED=1 python3 tools/go-mygoframe.py test -c \
  -o build/recording-gui-tests ./src/internal/desktop/presentation
MYGOFRAME_EXPERIMENTAL_RECORDING_TRANSFER=vulkan \
  MYGOFRAME_RECORDING_GUI_TEST_OUTPUT="$PWD/build/recording-gui-check.mkv" \
  MYGOFRAME_MEASURE_LATENCY=1 LD_LIBRARY_PATH="$PWD/build" \
  ./build/recording-gui-tests -test.run '^TestExperimentalRecordingTwoWindows$' -test.v
```

Build-local evidence is retained under `build/cuda-experiment/`, including `gui-results.json`, `decoded.json`, `gui-auto-v7.log`, `gui-vulkan-v7.log`, the cold-start logs, and sanitizer logs. No physical A/V synchronization, capture-to-photon latency, successful Thor import, or long-duration stability claim is made.

## Concurrent capture and recording startup

Startup GPU discovery calls `mgfr_recording_devices_json` while capture and recording can already be running. Previously its three codec probes called the live `VideoEncoder::new` constructor. With the Vulkan experiment selected, each probe also created, warmed, and destroyed a Vulkan transfer device. A debugger sample captured the discovery thread inside `vkDestroyDevice` while the recording worker waited for a mutex in the NVIDIA driver. Transfer-stage timing recorded upload stalls of approximately 80–290 ms. A three-slot recording queue cannot absorb those stalls without dropping frames.

`VideoEncoder::probe` now opens the hardware codec with experimental transfer disabled, independently of the process transfer setting. Live recording still uses the requested mode. No capture lease, queue capacity, completion synchronization, or teardown lifetime was changed. Failed persistent-image, extra-warmup, and host-staging experiments were removed.

The hardware probe regression opens H.264, HEVC, and AV1 while the process transfer setting is deliberately invalid, then checks that no experimental CUDA surface or Vulkan device exists. The GUI regression starts recording directly after `StartVideo`, keeps startup GPU discovery enabled, and verifies the actual Vulkan transfer path. The initial startup check recreated the capture window once; the current recreation check below recreates it three times.

```sh
MYGOFRAME_EXPERIMENTAL_RECORDING_TRANSFER=invalid \
  CARGO_TARGET_DIR="$PWD/build/rust-target" \
  cargo test --manifest-path src/rust/Cargo.toml \
  hardware_codec_probe_has_no_transfer_resources -- --ignored --nocapture

MYGOFRAME_RECORDING_GUI_SIMULTANEOUS=1 \
  MYGOFRAME_EXPERIMENTAL_RECORDING_TRANSFER=vulkan \
  MYGOFRAME_RECORDING_GUI_TEST_OUTPUT="$PWD/build/vulkan-startup-check.mkv" \
  MYGOFRAME_MEASURE_LATENCY=1 LD_LIBRARY_PATH="$PWD/build" \
  ./build/recording-gui-tests -test.run '^TestExperimentalRecordingTwoWindows$' -test.v
```

Each final run lasted 12 seconds at NV12 3840 × 2160, 60 fps with HEVC and audio disabled. Both windows retained DMA-BUF presentation. The actual encoder transfer was `vulkan-upload-cuda`, because this capture allocation still fails direct Vulkan import. Every file decoded without errors.

| Run | Encoded frames | Recording queue drops | Maximum file timestamp gap | Main submissions/s | Capture submissions/s before / after reopening |
| --- | ---: | ---: | ---: | ---: | ---: |
| Before fix | 636 | 35 | 266 ms | 59.77 | 59.99 / 60.08 |
| Final 1 | 677 | 0 | 17 ms | 59.88 | 59.99 / 50.17 |
| Final 2 | 674 | 0 | 17 ms | 59.89 | 60.01 / 59.93 |
| Final 3 | 675 | 0 | 17 ms | 59.89 | 60.01 / 59.72 |

Submission rates use the interval from two to eleven seconds after the first capture timestamp, intersected with each window context's lifetime. They are not display presentation or capture-to-photon measurements. One reopened capture window still submitted at approximately 50 fps with occasional 33–35 ms intervals; this presentation variability remained at that stage, although the recording in that run had no frame gaps above 17 ms. These results establish the startup recording fix, not universally stable 60 Hz presentation after window recreation.

`make all` and `make test` (including architecture checks) passed. Rust ASan/LSan reported 43 passing unit tests with four hardware tests skipped. The hardware probe and padded 4K Vulkan roundtrip also passed under ASan/LSan with leak detection enabled and no suppression file. The standalone GPU tests used the CUDA address-space option and existing NVIDIA launcher compatibility setting documented above. Full-GUI sanitizer coverage was not rerun for this constructor separation.

Evidence is in `build/vulkan-startup/`: `before.log`, `gdb-upload2.log`, `final-{1,2,3}.log`, `results.json`, `test.log`, and `asan-{probe,vulkan,unit}.log`. Temporary upload/import timing instrumentation was removed from production code.

## API references

- [CUDA external-resource interoperability](https://docs.nvidia.com/cuda/cuda-driver-api/group__CUDA__EXTRES__INTEROP.html): `DMABUF_FD` import, buffer mapping, and cleanup. The documented direct-import support is Tegra Thor onward; this implementation still tests each actual allocation.
- [FFmpeg Vulkan implementation](https://github.com/FFmpeg/FFmpeg/blob/master/libavutil/hwcontext_vulkan.c): device derivation, DRM imports, CUDA external-memory/semaphore transfers, and image retirement. Runtime support depends on the installed FFmpeg build and drivers.
- [FFmpeg hardware contexts](https://ffmpeg.org/doxygen/trunk/hwcontext_8h.html): device/frame ownership and transfer APIs.


## Capture-window recreation pacing

The subsequent recreation fix removes an independent GDK paint deadline for external damage in the verified private runtime. It preserves backend freezes, animation pacing, render-callback deferral, capture leases, GPU fences, and the bounded recording queue. See [native GUI presentation](gui-presentation.md) for activation and fallback behavior.

The final hardware checks used the same RTX 5080 and NV12 3840 × 2160, 60 fps source, simultaneous Vulkan recording startup, a 1280 × 800 main window, and a 960 × 540 capture window. Each run lasted 24.024 seconds and recreated the capture window three times. Audio was disabled. Distinct-frame submission rates exclude the first 30 samples of each context.

| Run | Main fps | Capture contexts fps | Recording queue drops |
| --- | ---: | --- | ---: |
| complete-1 | 59.96 | 60.00 / 60.00 / 60.03 / 60.01 | 0 |
| complete-2 | 59.96 | 60.01 / 60.00 / 60.02 / 60.01 | 0 |
| complete-3 | 59.96 | 60.00 / 60.01 / 60.00 / 60.00 | 0 |

The same executable with `MYGO_GDK_EVENT_DRIVEN_PAINT=0` reproduced 54.14 fps after recreation and failed the new 58 fps per-context regression threshold. The test requires all five contexts, at least 120 distinct frames per context, DMA-BUF preview retention, and zero recording queue drops. `MYGOFRAME_RECORDING_GUI_MIN_FPS=0` disables only the timing threshold for sanitizer runs.

Actual recording transfer remained `vulkan-upload-cuda`, not direct Vulkan DMA-BUF import. These are GPU submission rates rather than physical scanout measurements. Occasional main-window submission gaps reached 36.51 ms, so the results do not establish zero display jitter. Raw logs and summaries are under `build/vulkan-reopen/complete-*`; the negative control is `control-off.log`.

All three output files report HEVC 3840 × 2160 at 60/1, with maximum packet PTS spacing of 17 ms. Validation commands are `make all`, `make test test-race`, `make test-mygo-ownership`, and the opt-in `TestExperimentalRecordingTwoWindows` GUI test. Decode results are recorded in `build/vulkan-reopen/decode-summary.json`.

Sanitizer runs can set `MYGOFRAME_RECORDING_GUI_DURATION=240s` to include NVIDIA initialization before the first recreation. Shorter 24-second and 90-second instrumented attempts failed frame-coverage assertions rather than reporting memory errors; they are not successful memory-validation runs. The normal performance duration remains 24.024 seconds.

The GPU submission diagnostic buffer intentionally stops at 7,200 samples. Long sanitizer runs therefore validate all five contexts with independent test-only distinct-texture counters rather than interpreting the truncated timing samples as missing windows. Each context must still produce at least 120 distinct textures. An initial 240-second attempt reached the diagnostic cap and failed that older assertion; no production diagnostic limit was increased.

The recreation change is confined to `native/patches/gdk-3.24.52-event-driven-paint.patch`, `patches/mygo/0004-event-driven-paint.patch`, private-runtime capability policy, and the opt-in GUI regression. It does not alter capture queue capacity, encoding surfaces, DMA-BUF lease ownership, or fence retirement.

An inherited `GBM_BACKEND=nvidia-drm` override was present during the initial checks. The application correctly preserved it. The 240-second ASan run completed all five context counters (12,167 / 1,354 / 3,584 / 3,577 / 3,573 distinct textures) and recorded 11,698 frames with zero queue drops, but LSan exited 23 with two 112-byte allocations in `nvidia-drm_gbm.so`. This matches the previously isolated [NVIDIA GBM modifier leak](egl-leaks.md). It is not a clean memory result. Automatic-policy validation removes only this inherited override with `env -u GBM_BACKEND`; no host setting is changed and no leak suppression is used.

Default-policy performance checks (`env -u GBM_BACKEND`) also passed all five contexts in three 24.024-second runs:

| Run | Main fps | Capture contexts fps | Recording queue drops |
| --- | ---: | --- | ---: |
| default-policy | 59.96 | 60.03 / 60.01 / 60.02 / 60.03 | 0 |
| default-policy-2 | 59.96 | 60.02 / 60.00 / 60.00 / 60.01 | 0 |
| default-policy-3 | 59.96 | 60.00 / 59.99 / 59.98 / 60.02 | 0 |

The first default-policy file has a maximum packet PTS interval of 34 ms despite zero queue-overflow drops. Queue-drop counters alone therefore do not establish perfectly uniform file cadence. The recreation fix establishes sustained preview submission rates, not the absence of every recording timestamp gap. The automatic-policy sanitizer process mapped Mesa `gbm/dri_gbm.so`, `libgallium`, the Vulkan loader, and the verified private GDK; selected library mappings are recorded in `default-runtime.json`.

The GUI sanitizer executable is built with `CC=clang-23 CGO_ENABLED=1 python3 tools/go-mygoframe.py test -asan -c`. It uses `ASAN_OPTIONS=protect_shadow_gap=0:detect_leaks=1:leak_check_at_exit=1:halt_on_error=1` and `LSAN_OPTIONS=detect_leaks=1:leak_check_at_exit=1:exitcode=23`, without suppressions. The Go/cgo executable is instrumented; release Rust, private GTK, and vendor libraries are not fully address-instrumented. LSan observes intercepted allocations across those libraries. A clean run does not prove every native memory access safe.

The final automatic-policy ASan/LSan run (`asan-default.log`) exited **0** after the 240-second GUI lifecycle, with no address-error or leak report and no suppressions. All five contexts produced distinct textures (14,352 / 3,552 / 3,581 / 3,582 / 3,587). Recording completed with 14,366 frames, zero queue drops, and `vulkan-upload-cuda`. Capture and recorder shutdown completed normally. The three default-policy release recordings also decoded without errors; maximum packet PTS gaps were 34 / 17 / 17 ms respectively. The inherited NVIDIA-backend override remains caller-controlled and can re-enable its known vendor leak.


## Recording timestamp quantization

The occasional 34 ms file gap came from using the nominal frame interval as the encoder time base. `av_rescale_q` rounded capture timestamps to 1/60-second ticks, and both ordinary and experimental submission paths rejected a PTS no greater than the preceding PTS. Adjacent valid capture timestamps could fall into the same tick when the recording origin was near a rounding boundary. This loss happened after the input queue, so its overflow counter remained zero.

A temporary diagnostic build placed the recording origin half a frame before the first real input. It retained actual device timestamps and ran simultaneous 4K60 Vulkan recording with both windows and three capture-window recreations. Before the fix, 1,395 input frames produced 1,379 packets: all 16 missing packets matched duplicate PTS values, with a maximum file gap of 34 ms. For example, timestamps 75,000,333 ns and 91,665,333 ns relative to the origin both rounded to tick 5. This diagnostic changed only the test origin; it was not a production timestamp policy.

The encoder now uses a 1/1,000,000-second time base while retaining the negotiated `framerate`. The example becomes PTS 75,000 and 91,665. FFmpeg's [NVENC initialization](https://github.com/FFmpeg/FFmpeg/blob/master/libavcodec/nvenc.c) uses an explicitly configured frame rate independently of its time base. The shared audio/video monotonic origin remains unchanged, and real input gaps remain gaps. There is no frame duplication, queue expansion, buffer lifetime change, or synthetic sequential timestamp replacement.

The same half-frame-origin control after the fix recorded all 1,398 inputs, with no repeated PTS and a maximum packet interval of 17 ms. Both diagnostic environment switches and per-frame logging were removed before final builds. Evidence is under `build/recording-cadence/`.

Regression coverage includes the device-derived collision, alternating timestamp jitter at 24/60/59.94/240 fps, one-hour timestamp precision, and preservation of a real 50 ms input gap. NVENC H.264, HEVC, and AV1 hardware tests passed with NV12. Intel/AMD hardware encoding was not tested on this host. The opt-in two-window GUI test now uses `ffprobe` to require 3840 × 2160 HEVC at nominal 60/1, one packet per reported encoded frame, increasing PTS, and at most 20 ms between packets in performance runs. Sanitizer runs retain format, packet-count, and monotonicity checks but disable timing limits together with the existing FPS limit.

Final `make all test test-race test-sanitize` completed successfully, including 44 Rust ASan tests (four hardware-only tests skipped by the default suite). The separately invoked NV12 NVENC codec test passed H.264, HEVC, and AV1 with 12 packets each and 17 ms maximum PTS spacing.

The whole-GUI sanitizer check used the existing private runtime, `env -u GBM_BACKEND`, simultaneous Vulkan recording, three capture-window recreations, and a 120-second GUI duration. It exited 0 without ASan/LSan reports or suppressions. All five contexts rendered at least 1,740 distinct textures. The completed recording contained 7,166 packets, zero queue drops, and a 17 ms maximum PTS gap. Logs are `build/recording-cadence/regression.log`, `codecs.log`, and `asan.log`. The GUI executable's instrumentation boundary described above still applies; Rust unit checks separately used the instrumented Rust build.

Final release GUI runs used NV12 3840 × 2160 at 60 fps, HEVC, simultaneous capture/recording startup, both preview windows, and three capture-window recreations per run. Each run lasted 24.024 seconds with the default graphics policy. All five contexts passed the 58 fps submission threshold, and all output packets passed the 20 ms cadence threshold.

| Run | Main submissions/s | Capture contexts submissions/s | Maximum file PTS gap | Queue drops |
| --- | ---: | --- | ---: | ---: |
| final-1 | 59.96 | 59.98 / 60.01 / 60.00 / 60.02 | 17 ms | 0 |
| final-2 | 59.96 | 59.99 / 60.00 / 60.01 / 60.01 | 17 ms | 0 |
| final-3 | 60.00 | 60.01 / 60.00 / 59.99 / 60.00 | 17 ms | 0 |

The actual recording transfer remained `vulkan-upload-cuda`, and both preview windows retained DMA-BUF presentation. The rates above measure distinct GPU submissions, not physical scanout. These runs resolve the demonstrated timestamp collision; real source loss, hardware stalls, and other overloads can still produce gaps. All three final files decoded completely without errors. Performance summaries and complete file-decode results are in `build/recording-cadence/final-*-summary.json` and `decode.json`.
