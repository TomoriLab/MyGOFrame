import Foundation
import Glibc
import NativeCore

struct VideoConfiguration {
    let path: String
    let fourcc: UInt32
    let width: UInt32
    let height: UInt32
    let intervalNumerator: UInt32
    let intervalDenominator: UInt32
    let colorRange: Int32
}

struct VideoFrameMetadata {
    let timestampNanoseconds: UInt64
    let droppedFrames: UInt64
}

struct VideoError: Error, CustomStringConvertible {
    let description: String
}

private func videoSystemError(_ operation: String) -> VideoError {
    VideoError(description: "\(operation): \(String(cString: strerror(errno)))")
}

private func videoIOControl<Value>(_ descriptor: Int32, _ request: UInt, _ value: inout Value) -> Int32 {
    withUnsafeMutablePointer(to: &value) { pointer in
        var result: Int32
        repeat { result = mgf_system_ioctl(descriptor, request, pointer) } while result < 0 && errno == EINTR
        return result
    }
}

private func checkedVideoIOControl<Value>(_ descriptor: Int32, _ request: UInt, _ value: inout Value, _ operation: String) throws {
    if videoIOControl(descriptor, request, &value) < 0 { throw videoSystemError(operation) }
}

private struct VideoFile: ~Copyable {
    let descriptor: Int32
    let name: String

    init(path: String) throws {
        let opened = path.withCString { open($0, O_RDWR | O_NONBLOCK | O_CLOEXEC) }
        guard opened >= 0 else { throw videoSystemError("Open capture device") }
        var completed = false
        defer { if !completed { close(opened) } }
        var capability = v4l2_capability()
        try checkedVideoIOControl(opened, MGF_VIDIOC_QUERYCAP, &capability, "Read capture capabilities")
        let flags = capability.capabilities & V4L2_CAP_DEVICE_CAPS != 0 ? capability.device_caps : capability.capabilities
        guard flags & UInt32(V4L2_CAP_VIDEO_CAPTURE) != 0, flags & UInt32(V4L2_CAP_STREAMING) != 0 else {
            throw VideoError(description: "The device does not support single-planar streaming video capture.")
        }
        descriptor = opened
        name = withUnsafeBytes(of: capability.card) { bytes in
            String(decoding: bytes.prefix { $0 != 0 }, as: UTF8.self)
        }
        completed = true
    }

    deinit { close(descriptor) }
}

private struct MappedVideoBuffer {
    let address: UnsafeMutableRawPointer
    let length: Int
}

private struct VideoReadState {
    var droppedFrames: UInt64 = 0
    var consecutiveInvalidFrames = 0
}

struct VideoCapture: ~Copyable {
    private let file: VideoFile
    private let buffers: [MappedVideoBuffer]
    private let converter: PixelConverter
    private let readState: UnsafeMutablePointer<VideoReadState>
    let width: Int
    let height: Int
    let stride: Int
    let details: [String: Any]

    init(configuration: VideoConfiguration) throws {
        guard configuration.width > 0, configuration.height > 0,
              configuration.width <= 16384, configuration.height <= 16384,
              configuration.intervalNumerator > 0, configuration.intervalDenominator > 0,
              (0...2).contains(configuration.colorRange), PixelConverter.supports(configuration.fourcc) else {
            throw VideoError(description: "Invalid capture configuration.")
        }
        let opened = try VideoFile(path: configuration.path)
        var format = v4l2_format()
        format.type = V4L2_BUF_TYPE_VIDEO_CAPTURE.rawValue
        format.fmt.pix.width = configuration.width
        format.fmt.pix.height = configuration.height
        format.fmt.pix.pixelformat = configuration.fourcc
        format.fmt.pix.field = V4L2_FIELD_NONE.rawValue
        try checkedVideoIOControl(opened.descriptor, MGF_VIDIOC_S_FMT, &format, "Set video format")
        guard format.fmt.pix.field == V4L2_FIELD_NONE.rawValue else {
            throw VideoError(description: "Interlaced capture is unsupported; select a progressive input mode.")
        }
        var parameters = v4l2_streamparm()
        parameters.type = V4L2_BUF_TYPE_VIDEO_CAPTURE.rawValue
        try checkedVideoIOControl(opened.descriptor, MGF_VIDIOC_G_PARM, &parameters, "Read frame interval")
        if parameters.parm.capture.capability & UInt32(V4L2_CAP_TIMEPERFRAME) != 0 {
            parameters.parm.capture.timeperframe.numerator = configuration.intervalNumerator
            parameters.parm.capture.timeperframe.denominator = configuration.intervalDenominator
            try checkedVideoIOControl(opened.descriptor, MGF_VIDIOC_S_PARM, &parameters, "Set frame interval")
        }
        guard parameters.parm.capture.timeperframe.numerator > 0, parameters.parm.capture.timeperframe.denominator > 0 else {
            throw VideoError(description: "The capture device did not report a valid frame interval.")
        }
        let pixels = format.fmt.pix
        let pixelConverter = try PixelConverter(configuration: PixelConfiguration(
            fourcc: pixels.pixelformat, width: pixels.width, height: pixels.height,
            bytesPerLine: pixels.bytesperline, quantization: pixels.quantization,
            encoding: pixels.ycbcr_enc, colorspace: pixels.colorspace, colorRange: configuration.colorRange))
        var request = v4l2_requestbuffers()
        request.count = 3
        request.type = V4L2_BUF_TYPE_VIDEO_CAPTURE.rawValue
        request.memory = V4L2_MEMORY_MMAP.rawValue
        try checkedVideoIOControl(opened.descriptor, MGF_VIDIOC_REQBUFS, &request, "Allocate capture buffers")
        guard (2...64).contains(request.count) else {
            throw VideoError(description: "Unsupported capture buffer count: \(request.count)")
        }
        var mappings: [MappedVideoBuffer] = []
        var completed = false
        defer { if !completed { for mapping in mappings { munmap(mapping.address, mapping.length) } } }
        for index in 0..<request.count {
            var buffer = Self.buffer(index: index)
            try checkedVideoIOControl(opened.descriptor, MGF_VIDIOC_QUERYBUF, &buffer, "Query capture buffer")
            let mapped = mmap(nil, Int(buffer.length), PROT_READ | PROT_WRITE, MAP_SHARED, opened.descriptor, Int(buffer.m.offset))
            guard mapped != MAP_FAILED, let mapped else { throw videoSystemError("Map capture buffer") }
            mappings.append(MappedVideoBuffer(address: mapped, length: Int(buffer.length)))
            try checkedVideoIOControl(opened.descriptor, MGF_VIDIOC_QBUF, &buffer, "Queue capture buffer")
        }
        var type = V4L2_BUF_TYPE_VIDEO_CAPTURE.rawValue
        try checkedVideoIOControl(opened.descriptor, MGF_VIDIOC_STREAMON, &type, "Start capture")
        width = Int(pixels.width)
        height = Int(pixels.height)
        stride = Int(pixels.width) * 4
        details = [
            "path": configuration.path, "fourcc": pixels.pixelformat,
            "format": VideoDiscovery.formatName(pixels.pixelformat),
            "width": pixels.width, "height": pixels.height,
            "intervalNumerator": parameters.parm.capture.timeperframe.numerator,
            "intervalDenominator": parameters.parm.capture.timeperframe.denominator,
            "colorRange": configuration.colorRange, "bytesPerLine": pixels.bytesperline,
            "quantization": pixels.quantization, "encoding": pixels.ycbcr_enc,
            "colorspace": pixels.colorspace, "transferFunction": pixels.xfer_func,
            "captureBuffers": request.count
        ]
        readState = .allocate(capacity: 1)
        readState.initialize(to: VideoReadState())
        file = opened
        buffers = mappings
        converter = pixelConverter
        completed = true
    }

    deinit {
        var type = V4L2_BUF_TYPE_VIDEO_CAPTURE.rawValue
        _ = videoIOControl(file.descriptor, MGF_VIDIOC_STREAMOFF, &type)
        for mapping in buffers { munmap(mapping.address, mapping.length) }
        readState.deinitialize(count: 1)
        readState.deallocate()
    }

    private static func buffer(index: UInt32 = 0) -> v4l2_buffer {
        var buffer = v4l2_buffer()
        buffer.index = index
        buffer.type = V4L2_BUF_TYPE_VIDEO_CAPTURE.rawValue
        buffer.memory = V4L2_MEMORY_MMAP.rawValue
        return buffer
    }

    private borrowing func requeue(_ index: UInt32) throws {
        var buffer = Self.buffer(index: index)
        try checkedVideoIOControl(file.descriptor, MGF_VIDIOC_QBUF, &buffer, "Queue capture buffer")
    }

    borrowing func read(into destination: UnsafeMutablePointer<UInt8>, capacity: Int) throws -> VideoFrameMetadata? {
        var descriptor = pollfd(fd: file.descriptor, events: Int16(POLLIN), revents: 0)
        let ready = poll(&descriptor, 1, 50)
        if ready < 0 {
            if errno == EINTR { return nil }
            throw videoSystemError("Wait for video frame")
        }
        guard descriptor.revents & Int16(POLLHUP | POLLNVAL | POLLERR) == 0 else {
            throw VideoError(description: "Capture device disconnected or reported a stream error.")
        }
        guard ready > 0, descriptor.revents & Int16(POLLIN) != 0 else { return nil }
        var latest: v4l2_buffer?
        defer { if let latest { try? requeue(latest.index) } }
        // A bounded drain cannot be monopolized by a faster capture producer.
        for _ in buffers.indices {
            var buffer = Self.buffer()
            if videoIOControl(file.descriptor, MGF_VIDIOC_DQBUF, &buffer) < 0 {
                if errno == EAGAIN { break }
                throw videoSystemError("Dequeue capture buffer")
            }
            guard buffer.index < buffers.count, buffer.bytesused <= buffers[Int(buffer.index)].length else {
                throw VideoError(description: "The driver returned invalid capture buffer bounds.")
            }
            if buffer.flags & UInt32(V4L2_BUF_FLAG_ERROR) != 0 {
                try requeue(buffer.index)
                readState.pointee.droppedFrames += 1
                continue
            }
            let previous = latest
            latest = buffer
            if let previous {
                try requeue(previous.index)
                readState.pointee.droppedFrames += 1
            }
        }
        guard let buffer = latest else { return nil }
        let mapping = buffers[Int(buffer.index)]
        let result = try converter.convert(source: UnsafeRawPointer(mapping.address).assumingMemoryBound(to: UInt8.self),
                                           count: Int(buffer.bytesused), sourceCapacity: mapping.length,
                                           destination: destination, capacity: capacity)
        try requeue(buffer.index)
        latest = nil
        switch result {
        case .discarded(let reason):
            readState.pointee.droppedFrames += 1
            readState.pointee.consecutiveInvalidFrames += 1
            if readState.pointee.consecutiveInvalidFrames >= 120 {
                throw VideoError(description: "Capture returned 120 consecutive invalid frames: \(reason)")
            }
            return nil
        case .converted:
            let dropped = readState.pointee.droppedFrames
            readState.pointee.droppedFrames = 0
            readState.pointee.consecutiveInvalidFrames = 0
            var timestamp: UInt64
            if buffer.flags & UInt32(V4L2_BUF_FLAG_TIMESTAMP_MASK) == UInt32(V4L2_BUF_FLAG_TIMESTAMP_MONOTONIC),
               buffer.timestamp.tv_sec >= 0, (0..<1_000_000).contains(buffer.timestamp.tv_usec) {
                timestamp = UInt64(buffer.timestamp.tv_sec) * 1_000_000_000 + UInt64(buffer.timestamp.tv_usec) * 1000
            } else {
                var current = timespec()
                clock_gettime(CLOCK_MONOTONIC, &current)
                timestamp = UInt64(current.tv_sec) * 1_000_000_000 + UInt64(current.tv_nsec)
            }
            return VideoFrameMetadata(timestampNanoseconds: timestamp, droppedFrames: dropped)
        }
    }
}

enum VideoDiscovery {
    static func formatName(_ fourcc: UInt32) -> String {
        String(bytes: (0..<4).map { UInt8((fourcc >> ($0 * 8)) & 255) }, encoding: .ascii) ?? "Unknown"
    }

    static func devices() throws -> [[String: Any]] {
        let names = try FileManager.default.contentsOfDirectory(atPath: "/dev")
            .filter { $0.hasPrefix("video") && Int($0.dropFirst(5)) != nil }
            .sorted { $0.localizedStandardCompare($1) == .orderedAscending }
        var devices: [[String: Any]] = []
        for name in names {
            let path = "/dev/\(name)"
            do {
                let file = try VideoFile(path: path)
                devices.append(["path": path, "name": file.name])
            } catch { continue }
        }
        return devices
    }

    static func modes(path: String) throws -> [[String: Any]] {
        let file = try VideoFile(path: path)
        var modes: [[String: Any]] = []
        for formatIndex: UInt32 in 0..<1024 {
            var format = v4l2_fmtdesc()
            format.index = formatIndex
            format.type = V4L2_BUF_TYPE_VIDEO_CAPTURE.rawValue
            if videoIOControl(file.descriptor, MGF_VIDIOC_ENUM_FMT, &format) < 0 {
                if errno == EINVAL { break }
                throw videoSystemError("Enumerate video formats")
            }
            guard PixelConverter.supports(format.pixelformat) else { continue }
            for sizeIndex: UInt32 in 0..<4096 {
                var size = v4l2_frmsizeenum()
                size.index = sizeIndex
                size.pixel_format = format.pixelformat
                if videoIOControl(file.descriptor, MGF_VIDIOC_ENUM_FRAMESIZES, &size) < 0 {
                    if errno == EINVAL { break }
                    throw videoSystemError("Enumerate video sizes")
                }
                for (width, height) in candidateSizes(size) {
                    for intervalIndex: UInt32 in 0..<4096 {
                        var interval = v4l2_frmivalenum()
                        interval.index = intervalIndex
                        interval.pixel_format = format.pixelformat
                        interval.width = width
                        interval.height = height
                        if videoIOControl(file.descriptor, MGF_VIDIOC_ENUM_FRAMEINTERVALS, &interval) < 0 {
                            if errno == EINVAL { break }
                            throw videoSystemError("Enumerate frame intervals")
                        }
                        for (numerator, denominator) in candidateIntervals(interval) {
                            modes.append([
                                "fourcc": format.pixelformat, "format": formatName(format.pixelformat),
                                "width": width, "height": height,
                                "intervalNumerator": numerator, "intervalDenominator": denominator
                            ])
                        }
                        if interval.type != V4L2_FRMIVAL_TYPE_DISCRETE.rawValue { break }
                    }
                }
                if size.type != V4L2_FRMSIZE_TYPE_DISCRETE.rawValue { break }
            }
        }
        return modes
    }
    private static func candidateSizes(_ size: v4l2_frmsizeenum) -> [(UInt32, UInt32)] {
        if size.type == 1 { return [(size.discrete.width, size.discrete.height)] }
        let common: [(UInt32, UInt32)] = [
            (640, 480), (720, 480), (720, 576), (1280, 720), (1280, 800),
            (1920, 1080), (1920, 1200), (2560, 1080), (2560, 1440),
            (3440, 1440), (3840, 2160), (7680, 4320),
            (size.stepwise.min_width, size.stepwise.min_height), (size.stepwise.max_width, size.stepwise.max_height)
        ]
        var seen = Set<String>()
        return common.filter { width, height in
            guard width > 0, height > 0, width <= 16384, height <= 16384,
                width >= size.stepwise.min_width, width <= size.stepwise.max_width,
                height >= size.stepwise.min_height, height <= size.stepwise.max_height else { return false }
            if size.type == 3 {
                guard (width - size.stepwise.min_width) % max(1, size.stepwise.step_width) == 0,
                    (height - size.stepwise.min_height) % max(1, size.stepwise.step_height) == 0 else { return false }
            }
            return seen.insert("\(width)x\(height)").inserted
        }
    }

    private static func candidateIntervals(_ interval: v4l2_frmivalenum) -> [(UInt32, UInt32)] {
        if interval.type == 1 {
            return interval.discrete.numerator > 0 && interval.discrete.denominator > 0 ? [(interval.discrete.numerator, interval.discrete.denominator)] : []
        }
        guard interval.stepwise.min.denominator > 0, interval.stepwise.max.denominator > 0 else { return [] }
        let minimum = Double(interval.stepwise.min.numerator) / Double(interval.stepwise.min.denominator)
        let maximum = Double(interval.stepwise.max.numerator) / Double(interval.stepwise.max.denominator)
        let step = interval.stepwise.step.denominator > 0 ? Double(interval.stepwise.step.numerator) / Double(interval.stepwise.step.denominator) : 0
        let common: [(UInt32, UInt32)] = [
            (1, 240), (1, 144), (1, 120), (1, 100), (1, 60), (1001, 60000),
            (1, 50), (1, 30), (1001, 30000), (1, 25), (1, 24), (1001, 24000),
            (interval.stepwise.min.numerator, interval.stepwise.min.denominator), (interval.stepwise.max.numerator, interval.stepwise.max.denominator)
        ]
        var seen: [Double] = []
        return common.filter { numerator, denominator in
            guard numerator > 0, denominator > 0 else { return false }
            let value = Double(numerator) / Double(denominator)
            guard value >= minimum - 1e-10, value <= maximum + 1e-10 else { return false }
            if interval.type == 3 && step > 0 {
                let steps = (value - minimum) / step
                guard abs(steps - steps.rounded()) < 1e-7 else { return false }
            }
            guard !seen.contains(where: { abs($0 - value) < 1e-10 }) else { return false }
            seen.append(value)
            return true
        }
    }
}
