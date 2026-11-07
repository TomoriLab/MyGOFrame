#ifndef MYGOFRAME_RUST_CORE_H
#define MYGOFRAME_RUST_CORE_H

#include <stddef.h>
#include <stdint.h>

typedef struct {
    const char *path;
    uint32_t fourcc;
    uint32_t width;
    uint32_t height;
    uint32_t interval_numerator;
    uint32_t interval_denominator;
    int32_t color_range;
} mgfr_video_config;

typedef struct {
    uint32_t fourcc;
    uint32_t width;
    uint32_t height;
    uint32_t stride;
    uint32_t interval_numerator;
    uint32_t interval_denominator;
    uint32_t bytes_per_line;
    uint32_t quantization;
    uint32_t encoding;
    uint32_t colorspace;
    uint32_t transfer_function;
    uint32_t capture_buffers;
} mgfr_video_info;

typedef struct {
    void *token;
    const uint8_t *pixels;
    size_t byte_length;
    uint32_t width;
    uint32_t height;
    uint32_t stride;
    uint64_t sequence;
    uint64_t timestamp_ns;
} mgfr_frame_view;

typedef struct {
    uint32_t abi_version;
    uint32_t struct_size;
    void *token;
    int32_t fd;
    uint32_t index;
    uint32_t width;
    uint32_t height;
    uint32_t stride;
    uint32_t fourcc;
    size_t byte_length;
    uint64_t uv_offset;
    uint64_t modifier;
    uint64_t generation;
    uint64_t sequence;
    uint64_t timestamp_ns;
    int32_t color_range;
    uint32_t encoding;
    uint32_t colorspace;
    uint32_t transfer_function;
} mgfr_dma_frame_view;

typedef struct {
    uint64_t frames;
    uint64_t dropped_frames;
    int32_t running;
    char error[512];
} mgfr_video_status_data;

typedef struct {
    int32_t running;
    int32_t capture_state;
    int32_t playback_state;
    uint64_t queued_frames;
    uint64_t dropped_frames;
    uint64_t underrun_frames;
    uint64_t captured_frames;
    uint64_t played_frames;
    uint32_t requested_latency_frames;
    uint32_t sample_rate;
    char error[512];
} mgfr_audio_status_data;

/* The caller serializes session control. Frame releases remain independent. */
void *mgfr_video_create(const mgfr_video_config *config, mgfr_video_info *info, char **error);
void mgfr_video_destroy(void *video);
int32_t mgfr_video_acquire(void *video, uint64_t after_sequence, mgfr_frame_view *frame);
void mgfr_video_release(void *token);
int32_t mgfr_video_status(void *video, mgfr_video_status_data *status);

/* DMA descriptors borrow their FD until their matching frame token is released. */
void *mgfr_dma_video_create(const mgfr_video_config *config, mgfr_video_info *info, char **error);
void mgfr_dma_video_destroy(void *video);
int32_t mgfr_dma_video_acquire(void *video, uint64_t after_sequence, mgfr_dma_frame_view *frame);
void mgfr_dma_video_release(void *token);
int32_t mgfr_dma_video_status(void *video, mgfr_video_status_data *status);
/* This diagnostic copy uses the CPU reference converter while the frame lease remains held. */
int32_t mgfr_dma_copy_rgba(void *token, uint8_t *pixels, size_t capacity, char **error);
char *mgfr_video_devices_json(void);
char *mgfr_video_modes_json(const char *path);

void *mgfr_audio_create(const char *source, const char *output, float volume, char **error);
void mgfr_audio_destroy(void *audio);
void mgfr_audio_set_volume(void *audio, float volume);
int32_t mgfr_audio_status(void *audio, mgfr_audio_status_data *status);
char *mgfr_audio_devices_json(void);

/* Rust allocates returned strings and only this function releases them. */
void mgfr_string_free(char *string);

typedef struct {
    uint64_t sequence;
    uint64_t capture_timestamp_ns;
    uint64_t submit_timestamp_ns;
    uint64_t gpu_submit_cpu_ns;
    uint32_t width;
    uint32_t height;
} mgfr_gpu_metrics;

/* GPU renderer calls require the original GLES context current on the calling thread. */
void *mgfr_gpu_create(char **error);
int32_t mgfr_gpu_render(void *renderer, void *frame_token, uint32_t *texture,
                        mgfr_gpu_metrics *metrics, char **error);
void mgfr_gpu_destroy(void *renderer);
/* Context-loss cleanup releases EGL images without deleting objects in a different GL context. */
void mgfr_gpu_abandon(void *renderer);
int32_t mgfr_gpu_readback(void *renderer, void *frame_token, uint8_t *output,
                          size_t byte_length, char **error);
/* Optional measurement fences report capture-to-texture readiness, not display scanout. */
int32_t mgfr_gpu_timing_mark(uint64_t capture_timestamp_ns, uint64_t sequence,
                            uint64_t context_id);
char *mgfr_gpu_timings_json(void);

#endif
