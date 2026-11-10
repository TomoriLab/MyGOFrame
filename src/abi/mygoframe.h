#ifndef MYGOFRAME_H
#define MYGOFRAME_H
#include <stdint.h>
#include <stddef.h>
#include "rust_core.h"

typedef struct {
    void *token;
    const uint8_t *pixels;
    size_t byte_length;
    uint32_t width;
    uint32_t height;
    uint32_t stride;
    uint64_t sequence;
    uint64_t timestamp_ns;
} mgf_frame_view;

/* Session handles are never reused. Swift serializes calls and rejects closed handles. */
typedef uint64_t mgf_engine_handle;
uint32_t mgf_api_version(void);
char *mgf_recording_devices_json(void);
char *mgf_start_recording_json(mgf_engine_handle engine, const char *configuration);
void mgf_stop_recording(mgf_engine_handle engine);
char *mgf_set_monitoring(mgf_engine_handle engine, int32_t enabled);
mgf_engine_handle mgf_engine_create(void);
/* The returned descriptor is owned by the caller and remains valid across restarts. */
int32_t mgf_frame_events_fd(mgf_engine_handle engine);
void mgf_engine_destroy(mgf_engine_handle engine);
/* JSON and error strings belong to the caller and require mgf_string_free. */
char *mgf_video_devices_json(void);
char *mgf_video_modes_json(const char *path);
char *mgf_audio_devices_json(void);
char *mgf_start_video(mgf_engine_handle engine, const char *path, uint32_t fourcc,
                      uint32_t width, uint32_t height, uint32_t interval_numerator,
                      uint32_t interval_denominator, int32_t color_range);
/* Backend 0 prefers DMA-BUF, 1 uses CPU conversion, and 2 requests DMA-BUF with fallback. */
char *mgf_start_video_backend(mgf_engine_handle engine, const char *path, uint32_t fourcc,
                              uint32_t width, uint32_t height, uint32_t interval_numerator,
                              uint32_t interval_denominator, int32_t color_range,
                              int32_t backend, const char *fallback_reason);
void mgf_stop_video(mgf_engine_handle engine);
/* A successful acquire pins pixels until the token is released exactly once. */
int32_t mgf_acquire_frame(mgf_engine_handle engine, uint64_t after_sequence, mgf_frame_view *frame);
void mgf_release_frame(void *token);
/* DMA-BUF frame tokens retain capture storage independently of the engine. */
int32_t mgf_acquire_dma_frame(mgf_engine_handle engine, uint64_t after_sequence, mgfr_dma_frame_view *frame);
void mgf_release_dma_frame(void *token);
char *mgf_status_json(mgf_engine_handle engine);
/* Empty output selects the default sink. Empty source stops monitoring. */
char *mgf_start_audio(mgf_engine_handle engine, const char *source, const char *output);
void mgf_stop_audio(mgf_engine_handle engine);
void mgf_set_volume(mgf_engine_handle engine, float volume);
void mgf_string_free(char *string);
/* Mode selection and control policy belong to Swift. Returned strings use mgf_string_free. */
char *mgf_resolve_modes_json(const char *request);
/* Preset requests are bounded JSON. Responses use mgf_string_free. */
char *mgf_presets_json(const char *request);
char *mgf_activate_preset_json(mgf_engine_handle engine, const char *request);
char *mgf_apply_preset_json(mgf_engine_handle engine, const char *preset);
char *mgf_refresh_devices_json(mgf_engine_handle engine, const char *path, const char *source, const char *output);
void mgf_set_audio_controls(mgf_engine_handle engine, float volume, int32_t muted);
/* The UI retires displayed references first. Stale generations are ignored. */
char *mgf_report_gpu_failure(mgf_engine_handle engine, uint64_t generation, const char *reason);
#endif
