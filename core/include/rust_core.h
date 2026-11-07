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
char *mgfr_video_devices_json(void);
char *mgfr_video_modes_json(const char *path);

void *mgfr_audio_create(const char *source, const char *output, float volume, char **error);
void mgfr_audio_destroy(void *audio);
void mgfr_audio_set_volume(void *audio, float volume);
int32_t mgfr_audio_status(void *audio, mgfr_audio_status_data *status);
char *mgfr_audio_devices_json(void);

/* Rust allocates returned strings and only this function releases them. */
void mgfr_string_free(char *string);

#endif
