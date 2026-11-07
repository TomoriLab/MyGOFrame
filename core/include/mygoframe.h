#ifndef MYGOFRAME_H
#define MYGOFRAME_H
#include <stdint.h>
#include <stddef.h>

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

/* Calls on one engine must be serialized by the caller. Frame release is independent. */
void *mgf_engine_create(void);
void mgf_engine_destroy(void *engine);
/* JSON and error strings belong to the caller and require mgf_string_free. */
char *mgf_video_devices_json(void);
char *mgf_video_modes_json(const char *path);
char *mgf_audio_devices_json(void);
char *mgf_start_video(void *engine, const char *path, uint32_t fourcc,
                      uint32_t width, uint32_t height, uint32_t interval_numerator,
                      uint32_t interval_denominator, int32_t color_range);
void mgf_stop_video(void *engine);
/* A successful acquire pins pixels until the token is released exactly once. */
int32_t mgf_acquire_frame(void *engine, uint64_t after_sequence, mgf_frame_view *frame);
void mgf_release_frame(void *token);
char *mgf_status_json(void *engine);
/* Empty output selects the default sink. Empty source stops monitoring. */
char *mgf_start_audio(void *engine, const char *source, const char *output);
void mgf_stop_audio(void *engine);
void mgf_set_volume(void *engine, float volume);
void mgf_string_free(char *string);
#endif
