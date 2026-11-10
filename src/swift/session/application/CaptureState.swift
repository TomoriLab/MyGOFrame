import Foundation
// The registry isolates session state while Rust owns workers and frame storage.
struct CaptureState: ~Copyable {
    private let runtime: any MediaRuntime
    private let notification: any FrameNotification

    init(runtime: any MediaRuntime) throws {
        self.runtime = runtime
        notification = try runtime.makeNotification()
    }
    borrowing func duplicateEvents() -> Int32 { notification.duplicateEvents() }

    // The ABI adapter borrows a session inside the registry lock and returns only its delivery result.
    borrowing func withVideo(_ operation: ((any VideoSessionPort)?) -> Int32) -> Int32 { operation(video) }

    private var video: (any VideoSessionPort)?
    private var audio: (any AudioSessionPort)?
    private var recording: (any RecordingSessionPort)?
    private var monitorEnabled = true
    private var videoError = ""
    private var videoPhase = SessionPhase.stopped
    private var audioPhase = SessionPhase.stopped
    private var fallbackReason = ""
    private var videoDetails = NegotiatedVideo()
    private var requestedVideo: VideoConfiguration?
    private(set) var closed = false
    private var generation: UInt64 = 0
    private var commandID: UInt64 = 0
    private var frames: UInt64 = 0
    private var droppedFrames: UInt64 = 0
    private var audioPolicy = AudioPolicy()
    private var audioError = ""

    mutating func startRecording(_ configuration: RecordingConfiguration) throws {
        guard let video else { throw CoreError.message("Start video before recording.") }
        guard recording?.snapshot().active != true else { throw CoreError.message("A recording is already active or finalizing.") }
        if configuration.recordAudio && audio == nil { throw CoreError.message("Select the capture-card audio input or disable Record audio.") }
        recording = nil
        recording = try runtime.startRecording(configuration, video: video, audio: audio, details: videoDetails)
        advanceCommand()
    }

    mutating func stopRecording() { recording?.stop(); advanceCommand() }

    mutating func setMonitoring(_ enabled: Bool) throws {
        do { try audio?.updateMonitor(output: nil, enabled: enabled) }
        catch { monitorEnabled = false; throw error }
        monitorEnabled = enabled
        advanceCommand()
    }

    borrowing func requirePresetActivation() throws {
        guard recording?.snapshot().active != true else { throw CoreError.message("Stop recording before switching presets.") }
    }

    mutating func activatePreset(_ resolution: PresetResolution, start: Bool) throws {
        try requirePresetActivation()
        let previousVideo = requestedVideo
        let wasRunning = video != nil
        let previousSource = audio?.source ?? ""
        let previousOutput = audio?.output ?? ""
        let previousPolicy = audioPolicy
        let previousMonitor = monitorEnabled
        let preset = resolution.preset
        let mode = resolution.mode
        if wasRunning { stopVideo() }
        do {
            try applyPresetAudio(preset)
            if start || wasRunning {
                try startVideo(VideoConfiguration(path: preset.devicePath, fourcc: mode.fourcc,
                    width: mode.width, height: mode.height, intervalNumerator: mode.intervalNumerator,
                    intervalDenominator: mode.intervalDenominator,
                    colorRange: preset.colorRange == "Full" ? 2 : (preset.colorRange == "Limited" ? 1 : 0),
                    backend: preset.backend == "dma" ? 2 : (preset.backend == "cpu" ? 1 : 0), fallbackReason: ""))
            }
        } catch {
            var failures = [String(describing: error)]
            configureAudio(volume: previousPolicy.volume, muted: previousPolicy.muted)
            do {
                try setMonitoring(previousMonitor)
                try startAudio(source: previousSource, output: previousOutput)
            } catch { failures.append("Restore previous audio: \(error)") }
            if wasRunning, let previousVideo {
                do { try startVideo(previousVideo) }
                catch { failures.append("Restore previous video: \(error)") }
            }
            throw CoreError.message(failures.joined(separator: "; "))
        }
    }

    mutating func applyPresetAudio(_ preset: CapturePreset) throws {
        guard video == nil else { throw CoreError.message("Stop video before applying a preset.") }
        let previous = audioPolicy
        let previousMonitor = monitorEnabled
        try setMonitoring(preset.monitorEnabled ?? true)
        configureAudio(volume: Float(preset.volume), muted: preset.muted)
        do { try startAudio(source: preset.audioSource, output: preset.audioOutput) }
        catch {
            configureAudio(volume: previous.volume, muted: previous.muted)
            try? setMonitoring(previousMonitor)
            throw error
        }
    }

    borrowing func requireStoppedVideoForPreset() throws {
        guard recording?.snapshot().active != true else { throw CoreError.message("Stop recording before applying a preset.") }
        guard video == nil else { throw CoreError.message("Stop video before applying a preset.") }
    }

    mutating func startVideo(_ configuration: VideoConfiguration) throws {
        advanceCommand(videoChange: true)
        stopVideo(recordCommand: false)
        requestedVideo = configuration
        videoPhase = .starting
        videoError = ""
        fallbackReason = configuration.fallbackReason
        videoDetails = NegotiatedVideo()
        frames = 0
        droppedFrames = 0
        do {
            let session = try runtime.startVideo(configuration, notification: notification, fallbackReason: &fallbackReason)
            video = session
            videoDetails = session.details
            videoPhase = .running
        } catch {
            videoPhase = .failed
            videoError = String(describing: error)
            throw error
        }
    }

    mutating func stopVideo(recordCommand: Bool = true) {
        if recordCommand { advanceCommand(videoChange: true) }
        recording?.stop()
        videoPhase = .stopping
        if let snapshot = video?.snapshot() {
            frames = snapshot.frames
            droppedFrames = snapshot.droppedFrames
            if !snapshot.error.isEmpty { videoError = snapshot.error }
        }
        video = nil
        videoPhase = .stopped
    }

    mutating func startAudio(source: String, output: String) throws {
        if let audio, audio.source == source {
            do { try audio.updateMonitor(output: output, enabled: monitorEnabled) }
            catch { monitorEnabled = false; throw error }
            audio.output = output
            advanceCommand()
            return
        }
        guard recording?.snapshot().active != true else { throw CoreError.message("Stop recording before changing the audio input.") }
        advanceCommand()
        stopAudio(recordCommand: false)
        guard !source.isEmpty else { return }
        audioPhase = .starting
        do { audio = try runtime.startAudio(source: source, output: output, gain: audioPolicy.gain, monitor: monitorEnabled) }
        catch {
            audioPhase = .failed
            audioError = String(describing: error)
            throw error
        }
        audioPhase = .running
    }

    mutating func stopAudio(recordCommand: Bool = true) {
        if recordCommand { advanceCommand() }
        audioPhase = .stopping
        audio = nil
        audioPhase = .stopped
        audioError = ""
    }

    mutating func setVolume(_ value: Float) {
        advanceCommand()
        audioPolicy.setVolume(value)
        if let audio { audio.setVolume(audioPolicy.gain) }
    }

    mutating func configureAudio(volume: Float, muted: Bool) {
        advanceCommand()
        audioPolicy.configure(volume: volume, muted: muted)
        if let audio { audio.setVolume(audioPolicy.gain) }
    }

    mutating func fallback(expectedGeneration: UInt64, reason: String) throws -> Bool {
        guard expectedGeneration == generation, video?.usesDMA == true,
              let requested = requestedVideo else { return false }
        // Recovery changes the active backend without replacing the user's requested settings.
        defer { requestedVideo = requested }
        try startVideo(VideoConfiguration(path: requested.path, fourcc: requested.fourcc,
            width: requested.width, height: requested.height, intervalNumerator: requested.intervalNumerator,
            intervalDenominator: requested.intervalDenominator, colorRange: requested.colorRange,
            backend: 1, fallbackReason: reason))
        return true
    }

    mutating func refreshDevices(path: String, source: String, output: String) -> DeviceCatalog {
        advanceCommand()
        var catalog = DeviceCatalog()
        var errors: [String] = []
        var videoAvailable = false
        var audioAvailable = false
        do {
            catalog.devices = try runtime.videoDevices()
            videoAvailable = true
            catalog.videoAvailable = true
        } catch { errors.append(String(describing: error)) }
        do {
            catalog.audioDevices = try runtime.audioDevices()
            audioAvailable = true
            catalog.audioAvailable = true
        } catch { errors.append(String(describing: error)) }
        let selection = DeviceSelection.resolve(devices: catalog.devices, audio: catalog.audioDevices,
            path: path, source: source, output: output)
        catalog.selectedPath = videoAvailable ? selection.path : path
        // Enumeration failures do not prove that a running audio device disappeared.
        catalog.source = audioAvailable ? selection.source : source
        catalog.output = audioAvailable ? selection.output : output
        if audioAvailable && selection.stopAudio { stopAudio(recordCommand: false) }
        if !catalog.selectedPath.isEmpty {
            do {
                catalog.modes = try runtime.videoModes(catalog.selectedPath)
                catalog.modesAvailable = true
            } catch { errors.append(String(describing: error)) }
        }
        catalog.error = errors.joined(separator: "; ")
        return catalog
    }

    private mutating func advanceCommand(videoChange: Bool = false) {
        // Counter exhaustion cannot alias an earlier session or command.
        precondition(commandID < UInt64.max && (!videoChange || generation < UInt64.max))
        commandID += 1
        if videoChange { generation += 1 }
    }

    mutating func shutdown() {
        closed = true
        recording?.stop()
        recording = nil
        stopVideo(recordCommand: false)
        stopAudio(recordCommand: false)
    }

    mutating func status() -> SessionSnapshot {
        var videoRunning = false
        if let video {
            if let snapshot = video.snapshot() {
                videoRunning = snapshot.running
                frames = snapshot.frames
                droppedFrames = snapshot.droppedFrames
                if !snapshot.error.isEmpty { videoError = snapshot.error }
            } else { videoError = "Unable to read video status from the media core." }
        }
        let audioStatus = audio?.snapshot() ?? AudioSnapshot(error: audioError)
        if video != nil { videoPhase = videoRunning ? .running : (videoError.isEmpty ? .stopped : .failed) }
        if audio != nil { audioPhase = audioStatus.running ? .running : (audioStatus.error.isEmpty ? .stopped : .failed) }
        return SessionSnapshot(recording: recording?.snapshot() ?? RecordingSnapshot(), monitorEnabled: monitorEnabled, generation: generation, commandID: commandID,
            videoPhase: videoPhase,
            audioPhase: audioPhase,
            videoRunning: videoRunning, error: videoError, frames: frames, droppedFrames: droppedFrames,
            video: videoDetails, requestedVideo: requestedVideo, videoBackend: videoDetails.backend,
            fallbackReason: fallbackReason, audio: audioStatus, volume: audioPolicy.volume,
            muted: audioPolicy.muted, effectiveGain: audioPolicy.gain)
    }

}

