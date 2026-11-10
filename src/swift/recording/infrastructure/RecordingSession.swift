import Foundation
import NativeCore
final class RecordingSession: RecordingSessionPort {
    private let owner: NativeHandle
    var handle: UnsafeMutableRawPointer { owner.pointer }
    init(handle: UnsafeMutableRawPointer) { owner = NativeHandle(pointer: handle, destroy: { mgfr_recording_destroy($0) }) }
    func stop() { mgfr_recording_stop(handle) }
    func snapshot() -> RecordingSnapshot {
        (try? decodeRustJSON(mgfr_recording_status_json(handle), as: RecordingSnapshot.self)) ?? RecordingSnapshot(phase: "failed", error: "Unable to read recording status.")
    }
}

