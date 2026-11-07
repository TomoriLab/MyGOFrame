import Glibc
import NativeCore

struct PixelConfiguration {
    var fourcc: UInt32
    var width: UInt32
    var height: UInt32
    var bytesPerLine: UInt32 = 0
    var quantization: UInt32 = 0
    var encoding: UInt32 = 0
    var colorspace: UInt32 = 0
    var colorRange: Int32 = 0
}

enum PixelConversionResult {
    case converted
    case discarded(String)
}

private struct ConverterStorage {
    let config: PixelConfiguration
    let format: AVPixelFormat
    var scaler: mgf_sws_context?
    var decoder: UnsafeMutablePointer<AVCodecContext>?
    var frame: UnsafeMutablePointer<AVFrame>?
    var packet: UnsafeMutablePointer<AVPacket>?
    var paddedInput: UnsafeMutablePointer<UInt8>?
    var paddedInputCapacity: Int = 0
}

struct PixelConverter: ~Copyable {
    private let storage: UnsafeMutablePointer<ConverterStorage>

    static func pixelFormat(_ fourcc: UInt32) -> AVPixelFormat {
        switch fourcc {
        case MGF_PIX_FMT_YUYV: return AV_PIX_FMT_YUYV422
        case MGF_PIX_FMT_UYVY: return AV_PIX_FMT_UYVY422
        case MGF_PIX_FMT_NV12: return AV_PIX_FMT_NV12
        case MGF_PIX_FMT_P010: return AV_PIX_FMT_P010LE
        case MGF_PIX_FMT_BGR24: return AV_PIX_FMT_BGR24
        case MGF_PIX_FMT_RGB24: return AV_PIX_FMT_RGB24
        default: return AV_PIX_FMT_NONE
        }
    }

    static func supports(_ fourcc: UInt32) -> Bool {
        fourcc == MGF_PIX_FMT_MJPEG || pixelFormat(fourcc) != AV_PIX_FMT_NONE
    }

    init(configuration: PixelConfiguration) throws {
        guard configuration.width > 0, configuration.height > 0,
              configuration.width <= 16384, configuration.height <= 16384,
              (0...2).contains(configuration.colorRange), Self.supports(configuration.fourcc) else {
            throw VideoError(description: "Invalid pixel conversion configuration.")
        }
        let state = UnsafeMutablePointer<ConverterStorage>.allocate(capacity: 1)
        state.initialize(to: ConverterStorage(config: configuration, format: Self.pixelFormat(configuration.fourcc)))
        var completed = false
        defer { if !completed { Self.destroy(state) } }
        if configuration.fourcc == MGF_PIX_FMT_MJPEG {
            guard let codec = avcodec_find_decoder(AV_CODEC_ID_MJPEG) else {
                throw VideoError(description: "The FFmpeg MJPEG decoder is unavailable.")
            }
            state.pointee.decoder = avcodec_alloc_context3(codec)
            state.pointee.frame = av_frame_alloc()
            state.pointee.packet = av_packet_alloc()
            guard let decoder = state.pointee.decoder, state.pointee.frame != nil, state.pointee.packet != nil else {
                throw VideoError(description: "Unable to allocate the MJPEG decoder.")
            }
            decoder.pointee.thread_count = 1
            decoder.pointee.flags |= AV_CODEC_FLAG_LOW_DELAY
            let result = avcodec_open2(decoder, codec, nil)
            guard result >= 0 else { throw VideoError(description: "Open MJPEG decoder: \(Self.errorText(result))") }
        }
        storage = state
        completed = true
    }

    private static func destroy(_ state: UnsafeMutablePointer<ConverterStorage>) {
        sws_freeContext(state.pointee.scaler)
        avcodec_free_context(&state.pointee.decoder)
        av_frame_free(&state.pointee.frame)
        av_packet_free(&state.pointee.packet)
        av_free(state.pointee.paddedInput)
        state.deinitialize(count: 1)
        state.deallocate()
    }

    deinit { Self.destroy(storage) }

    private static func errorText(_ code: Int32) -> String {
        var description = [CChar](repeating: 0, count: 128)
        av_strerror(code, &description, description.count)
        return description.withUnsafeBufferPointer { String(cString: $0.baseAddress!) }
    }

    private static func decoderFailure(_ operation: String, _ code: Int32) throws -> PixelConversionResult {
        let description = "\(operation): \(errorText(code))"
        if code == MGF_AVERROR_INVALIDDATA || code == -EAGAIN || code == MGF_AVERROR_EOF {
            return .discarded(description)
        }
        throw VideoError(description: description)
    }

    borrowing func convert(source: UnsafePointer<UInt8>, count: Int, sourceCapacity: Int,
                           destination: UnsafeMutablePointer<UInt8>, capacity: Int) throws -> PixelConversionResult {
        let config = storage.pointee.config
        let width = Int(config.width)
        let height = Int(config.height)
        guard capacity >= width * height * 4, count >= 0, sourceCapacity >= count else {
            throw VideoError(description: "RGBA destination is too small or the input length is invalid.")
        }
        var format = storage.pointee.format
        let isRGB = format == AV_PIX_FMT_RGB24 || format == AV_PIX_FMT_BGR24
        var fullRange = config.quantization == V4L2_QUANTIZATION_FULL_RANGE.rawValue
        var encoding = config.encoding != 0 ? config.encoding : mgf_default_ycbcr_encoding(config.colorspace)
        if config.quantization == 0 {
            fullRange = mgf_default_quantization(isRGB ? 1 : 0, config.colorspace, encoding) == V4L2_QUANTIZATION_FULL_RANGE.rawValue
        }
        var planes: (UnsafePointer<UInt8>?, UnsafePointer<UInt8>?, UnsafePointer<UInt8>?, UnsafePointer<UInt8>?) = (source, nil, nil, nil)
        var strides: (Int32, Int32, Int32, Int32) = (0, 0, 0, 0)
        if let decoder = storage.pointee.decoder, let frame = storage.pointee.frame, let packet = storage.pointee.packet {
            guard count <= Int(Int32.max) else { throw VideoError(description: "MJPEG packet length exceeds the decoder limit.") }
            guard count >= 2, source[0] == 0xff, source[1] == 0xd8 else {
                return .discarded("MJPEG packet is missing its start-of-image marker.")
            }
            av_packet_unref(packet)
            guard av_new_packet(packet, Int32(count)) >= 0 else { throw VideoError(description: "Unable to allocate an MJPEG packet.") }
            // FFmpeg allocates and clears its required compressed-packet padding.
            memcpy(packet.pointee.data, source, count)
            avcodec_flush_buffers(decoder)
            var result = avcodec_send_packet(decoder, packet)
            if result < 0 { return try Self.decoderFailure("Submit MJPEG packet", result) }
            result = avcodec_receive_frame(decoder, frame)
            if result < 0 { return try Self.decoderFailure("Decode MJPEG frame", result) }
            guard frame.pointee.width == width, frame.pointee.height == height else {
                throw VideoError(description: "MJPEG dimensions changed; restart capture.")
            }
            format = AVPixelFormat(rawValue: frame.pointee.format)
            planes = (frame.pointee.data.0.map { UnsafePointer($0) }, frame.pointee.data.1.map { UnsafePointer($0) },
                      frame.pointee.data.2.map { UnsafePointer($0) }, frame.pointee.data.3.map { UnsafePointer($0) })
            strides = (frame.pointee.linesize.0, frame.pointee.linesize.1, frame.pointee.linesize.2, frame.pointee.linesize.3)
            fullRange = frame.pointee.color_range != AVCOL_RANGE_MPEG
            // Normalized aliases keep libswscale from rebuilding its converter on each JPEG frame.
            switch format {
            case AV_PIX_FMT_YUVJ420P: format = AV_PIX_FMT_YUV420P
            case AV_PIX_FMT_YUVJ422P: format = AV_PIX_FMT_YUV422P
            case AV_PIX_FMT_YUVJ444P: format = AV_PIX_FMT_YUV444P
            case AV_PIX_FMT_YUVJ440P: format = AV_PIX_FMT_YUV440P
            case AV_PIX_FMT_YUVJ411P: format = AV_PIX_FMT_YUV411P
            default: break
            }
            if frame.pointee.colorspace == AVCOL_SPC_BT709 { encoding = V4L2_YCBCR_ENC_709.rawValue }
            else if frame.pointee.colorspace == AVCOL_SPC_BT2020_NCL { encoding = V4L2_YCBCR_ENC_BT2020.rawValue }
        } else {
            let planar = format == AV_PIX_FMT_NV12 || format == AV_PIX_FMT_P010LE
            let minimumStride = width * (isRGB ? 3 : format == AV_PIX_FMT_NV12 ? 1 : 2)
            guard config.bytesPerLine <= Int32.max else { throw VideoError(description: "Video stride exceeds the converter limit.") }
            let rowStride = config.bytesPerLine == 0 ? minimumStride : Int(config.bytesPerLine)
            guard rowStride >= minimumStride, !planar || ((width | height) & 1) == 0,
                  isRGB || width & 1 == 0 else {
                throw VideoError(description: "Invalid raw frame dimensions or stride.")
            }
            strides.0 = Int32(rowStride)
            let firstPlaneSize = rowStride * height
            let required = firstPlaneSize + (planar ? rowStride * (height / 2) : 0)
            guard count >= required else { return .discarded("Truncated raw video frame.") }
            var readableSource = source
            let padding = Int(AV_INPUT_BUFFER_PADDING_SIZE)
            if sourceCapacity - required < padding {
                let allocationSize = required + padding
                if storage.pointee.paddedInputCapacity < allocationSize {
                    guard let allocation = av_malloc(allocationSize) else {
                        throw VideoError(description: "Unable to allocate a padded raw video frame.")
                    }
                    av_free(storage.pointee.paddedInput)
                    storage.pointee.paddedInput = allocation.assumingMemoryBound(to: UInt8.self)
                    storage.pointee.paddedInputCapacity = allocationSize
                }
                guard let paddedInput = storage.pointee.paddedInput else {
                    throw VideoError(description: "The padded raw video frame is unavailable.")
                }
                // swscale can read beyond the final plane, so unmapped driver buffers need padded storage.
                memcpy(paddedInput, source, required)
                memset(paddedInput + required, 0, padding)
                readableSource = UnsafePointer(paddedInput)
            }
            planes.0 = readableSource
            if planar { planes.1 = readableSource + firstPlaneSize; strides.1 = strides.0 }
        }
        if config.colorRange == 1 { fullRange = false }
        else if config.colorRange == 2 { fullRange = true }
        var matrix = SWS_CS_ITU601
        switch encoding {
        case V4L2_YCBCR_ENC_709.rawValue, V4L2_YCBCR_ENC_XV709.rawValue: matrix = SWS_CS_ITU709
        case V4L2_YCBCR_ENC_BT2020.rawValue: matrix = SWS_CS_BT2020
        case V4L2_YCBCR_ENC_SMPTE240M.rawValue: matrix = SWS_CS_SMPTE240M
        case V4L2_YCBCR_ENC_BT2020_CONST_LUM.rawValue:
            throw VideoError(description: "BT.2020 constant-luminance conversion is unsupported.")
        default: break
        }
        storage.pointee.scaler = sws_getCachedContext(storage.pointee.scaler, Int32(width), Int32(height), format,
            Int32(width), Int32(height), AV_PIX_FMT_RGBA, MGF_SWS_FAST_BILINEAR, nil, nil, nil)
        guard let scaler = storage.pointee.scaler else { throw VideoError(description: "Unable to allocate the color converter.") }
        let coefficients = sws_getCoefficients(matrix)
        guard sws_setColorspaceDetails(scaler, coefficients, fullRange ? 1 : 0, coefficients, 1, 0, 1 << 16, 1 << 16) >= 0 else {
            throw VideoError(description: "Unable to configure color range conversion.")
        }
        var outputPlanes: (UnsafeMutablePointer<UInt8>?, UnsafeMutablePointer<UInt8>?, UnsafeMutablePointer<UInt8>?, UnsafeMutablePointer<UInt8>?) = (destination, nil, nil, nil)
        var outputStrides: (Int32, Int32, Int32, Int32) = (Int32(width * 4), 0, 0, 0)
        let converted = withUnsafePointer(to: &planes) { sourcePlanes in
            withUnsafePointer(to: &strides) { sourceStrides in
                withUnsafePointer(to: &outputPlanes) { destinationPlanes in
                    withUnsafePointer(to: &outputStrides) { destinationStrides in
                        sws_scale(scaler,
                            UnsafeRawPointer(sourcePlanes).assumingMemoryBound(to: UnsafePointer<UInt8>?.self),
                            UnsafeRawPointer(sourceStrides).assumingMemoryBound(to: Int32.self), 0, Int32(height),
                            UnsafeRawPointer(destinationPlanes).assumingMemoryBound(to: UnsafeMutablePointer<UInt8>?.self),
                            UnsafeRawPointer(destinationStrides).assumingMemoryBound(to: Int32.self))
                    }
                }
            }
        }
        guard converted == height else { throw VideoError(description: "Color conversion failed.") }
        // RGB-to-RGBA conversion bypasses libswscale's YUV limited-range expansion.
        if isRGB && !fullRange {
            for index in 0..<(width * height) {
                for channel in 0..<3 {
                    let sample = ((Int(destination[index * 4 + channel]) - 16) * 255 + 109) / 219
                    destination[index * 4 + channel] = UInt8(clamping: sample)
                }
            }
        }
        return .converted
    }
}
