import Foundation
import Dispatch

@main
struct SessionTests {
    static func consumeHandle(_ handle: consuming NativeHandle) { _ = handle.pointer }

    static func main() throws {
        try runPresetTests()
        var releases = 0
        let allocation = UnsafeMutableRawPointer.allocate(byteCount: 8, alignment: 8)
        let owner = NativeHandle(pointer: allocation) { pointer in
            pointer.deallocate()
            releases += 1
        }
        consumeHandle(consume owner)
        precondition(releases == 1)
        let modes = [
            VideoMode(fourcc: 1, format: "NV12", width: 1920, height: 1080, intervalNumerator: 1, intervalDenominator: 60),
            VideoMode(fourcc: 1, format: "NV12", width: 1920, height: 1080, intervalNumerator: 1001, intervalDenominator: 60000),
            VideoMode(fourcc: 2, format: "BGR3", width: 640, height: 480, intervalNumerator: 1, intervalDenominator: 30)
        ]
        let selection = ModeSelection.resolve(ModeRequest(modes: modes, format: "BGR3", width: 1920, height: 1080,
            intervalNumerator: 1001, intervalDenominator: 60000))
        precondition(selection.selected == modes[2] && selection.frameRates.count == 1)
        let rational = ModeSelection.resolve(ModeRequest(modes: modes, format: "NV12", width: 1920, height: 1080,
            intervalNumerator: 1001, intervalDenominator: 60000))
        precondition(rational.selected == modes[1])
        precondition(ModeSelection.resolve(ModeRequest(modes: [], format: "NV12", width: 1920, height: 1080,
            intervalNumerator: 1, intervalDenominator: 60)).selected == nil)
        var audio = AudioPolicy()
        audio.configure(volume: 0.37, muted: true)
        precondition(audio.volume == 0.37 && audio.gain == 0)
        audio.configure(volume: audio.volume, muted: false)
        precondition(audio.gain == 0.37)
        audio.setVolume(.nan)
        precondition(audio.gain == 0)
        audio.setVolume(2)
        precondition(audio.gain == 1)
        let removed = DeviceSelection.resolve(devices: [VideoDevice(path: "/dev/video2", name: "Capture")],
            audio: AudioDevices(sources: [AudioDevice(id: "capture", name: "Capture")], outputs: []),
            path: "/dev/video1", source: "capture", output: "removed-output")
        precondition(removed.path == "/dev/video2" && removed.source.isEmpty && removed.output.isEmpty && removed.stopAudio)
        let handle = createEngine()
        precondition(handle != 0)
        let initial = try EngineRegistry.withState(handle) { $0.status() }
        precondition(initial.schemaVersion == 1 && initial.generation == 0 && initial.commandID == 0)
        DispatchQueue.concurrentPerform(iterations: 200) { index in
            if index % 2 == 0 { setVolume(handle, Float(index) / 200) }
            else { stopVideo(handle) }
        }
        let snapshot = try EngineRegistry.withState(handle) { $0.status() }
        precondition(snapshot.commandID == 200 && snapshot.generation == 100)
        let stale = try EngineRegistry.withState(handle) { try $0.fallback(expectedGeneration: 0, reason: "Retired renderer") }
        precondition(!stale)
        let unchanged = try EngineRegistry.withState(handle) { $0.status().commandID }
        precondition(unchanged == snapshot.commandID)
        DispatchQueue.concurrentPerform(iterations: 100) { index in
            if index == 50 { destroyEngine(handle) }
            else { setVolume(handle, 0) }
        }
        destroyEngine(handle)
        do {
            _ = try EngineRegistry.withState(handle) { $0.status() }
            preconditionFailure("A retired session handle was accepted.")
        } catch {}
        let replacement = createEngine()
        precondition(replacement != handle)
        destroyEngine(handle)
        _ = try EngineRegistry.withState(replacement) { $0.status() }
        destroyEngine(replacement)
        print("Session concurrency, retirement, generation, snapshot, selection, and audio policy tests passed.")
    }
}
