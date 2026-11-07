# DMA-BUF color validation

The production NV12 SDR renderer passed controlled 3840 × 2160 color comparisons for limited and full Rec.709 range on the tested NVIDIA system. Each range used 24 newly acquired frames in one capture session and one GPU renderer. All six capture slots were reused. This validates the sampled conversion and source visibility through the production DMA-BUF renderer; it does not establish HDMI input accuracy or calibrated display output.

## Tested environment

| Component | Observed value |
| --- | --- |
| Capture device | Live Gamer Ultra 2.1 at `/dev/video0` |
| Capture layout | Linear NV12, 3840 × 2160, 3840-byte stride, 12,441,600-byte source allocation |
| Requested capture rate | 60 frames per second |
| Resolved encoding | Rec.709 (`encoding=2`) |
| GPU | NVIDIA GeForce RTX 5080 |
| NVIDIA driver | 615.78.08 |
| Graphics API | GLES 3 through Wayland EGL; EGL vendor `NVIDIA` |
| Kernel | `7.3.1-arare-workstation-bore-llvm-v1` |
| DMA-BUF exporter | `videobuf2_vmalloc`, read from `/proc/self/fdinfo/<fd>` |
| CPU cache capability | x86-64, CLFLUSH and CLFLUSHOPT, 64-byte cache lines |

The exporter name is checked from the live descriptor rather than inferred from the video-device name. Linux exposes DMA-BUF information through procfs and sysfs. See [DMA-BUF statistics](https://www.kernel.org/doc/html/v6.6/driver-api/dma-buf.html#dma-buf-statistics).

## Reproduce the production check

Build the production library and close other capture applications before opening the device:

```sh
make core
python3 tools/validate-dma-colors.py --width 3840 --height 2160 --fps 60 --repeat-frames 24 --poison-pack-state --output build/dma-colors.json
```

The command must exit successfully and the JSON `passed` field must be `true`. Do not add the diagnostic flush, stop, or finish options when validating the shipped path. The tool restores the original V4L2 format and releases its sessions, leases, renderer, and EGL context on exit.

For each range, the tool performs the following checks:

1. Acquire a new frame token after the previous sequence and retain its capture lease.
2. Write 24 flat NV12 patches into that leased allocation before its first GPU read. The patches contain eight RGB bars, eight gray levels, and eight chroma cases. Gray and chroma values vary between frames; white and black remain fixed.
3. Bracket CPU mapping access with `DMA_BUF_IOCTL_SYNC`. Convert the same held frame through `mgfr_dma_copy_rgba`, which uses the production FFmpeg CPU converter and the frame's resolved encoding and range.
4. Read back the production GPU renderer's output. Compare RGB channels at an 8 × 8 grid of interior points in each patch: 1,536 pixels or 4,608 RGB channel values per frame.
5. Require a maximum sampled channel error of three 8-bit code values, opaque alpha, and correct black and white endpoints. The tolerance allows coefficient and rounding differences; patch boundaries are excluded.
6. Hash the source before and after GPU access and repeat the CPU conversion. Require unchanged source hashes and zero sampled change in the CPU reference.
7. Poison GL pack alignment, row length, skipped rows and pixels, and the pack-buffer binding. Pass exactly 33,177,600 output bytes to native readback and require all 184,576 trailing canary bytes to remain unchanged. Require the exact poisoned state to be restored.

The repeated run keeps the same GPU renderer across capture-slot reuse. A new pattern on each token detects stale imported-image contents. `capturedSequence` must increase, `captureSlots` records the slots visited, and `reusedSlotCount` records acquisitions after a previously visited slot.

## Production results

| Range | Frames | Maximum RGB error | Mean RGB error | Largest per-frame p95 error | Slots visited | Repeated slots |
| --- | ---: | ---: | ---: | ---: | --- | ---: |
| Limited | 24 | 3 | 1.351852 | 3 | 0–5 | 18 |
| Full | 24 | 2 | 0.147569 | 2 | 0–5 | 18 |

Errors are measured in 8-bit RGB code values. The p95 column is the maximum of the 24 per-frame p95 values, not a pooled percentile. The mean column averages equally sized per-frame samples. Both ranges had zero black/white endpoint error and zero alpha error. All 48 source hashes, repeated CPU-reference comparisons, canaries, and GL-state restoration checks passed. No tool-side cache flush was enabled. The original device format was restored.

## Driver range hints and explicit conversion

The initial renderer imported NV12 as a YUV EGLImage and supplied Rec.709 and limited/full range hints. On the tested driver, limited-range samples were within three code values. Full-range neutral samples instead followed a limited-range expansion: a CPU reference gray of 64 became 56 on the GPU, and 192 became 205. This was observed behavior on this driver, not a guarantee about other NVIDIA versions. EGL defines these interpretation attributes as hints and permits implementations to approximate unsupported combinations. See [EGL_EXT_image_dma_buf_import](https://registry.khronos.org/EGL/extensions/EXT/EGL_EXT_image_dma_buf_import.txt).

The production shader therefore imports two byte views of the same allocation: an R8 luma image and an R8 image covering the interleaved UV bytes at half height. It samples adjacent U and V bytes explicitly. This avoids both driver YUV range conversion and reliance on an RG88 channel interpretation.

The shader uses high-precision samplers and explicit Rec.601 or Rec.709 matrix rows. For limited range it normalizes `Y` with `(Y - 16) / 219` and chroma with `(U - 128) / 224` and `(V - 128) / 224`. For full range it uses `Y / 255`, `(U - 128) / 255`, and `(V - 128) / 255`. The hardware comparison above covers Rec.709; Rec.601 hardware accuracy was not established by this run.

Each plane import requests `EGL_IMAGE_PRESERVED_KHR=EGL_TRUE`, because the default does not preserve source pixels. Reused external textures are rebound after source preparation. These requirements are specified by [EGL_KHR_image_base](https://registry.khronos.org/EGL/extensions/KHR/EGL_KHR_image_base.txt) and [OES_EGL_image_external](https://registry.khronos.org/OpenGL/extensions/OES/OES_EGL_image_external.txt).

## Source cache-coherency diagnosis

Explicit conversion fixed the range mismatch, but some runs still contained sparse horizontal corruption with errors up to 255. Source SHA-256 hashes remained unchanged across the GPU read, and a second CPU conversion matched the first at every sampled point. Destroying the capture session before writing and sampling the retained allocation did not remove the corruption. These controls excluded continued capture writes to the held slot in the observed failures.

`mmap.flush()` returned `EINVAL` on this exported mapping. An extra `glFinish`, preserved EGL imports, and high-precision sampler declarations did not remove the corruption. A tool-side CLFLUSH plus MFENCE control did remove it for both ranges. CLFLUSHOPT plus SFENCE then passed five fresh processes with two ranges each. This combination identifies missing CPU-cache visibility in the tested NVIDIA/`videobuf2_vmalloc` path as the supported diagnosis.

The inspected `vb2_vmalloc_dmabuf_ops` contains DMA attachment mapping callbacks but no `begin_cpu_access` or `end_cpu_access` callbacks. The [upstream videobuf2-vmalloc source](https://github.com/torvalds/linux/blob/master/drivers/media/common/videobuf2/videobuf2-vmalloc.c) has the same callback structure. Linux documents that `DMA_BUF_IOCTL_SYNC` forwards CPU coherency markers to exporter hooks; it does not provide mutual exclusion, and fence completion does not replace necessary cache preparation. The diagnostic continued to bracket all mapped CPU accesses as required by the [DMA-BUF synchronization contract](https://docs.kernel.org/driver-api/dma-buf.html#cpu-access-to-dma-buffer-objects).

The production workaround is restricted to an NVIDIA GL vendor and the exact `videobuf2_vmalloc` exporter. After dequeue and before GPU import or sampling, the frame performs cache-line writeback once. A per-frame `OnceLock` shares the result across consumers. CPUID selects CLFLUSHOPT plus SFENCE when supported or CLFLUSH plus MFENCE otherwise. Unsupported capabilities or invalid mapping bounds reject that GPU path so the session can use CPU fallback. Other exporter/vendor combinations do not receive this workaround. The instruction ordering is described in the [Intel optimization manual, section 9.4.7](https://cdrdv2-public.intel.com/671488/248966-Software-Optimization-Manual-V1-048.pdf).

This workaround addresses visibility. Native GPU fences and frame leases separately prevent capture-slot reuse until GPU reads complete. No C implementation or new source dependency was added for either the diagnostic or the production cache preparation.

## Diagnostic writeback cost

Five fresh processes each measured limited and full range after CPU-writing a 12,441,600-byte source mapping. The ten CLFLUSHOPT/SFENCE durations were 302,359; 303,161; 233,268; 368,122; 291,797; 280,083; 242,385; 446,028; 215,593; and 435,994 nanoseconds.

| Diagnostic | Samples | Median | p95, nearest rank | Minimum | Maximum |
| --- | ---: | ---: | ---: | ---: | ---: |
| CLFLUSHOPT + SFENCE | 10 | 0.297 ms | 0.446 ms | 0.216 ms | 0.446 ms |

The earlier CLFLUSH/MFENCE control took 5.568 ms and 7.649 ms in two samples. These are tool-side dirty-source writeback measurements on one CPU. They are not a production frame-time distribution, total GPU time, capture latency, or input-to-display latency. Use the [video benchmark](benchmark-video.md) for the application measurement procedure and its limits.

## Evidence and limits

The recorded test workspace keeps JSON and console logs under `work/dmabuf/`. Matching `.log` files contain the complete JSON console output. These generated artifacts are not source dependencies.

| Evidence filename | Purpose |
| --- | --- |
| `color-validation-initial.json` | Driver NV12 conversion hints and full-range gray mismatch |
| `color-validation-hashes-live.json` | Stable source and CPU reference during live capture despite GPU corruption |
| `color-validation-hashes-stopped.json` | Retained allocation control after capture shutdown |
| `color-validation-preserve.json` and `color-validation-highp.json` | Preservation and sampler-precision controls |
| `color-validation-clflush.json` | Successful CLFLUSH/MFENCE visibility control |
| `color-validation-clflushopt-1.json` through `color-validation-clflushopt-5.json` | Five successful optimized controls and ten writeback timings |
| `color-validation-production-repeat.json` | Final production path, two ranges × 24 varied frames, no diagnostic writeback |

The check samples flat-patch interiors rather than every output pixel. It does not validate chroma siting at edges, scaling quality, arbitrary imagery, Rec.601 hardware output, HDR, P010, wide-gamut input, or other devices and drivers. Because known patterns overwrite leased capture memory, it does not measure the capture card's HDMI signal conversion. Readback also does not measure desktop color management, monitor calibration, visible display color, audio synchronization, or end-to-end latency. The synchronous color readback check does not establish concurrent GPU fence safety by itself; use the ownership tests and actual multi-window UI checks documented in [Verification](verification.md).
