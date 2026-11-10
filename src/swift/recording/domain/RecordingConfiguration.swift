import Foundation
struct RecordingConfiguration: Codable, Sendable, Equatable {
    var gpuID = "auto"
    var codec = "h264"
    var bitrateMbps: Double = 40
    var rateControl = "vbr"
    var path = ""
    var recordAudio = true

    func validated() throws -> RecordingConfiguration {
        guard ["h264", "hevc", "av1"].contains(codec), ["vbr", "cbr"].contains(rateControl),
              bitrateMbps.isFinite, (0.1...1000).contains(bitrateMbps),
              gpuID.utf8.count <= 256, !gpuID.contains("\0"), path.utf8.count <= 4096, !path.contains("\0"),
              path.isEmpty || path.hasPrefix("/") else { throw CoreError.message("Invalid recording GPU, codec, bitrate, or output path.") }
        if !path.isEmpty && !["mkv", "mp4"].contains(URL(fileURLWithPath: path).pathExtension.lowercased()) {
            throw CoreError.message("Use an absolute .mkv or .mp4 recording output path.")
        }
        return self
    }

}

struct RecordingSnapshot: Codable, Sendable {
    var phase = "idle"
    var frames: UInt64 = 0
    var droppedFrames: UInt64 = 0
    var bytes: UInt64 = 0
    var durationNanoseconds: UInt64 = 0
    var error = ""
    var path: String?
    var gpu: String?
    var codec: String?
    var decoder: String?
    var transfer: String?
    var active: Bool { ["starting", "recording", "stopping"].contains(phase) }
}

