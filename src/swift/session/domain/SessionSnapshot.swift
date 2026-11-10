import Foundation
enum SessionPhase: String, Codable, Sendable {
    case stopped, starting, running, stopping, failed
}

struct SessionSnapshot: Codable, Sendable {
    var recording = RecordingSnapshot()
    var monitorEnabled = true
    var schemaVersion: UInt32 = 1
    var generation: UInt64
    var commandID: UInt64
    var videoPhase: SessionPhase
    var audioPhase: SessionPhase
    var videoRunning: Bool
    var error: String
    var frames: UInt64
    var droppedFrames: UInt64
    var video: NegotiatedVideo
    var requestedVideo: VideoConfiguration?
    var videoBackend: String
    var fallbackReason: String
    var audio: AudioSnapshot
    var volume: Float
    var muted: Bool
    var effectiveGain: Float
}
