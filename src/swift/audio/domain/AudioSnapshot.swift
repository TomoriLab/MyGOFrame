import Foundation
struct AudioSnapshot: Codable, Sendable {
    var inputPeak: Float = 0
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

