# DMA-BUF raw formats

The automatic backend supports the following progressive, single-planar V4L2 layouts. Compressed MJPEG remains on the CPU decoder path. Unsupported layouts, color metadata, export capabilities, or graphics imports retain CPU fallback with a reason in status.

| V4L2 format | Source layout | R8 image width | Chroma image | Dimension restrictions |
| --- | --- | --- | --- | --- |
| NV12 | Eight-bit Y followed by interleaved UV | Width | Half height at `stride × height` | Even width and height |
| P010 | Little-endian words containing ten significant high bits | `2 × width` | Half height at `stride × height` | Even width, height, and byte stride |
| YUYV | Y0, U, Y1, V for each pair | `2 × width` | None | Even width |
| UYVY | U, Y0, V, Y1 for each pair | `2 × width` | None | Even width |
| BGR3 | B, G, R for each pixel | `3 × width` | None | Positive dimensions |
| RGB3 | R, G, B for each pixel | `3 × width` | None | Positive dimensions |

These byte layouts follow the Linux [planar YUV](https://docs.kernel.org/userspace-api/media/v4l/pixfmt-yuv-planar.html), [packed YUV](https://docs.kernel.org/userspace-api/media/v4l/pixfmt-packed-yuv.html), and [packed RGB](https://docs.kernel.org/userspace-api/media/v4l/pixfmt-rgb.html) specifications. P010's six low bits are padding; the shader reconstructs the ten significant bits before conversion.

## Bounds and lifetime

Rust validates requested dimensions before opening the device and validates the negotiated fourcc, progressive field, row stride, allocation size, and source-plane offsets before allocating capture slots. Each source is limited to 128 MiB, the complete capture pool to 512 MiB, and the potential RGBA output to the existing 128 MiB frame limit. Go independently validates the descriptor at the language boundary. The ABI structure remains version 1; packed formats use `uv_offset=0` and RGB uses `encoding=0` because it has no YCbCr matrix.

Each source byte becomes one R8 texel. Its width and height must fit the current context's `GL_MAX_TEXTURE_SIZE`; wider raw rows use CPU fallback. EGL imports preserve pixels and retain the negotiated byte pitch, including padding. The shader samples byte centers with nearest filtering. Explicit unpacking avoids dependence on driver YUV range hints, RGB channel conventions, or R16 import support. See the [EGL DMA-BUF import contract](https://registry.khronos.org/EGL/extensions/EXT/EGL_EXT_image_dma_buf_import.txt).

The existing six-slot capture pool, source-cache visibility check, per-frame leases, native GPU fences, and independent MyGo window renderers remain in use. Packed formats import one image; NV12 and P010 import two. Import failures destroy every image already created before requesting CPU fallback. Cached images are retired when the session generation changes. Production conversion performs no CPU RGBA conversion, texture upload, or synchronous readback.

## Color contract

YUV accepts Rec.601 and Rec.709 matrices with Rec.709 or sRGB transfer functions. RGB skips the YUV matrix and defaults to full range when quantization is unspecified. Explicit limited/full range selections apply to both RGB and YUV. BT.2020, DCI-P3, and unsupported transfer functions reject the DMA path. CPU fallback preserves the existing converter's behavior; it does not add HDR tone mapping.

P010 limited range uses Y codes 64–940 and chroma codes 64–960 with neutral chroma at 512. Full range uses 0–1023 with neutral chroma at 512. Output remains RGBA8 SDR, so this feature does not provide a ten-bit display surface or HDR output.

## Validation procedure

`tools/validate-dma-colors.py --format FORMAT` writes known patches only into a held capture lease before GPU submission. Default checks require both the production FFmpeg reference and analytic source-sample conversion to agree with the GPU within three eight-bit RGB code values. Repeated frames vary the pattern and reuse capture slots. Source hashes, repeated CPU conversion, readback canaries, and poisoned GL pack state detect unintended writes and state corruption.

`--alternating-luma` adds adjacent pixels with different luma values. This checks packed-pixel parity and P010 low-bit reconstruction against analytic source values. The complete FFmpeg comparison is retained separately in `cpuComparison`; it is not the acceptance reference for this spatial pattern. At 3840 pixels wide, the existing FFmpeg fast-bilinear converter produced different values on alternating luma samples. The initial failed comparisons remain in the validation artifacts. Flat-patch comparisons continue to require the unchanged three-code-value tolerance.

The hardware tools require exclusive use of the capture device. Select modes actually reported by the device, for example:

```sh
python3 tools/validate-dma-colors.py --format YUYV --width 3840 --height 2160 --fps 50 --repeat-frames 12 --poison-pack-state --output build/yuyv-colors.json
python3 tools/validate-dma-colors.py --format P010 --width 3840 --height 2160 --fps 30 --repeat-frames 4 --alternating-luma --poison-pack-state --output build/p010-parity.json
python3 tools/benchmark-video.py --format BGR3 --width 2560 --height 1440 --fps 60 --capture-window --output build/bgr3-benchmark.json
```

Hardware validation covers exported capture allocations and GPU conversion, not the capture card's HDMI conversion, desktop color management, monitor calibration, or input-to-photon latency. UYVY and RGB3 are absent from the attached card's mode list; their layouts and diagnostic byte order have automated coverage, but their hardware paths need another device.

## Recorded device results

On 2026-11-09, the Live Gamer Ultra 2.1 and NVIDIA RTX 5080 with driver 615.78.08 passed the following checks. The host ran kernel `7.3.1-arare-workstation-bore-llvm-v1` and libswscale `10.1.102`. Tests removed the inherited `GBM_BACKEND=nvidia-drm` override to exercise the application's existing automatic Mesa DRI/Zink compatibility policy. No new graphics policy or dependency was added.

| Format | Negotiated input | Native UI backend | Limited/full maximum error against FFmpeg | Limited/full maximum error against analytic samples |
| --- | --- | --- | --- | --- |
| YUYV | 3840 × 2160 at 50 fps | DMA-BUF | 3 / 2 | 1 / 1 |
| P010 | 3840 × 2160 at 30 fps | DMA-BUF | 3 / 2 | 1 / 1 |
| BGR3 | 2560 × 1440 at 60 fps | DMA-BUF | 1 / 0 | 1 / 0 |
| NV12 regression | 3840 × 2160 at 60 fps | DMA-BUF | 3 / 2 | 1 / 1 |

Errors are eight-bit RGB code values. Each format used twelve newly leased frames per range for the high-resolution flat-patch test. Separate alternating-luma runs used four frames per range for YUYV, P010, and NV12 and had a maximum analytic error of one. All source hashes, repeated CPU-reference reads, canaries, and GL pack-state checks passed. The card does not advertise 4K60 for YUYV or P010, or 4K for BGR3.

Nine-second native UI checks kept both windows active and recreated the capture window three times per format. Every case reported `videoBackend=dmabuf`, the requested format and interval, rendered frames in both roles, and empty stderr. These are startup, rendering, and lifecycle checks; negotiated input rates do not establish sustained presentation rates. Forced GPU initialization failure returned YUYV, P010, and BGR3 to CPU rendering in both windows. MJPEG selected CPU decoding with an explicit fallback reason.
