struct VideoProgress {
    var running: Bool
    var frames: UInt64
    var droppedFrames: UInt64
    var error: String
}

protocol FrameNotification: AnyObject {
    func duplicateEvents() -> Int32
}

protocol VideoSessionPort: AnyObject {
    var usesDMA: Bool { get }
    var details: NegotiatedVideo { get }
    func snapshot() -> VideoProgress?
}

protocol AudioSessionPort: AnyObject {
    var source: String { get }
    var output: String { get set }
    func updateMonitor(output: String?, enabled: Bool) throws
    func setVolume(_ gain: Float)
    func snapshot() -> AudioSnapshot
}

protocol RecordingSessionPort: AnyObject {
    func stop()
    func snapshot() -> RecordingSnapshot
}

protocol MediaRuntime {
    func makeNotification() throws -> any FrameNotification
    func videoDevices() throws -> [VideoDevice]
    func videoModes(_ path: String) throws -> [VideoMode]
    func audioDevices() throws -> AudioDevices
    func startVideo(_ configuration: VideoConfiguration, notification: any FrameNotification,
                    fallbackReason: inout String) throws -> any VideoSessionPort
    func startAudio(source: String, output: String, gain: Float, monitor: Bool) throws -> any AudioSessionPort
    func startRecording(_ configuration: RecordingConfiguration, video: any VideoSessionPort,
                        audio: (any AudioSessionPort)?, details: NegotiatedVideo) throws -> any RecordingSessionPort
}
