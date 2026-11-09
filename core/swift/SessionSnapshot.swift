import Foundation

struct NegotiatedVideo: Codable, Sendable {
    var path = ""
    var backend = "stopped"
    var fallbackReason = ""
    var fourcc: UInt32 = 0
    var format = ""
    var width: UInt32 = 0
    var height: UInt32 = 0
    var intervalNumerator: UInt32 = 0
    var intervalDenominator: UInt32 = 0
    var colorRange: Int32 = 0
    var bytesPerLine: UInt32 = 0
    var quantization: UInt32 = 0
    var encoding: UInt32 = 0
    var colorspace: UInt32 = 0
    var transferFunction: UInt32 = 0
    var captureBuffers: UInt32 = 0
}

struct AudioSnapshot: Codable, Sendable {
    var running = false
    var source = ""
    var output = ""
    var captureState: Int32 = 0
    var playbackState: Int32 = 0
    var queuedFrames: UInt64 = 0
    var droppedFrames: UInt64 = 0
    var underrunFrames: UInt64 = 0
    var capturedFrames: UInt64 = 0
    var playedFrames: UInt64 = 0
    var requestedLatencyFrames: UInt32 = 0
    var sampleRate: UInt32 = 0
    var error = ""
}

enum SessionPhase: String, Codable, Sendable {
    case stopped, starting, running, stopping, failed
}

struct SessionSnapshot: Codable, Sendable {
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
