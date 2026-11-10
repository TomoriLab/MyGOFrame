#ifndef MYGOFRAME_VIDEO_H
#define MYGOFRAME_VIDEO_H
#include <stdint.h>
#include <time.h>
#include <linux/videodev2.h>
#include <sys/ioctl.h>
#include <libavcodec/avcodec.h>
#include <libavutil/error.h>
#include <libavutil/pixfmt.h>
#include <libswscale/swscale.h>

typedef struct SwsContext *mgf_sws_context;
static const int MGF_SWS_FAST_BILINEAR = SWS_FAST_BILINEAR;

/* These aliases expose system macros that the Swift Clang importer cannot represent. */
static const unsigned long MGF_VIDIOC_QUERYCAP = VIDIOC_QUERYCAP;
static const unsigned long MGF_VIDIOC_ENUM_FMT = VIDIOC_ENUM_FMT;
static const unsigned long MGF_VIDIOC_ENUM_FRAMESIZES = VIDIOC_ENUM_FRAMESIZES;
static const unsigned long MGF_VIDIOC_ENUM_FRAMEINTERVALS = VIDIOC_ENUM_FRAMEINTERVALS;
static const unsigned long MGF_VIDIOC_S_FMT = VIDIOC_S_FMT;
static const unsigned long MGF_VIDIOC_G_PARM = VIDIOC_G_PARM;
static const unsigned long MGF_VIDIOC_S_PARM = VIDIOC_S_PARM;
static const unsigned long MGF_VIDIOC_REQBUFS = VIDIOC_REQBUFS;
static const unsigned long MGF_VIDIOC_QUERYBUF = VIDIOC_QUERYBUF;
static const unsigned long MGF_VIDIOC_QBUF = VIDIOC_QBUF;
static const unsigned long MGF_VIDIOC_DQBUF = VIDIOC_DQBUF;
static const unsigned long MGF_VIDIOC_STREAMON = VIDIOC_STREAMON;
static const unsigned long MGF_VIDIOC_STREAMOFF = VIDIOC_STREAMOFF;
static const uint32_t MGF_PIX_FMT_YUYV = V4L2_PIX_FMT_YUYV;
static const uint32_t MGF_PIX_FMT_UYVY = V4L2_PIX_FMT_UYVY;
static const uint32_t MGF_PIX_FMT_NV12 = V4L2_PIX_FMT_NV12;
static const uint32_t MGF_PIX_FMT_P010 = V4L2_PIX_FMT_P010;
static const uint32_t MGF_PIX_FMT_BGR24 = V4L2_PIX_FMT_BGR24;
static const uint32_t MGF_PIX_FMT_RGB24 = V4L2_PIX_FMT_RGB24;
static const uint32_t MGF_PIX_FMT_MJPEG = V4L2_PIX_FMT_MJPEG;
static const int MGF_AVERROR_INVALIDDATA = AVERROR_INVALIDDATA;
static const int MGF_AVERROR_EOF = AVERROR_EOF;

/* Swift cannot call C variadic functions or function-like macros directly. */
static inline int mgf_system_ioctl(int descriptor, unsigned long request, void *argument) {
    return ioctl(descriptor, request, argument);
}
static inline uint32_t mgf_default_ycbcr_encoding(uint32_t colorspace) {
    return V4L2_MAP_YCBCR_ENC_DEFAULT(colorspace);
}
static inline uint32_t mgf_default_quantization(int is_rgb, uint32_t colorspace, uint32_t encoding) {
    (void)encoding;
    return V4L2_MAP_QUANTIZATION_DEFAULT(is_rgb, colorspace, encoding);
}
#endif
