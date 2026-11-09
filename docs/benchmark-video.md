# Video path benchmark

`tools/benchmark-video.py` compares the CPU and DMA-BUF paths with the same raw video mode and native window sizes. `--format` accepts NV12 (the default), P010, YUYV, UYVY, BGR3, and RGB3. Select dimensions and a frame rate that the device reports for that format. The tool verifies the negotiated format, dimensions, and frame interval. It runs three CPU/DMA pairs by default, with 30 seconds per run, and rejects a DMA run that silently falls back to CPU processing.

```sh
make
python3 tools/benchmark-video.py --device /dev/video0 --output build/benchmarks/4k60.json
python3 tools/benchmark-video.py --device /dev/video0 --capture-window --output build/benchmarks/4k60-sharing.json
```

Close another viewer using the capture device before running the benchmark. The command configures and streams from the selected device. Audio is disabled. Keep console output, display configuration, window placement, and background load consistent across runs.

Options include capture dimensions and frame rate, initial window dimensions, color range, repetition count, duration, and capture-window recreation. `--first-backend dma` reverses the starting backend while retaining alternating runs. Each child process has a timeout; an interrupted or failed benchmark saves its partial report and terminates only its own child process group.

The JSON report contains the application status and commands for each run, process user/system time, CPU percentage, peak resident memory, captured frames, software drops, and GPU timing percentiles. Separate stdout, stderr, and resource records are saved beside the report. GNU `/usr/bin/time` is used when available; otherwise Linux `wait4` provides per-child resource usage without an additional package.

CPU percentage is `(user time + system time) / elapsed wall time × 100`, so 100% represents one core. Capture average FPS includes startup. The GPU collector's observed FPS excludes its two-second warmup and measures completed texture preparations in each renderer context.

Latency measures the capture timestamp to GPU texture-ready completion, before UI compositing, compositor presentation, and display scanout. It does not measure controller-to-display latency. Aggregated p50 and p95 results are medians of each run's percentile, not percentiles of pooled samples. Renderer IDs are process-local and their creation order can vary. Schema version 2 records each native window's observed context ID before teardown and compares `main` and `capture` roles independently of ID order. A missing role rejects the comparison. Native mapped visibility does not establish absence of occlusion; keep both windows on the same output without overlap and record compositor geometry for controlled backend comparisons.

The completion collector polls native fence descriptors at a nominal one-millisecond interval. Measurements include observation and scheduling delay; they are not GPU hardware timestamps. The same collector observes both CPU uploads and DMA-BUF preparation. Compare runs under equivalent desktop load.

Do not interpret a lower CPU percentage alone as lower latency. Compare texture-ready p50/p95 and post-warmup throughput at equal viewport sizes. Small CPU previews can upload a downsampled image, so a full-size RGBA upload estimate does not apply to every window size.
