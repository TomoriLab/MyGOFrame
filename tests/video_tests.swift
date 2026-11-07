import Glibc
import NativeCore

private func requireConverted(_ result: PixelConversionResult) {
    if case .discarded(let reason) = result { fatalError("Unexpected discarded frame: \(reason)") }
}

private func requireDiscarded(_ result: PixelConversionResult) {
    if case .converted = result { fatalError("An invalid frame was accepted.") }
}

private func checkGray(_ bytes: [UInt8], expected: Int, tolerance: Int) {
    for pixel in stride(from: 0, to: bytes.count, by: 4) {
        for channel in 0..<3 { precondition(abs(Int(bytes[pixel + channel]) - expected) <= tolerance) }
        precondition(bytes[pixel + 3] == 255)
    }
}

private func bytes(_ words: [UInt16]) -> [UInt8] {
    words.flatMap { [UInt8(truncatingIfNeeded: $0), UInt8(truncatingIfNeeded: $0 >> 8)] }
}

private func convert(_ converter: borrowing PixelConverter, source: [UInt8], output: inout [UInt8], count: Int? = nil,
                     capacity: Int? = nil) throws -> PixelConversionResult {
    let sourceLength = count ?? source.count
    let destinationCapacity = capacity ?? output.count
    return try source.withUnsafeBufferPointer { input in
        try output.withUnsafeMutableBufferPointer { destination in
            try converter.convert(source: input.baseAddress!, count: sourceLength,
                                  sourceCapacity: source.count,
                                  destination: destination.baseAddress!, capacity: destinationCapacity)
        }
    }
}

private func checkRaw(format: UInt32, rowStride: UInt32, source: [UInt8]) throws {
    let converter = try PixelConverter(configuration: PixelConfiguration(fourcc: format, width: 4, height: 2,
                                                                        bytesPerLine: rowStride, colorRange: 1))
    var output = [UInt8](repeating: 0, count: 32)
    requireConverted(try convert(converter, source: source, output: &output))
    checkGray(output, expected: 0, tolerance: 2)
    requireDiscarded(try convert(converter, source: source, output: &output, count: source.count - 1))
    requireConverted(try convert(converter, source: source, output: &output))
    do {
        _ = try convert(converter, source: source, output: &output, capacity: 31)
        fatalError("A short destination was accepted.")
    } catch {}
}

private func testRawFormats() throws {
    let yuyv: [UInt8] = [16,128,16,128,16,128,16,128,0,0,0,0,16,128,16,128,16,128,16,128,0,0,0,0]
    let uyvy: [UInt8] = [128,16,128,16,128,16,128,16,128,16,128,16,128,16,128,16]
    let nv12: [UInt8] = [16,16,16,16,16,16,16,16,128,128,128,128]
    let p010 = bytes([UInt16](repeating: 64 << 6, count: 8) + [UInt16](repeating: 512 << 6, count: 4))
    let rgb = [UInt8](repeating: 16, count: 24)
    try checkRaw(format: MGF_PIX_FMT_YUYV, rowStride: 12, source: yuyv)
    try checkRaw(format: MGF_PIX_FMT_UYVY, rowStride: 8, source: uyvy)
    try checkRaw(format: MGF_PIX_FMT_NV12, rowStride: 4, source: nv12)
    try checkRaw(format: MGF_PIX_FMT_P010, rowStride: 8, source: p010)
    try checkRaw(format: MGF_PIX_FMT_RGB24, rowStride: 12, source: rgb)
    try checkRaw(format: MGF_PIX_FMT_BGR24, rowStride: 12, source: rgb)
    var output = [UInt8](repeating: 0, count: 32)
    let fullRange = try PixelConverter(configuration: PixelConfiguration(fourcc: MGF_PIX_FMT_YUYV, width: 4, height: 2,
                                                                        bytesPerLine: 12, colorRange: 2))
    requireConverted(try convert(fullRange, source: yuyv, output: &output))
    checkGray(output, expected: 16, tolerance: 2)
    for (width, rowStride): (UInt32, UInt32) in [(4, 1), (3, 8)] {
        let invalid = try PixelConverter(configuration: PixelConfiguration(fourcc: MGF_PIX_FMT_YUYV, width: width, height: 2,
                                                                          bytesPerLine: rowStride))
        do {
            _ = try convert(invalid, source: yuyv, output: &output)
            fatalError("Invalid dimensions or stride were accepted.")
        } catch {}
    }
}

private func testColorChannels() throws {
    let yuyv: [UInt8] = [81,90,81,240,81,90,81,240,81,90,81,240,81,90,81,240]
    let uyvy: [UInt8] = [90,81,240,81,90,81,240,81,90,81,240,81,90,81,240,81]
    let nv12: [UInt8] = [81,81,81,81,81,81,81,81,90,240,90,240]
    let p010 = bytes([UInt16](repeating: 324 << 6, count: 8) + [360 << 6,960 << 6,360 << 6,960 << 6])
    let rgb: [UInt8] = Array(repeating: [UInt8(255),0,0], count: 8).flatMap { $0 }
    let bgr: [UInt8] = Array(repeating: [UInt8(0),0,255], count: 8).flatMap { $0 }
    let inputs: [(UInt32, [UInt8], Int32)] = [(MGF_PIX_FMT_YUYV,yuyv,1),(MGF_PIX_FMT_UYVY,uyvy,1),
        (MGF_PIX_FMT_NV12,nv12,1),(MGF_PIX_FMT_P010,p010,1),(MGF_PIX_FMT_RGB24,rgb,2),(MGF_PIX_FMT_BGR24,bgr,2)]
    for (format, source, range) in inputs {
        let converter = try PixelConverter(configuration: PixelConfiguration(fourcc: format, width: 4, height: 2,
            encoding: V4L2_YCBCR_ENC_601.rawValue, colorRange: range))
        var output = [UInt8](repeating: 0, count: 32)
        requireConverted(try convert(converter, source: source, output: &output))
        for pixel in 0..<8 {
            precondition(output[pixel * 4] >= 250)
            precondition(output[pixel * 4 + 1] <= 4)
            precondition(output[pixel * 4 + 2] <= 4)
            precondition(output[pixel * 4 + 3] == 255)
        }
    }
}

private func testRGBRangeOverrides() throws {
    for format in [MGF_PIX_FMT_RGB24, MGF_PIX_FMT_BGR24] {
        var output = [UInt8](repeating: 0, count: 32)
        let limited = try PixelConverter(configuration: PixelConfiguration(fourcc: format, width: 4, height: 2, colorRange: 1))
        requireConverted(try convert(limited, source: [UInt8](repeating: 235, count: 24), output: &output))
        checkGray(output, expected: 255, tolerance: 0)
        requireConverted(try convert(limited, source: [UInt8](repeating: 16, count: 24), output: &output))
        checkGray(output, expected: 0, tolerance: 0)
        let full = try PixelConverter(configuration: PixelConfiguration(fourcc: format, width: 4, height: 2, colorRange: 2))
        requireConverted(try convert(full, source: [UInt8](repeating: 16, count: 24), output: &output))
        checkGray(output, expected: 16, tolerance: 0)
    }
}

private struct GuardedPixels: ~Copyable {
    let mapping: UnsafeMutableRawPointer
    let mappingLength: Int
    let pixels: UnsafeMutablePointer<UInt8>

    init(length: Int, readableTail: Int) {
        let pageSize = Int(sysconf(Int32(_SC_PAGESIZE)))
        let readableLength = ((length + readableTail + pageSize - 1) / pageSize) * pageSize
        mappingLength = readableLength + pageSize
        guard let address = mmap(nil, mappingLength, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0),
              address != MAP_FAILED, mprotect(address + readableLength, pageSize, PROT_NONE) == 0 else {
            fatalError("Cannot allocate guarded video test storage.")
        }
        mapping = address
        pixels = (address + readableLength - length - readableTail).assumingMemoryBound(to: UInt8.self)
        memset(pixels, 128, length + readableTail)
    }

    deinit { _ = munmap(mapping, mappingLength) }
}

private func testRawBoundaryPadding() throws {
    let width = 640
    let height = 480
    for (format, bytesPerPixel, planar) in [
        (MGF_PIX_FMT_YUYV, 2, false), (MGF_PIX_FMT_UYVY, 2, false),
        (MGF_PIX_FMT_NV12, 1, true), (MGF_PIX_FMT_P010, 2, true),
        (MGF_PIX_FMT_BGR24, 3, false), (MGF_PIX_FMT_RGB24, 3, false)
    ] {
        let rowStride = width * bytesPerPixel
        let sourceLength = rowStride * height + (planar ? rowStride * height / 2 : 0)
        let outputLength = width * height * 4
        let converter = try PixelConverter(configuration: PixelConfiguration(
            fourcc: format, width: UInt32(width), height: UInt32(height), bytesPerLine: UInt32(rowStride)))
        let tightSource = GuardedPixels(length: sourceLength, readableTail: 0)
        let safeOutput = GuardedPixels(length: outputLength, readableTail: 128)
        requireConverted(try converter.convert(source: UnsafePointer(tightSource.pixels), count: sourceLength,
                                               sourceCapacity: sourceLength, destination: safeOutput.pixels,
                                               capacity: outputLength))
        let paddedSource = GuardedPixels(length: sourceLength, readableTail: 128)
        let tightOutput = GuardedPixels(length: outputLength, readableTail: 0)
        requireConverted(try converter.convert(source: UnsafePointer(paddedSource.pixels), count: sourceLength,
                                               sourceCapacity: sourceLength + 128, destination: tightOutput.pixels,
                                               capacity: outputLength))
        precondition(memcmp(safeOutput.pixels, tightOutput.pixels, outputLength) == 0,
                     "Padded raw input changed the converted frame.")
    }
}

private func jpegFixture() -> [UInt8] {
    let encoder = avcodec_find_encoder(AV_CODEC_ID_MJPEG)!
    var context = avcodec_alloc_context3(encoder)
    var frame = av_frame_alloc()
    var packet = av_packet_alloc()
    precondition(context != nil && frame != nil && packet != nil)
    defer { av_frame_free(&frame); av_packet_free(&packet); avcodec_free_context(&context) }
    context!.pointee.width = 32
    context!.pointee.height = 16
    context!.pointee.pix_fmt = AV_PIX_FMT_YUVJ420P
    context!.pointee.time_base = AVRational(num: 1, den: 60)
    context!.pointee.color_range = AVCOL_RANGE_JPEG
    precondition(avcodec_open2(context, encoder, nil) == 0)
    frame!.pointee.width = 32
    frame!.pointee.height = 16
    frame!.pointee.format = AV_PIX_FMT_YUVJ420P.rawValue
    precondition(av_frame_get_buffer(frame, 32) == 0)
    for row in 0..<16 { memset(frame!.pointee.data.0! + row * Int(frame!.pointee.linesize.0), 128, 32) }
    for row in 0..<8 {
        memset(frame!.pointee.data.1! + row * Int(frame!.pointee.linesize.1), 128, 16)
        memset(frame!.pointee.data.2! + row * Int(frame!.pointee.linesize.2), 128, 16)
    }
    precondition(avcodec_send_frame(context, frame) == 0)
    precondition(avcodec_receive_packet(context, packet) == 0)
    return Array(UnsafeBufferPointer(start: packet!.pointee.data!, count: Int(packet!.pointee.size)))
}

private func testMJPEG() throws {
    let jpeg = jpegFixture()
    var output = [UInt8](repeating: 0, count: 32 * 16 * 4)
    let converter = try PixelConverter(configuration: PixelConfiguration(fourcc: MGF_PIX_FMT_MJPEG, width: 32, height: 16))
    requireConverted(try convert(converter, source: jpeg, output: &output))
    checkGray(output, expected: 128, tolerance: 3)
    requireDiscarded(try convert(converter, source: [1,2,3,4], output: &output))
    requireDiscarded(try convert(converter, source: [1], output: &output, count: 0))
    requireDiscarded(try convert(converter, source: [255,216,255,217], output: &output))
    requireConverted(try convert(converter, source: jpeg, output: &output))
    checkGray(output, expected: 128, tolerance: 3)
    let mismatch = try PixelConverter(configuration: PixelConfiguration(fourcc: MGF_PIX_FMT_MJPEG, width: 16, height: 16))
    do {
        _ = try convert(mismatch, source: jpeg, output: &output)
        fatalError("Changed JPEG dimensions were accepted.")
    } catch {}
}

@main
struct VideoTests {
    static func main() throws {
        try testRawFormats()
        try testRawBoundaryPadding()
        try testColorChannels()
        try testRGBRangeOverrides()
        try testMJPEG()
        precondition(!PixelConverter.supports(0x34363248))
        do {
            _ = try PixelConverter(configuration: PixelConfiguration(fourcc: MGF_PIX_FMT_NV12, width: 0, height: 2))
            fatalError("Invalid converter dimensions were accepted.")
        } catch {}
        print("Swift video tests passed: formats, channels, ranges, bounds, JPEG decoding and recovery.")
    }
}
