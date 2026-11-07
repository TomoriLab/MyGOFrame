import Foundation
import Glibc

private func testGainAndUnderrun() {
    let ring = AudioRing()
    ring.setVolume(0.5)
    let input: [Float] = [1, -1, 0.25, -0.25]
    var output = [Float](repeating: .nan, count: 8)
    input.withUnsafeBufferPointer { ring.capture($0.baseAddress, frames: 2) }
    output.withUnsafeMutableBufferPointer { ring.playback($0.baseAddress!, frames: 4) }
    precondition(output == [0.5, -0.5, 0.125, -0.125, 0, 0, 0, 0])
    precondition(ring.underrunFrames.load(ordering: .relaxed) == 2)
    for volume: Float in [0, .nan, -.infinity, -1] {
        ring.setVolume(volume)
        input.withUnsafeBufferPointer { ring.capture($0.baseAddress, frames: 2) }
        output.withUnsafeMutableBufferPointer { ring.playback($0.baseAddress!, frames: 4) }
        precondition(output.allSatisfy { $0 == 0 })
    }
}

private func testStaleQueueDiscard() {
    let ring = AudioRing()
    let input = (0..<1024).map { Float($0) }
    var output = [Float](repeating: 0, count: 256)
    input.withUnsafeBufferPointer { samples in
        for index in 0..<4 { ring.capture(samples.baseAddress!.advanced(by: index * 256), frames: 128) }
    }
    output.withUnsafeMutableBufferPointer { ring.playback($0.baseAddress!, frames: 128) }
    precondition(output[0] == 768 && output[255] == 1023)
    precondition(ring.droppedFrames.load(ordering: .relaxed) == 384)
}

private func testOverflowRecoveryAndWrap() {
    let ring = AudioRing()
    let input = [Float](repeating: 0.75, count: (AudioRing.capacity + 16) * AudioRing.channels)
    var output = [Float](repeating: 1, count: 256)
    input.withUnsafeBufferPointer { ring.capture($0.baseAddress!, frames: UInt32(AudioRing.capacity + 16)) }
    output.withUnsafeMutableBufferPointer { ring.playback($0.baseAddress!, frames: 128) }
    precondition(output.allSatisfy { $0 == 0 })
    precondition(ring.droppedFrames.load(ordering: .relaxed) == UInt64(AudioRing.capacity + 16))
    input.withUnsafeBufferPointer { ring.capture($0.baseAddress!, frames: 128) }
    output.withUnsafeMutableBufferPointer { ring.playback($0.baseAddress!, frames: 128) }
    precondition(output.allSatisfy { $0 == 0.75 })
    ring.capture(nil, frames: 128)
    output.withUnsafeMutableBufferPointer { ring.playback($0.baseAddress!, frames: 128) }
    precondition(output.allSatisfy { $0 == 0 })
}

private func produceSamples(_ userdata: UnsafeMutableRawPointer?) -> UnsafeMutableRawPointer? {
    let ring = userdata!.assumingMemoryBound(to: AudioRing.self)
    let input = UnsafeMutablePointer<Float>.allocate(capacity: 256)
    input.initialize(repeating: 0, count: 256)
    defer { input.deinitialize(count: 256); input.deallocate() }
    for iteration in 1...20_000 {
        for index in 0..<128 {
            input[index * 2] = Float(iteration)
            input[index * 2 + 1] = -Float(iteration)
        }
        ring.pointee.capture(input, frames: 128)
    }
    return nil
}

private func testConcurrentRing() {
    let ring = UnsafeMutablePointer<AudioRing>.allocate(capacity: 1)
    ring.initialize(to: AudioRing())
    defer { ring.deinitialize(count: 1); ring.deallocate() }
    var thread = pthread_t()
    precondition(pthread_create(&thread, nil, produceSamples, ring) == 0)
    let output = UnsafeMutablePointer<Float>.allocate(capacity: 256)
    output.initialize(repeating: 0, count: 256)
    defer { output.deinitialize(count: 256); output.deallocate() }
    for _ in 0..<20_000 {
        ring.pointee.playback(output, frames: 128)
        for index in 0..<128 { precondition(output[index * 2] == -output[index * 2 + 1]) }
    }
    precondition(pthread_join(thread, nil) == 0)
    precondition(ring.pointee.capturedFrames.load(ordering: .relaxed) == 2_560_000)
}

private func testMissingAudioSource() {
    do {
        _ = try AudioMonitor(source: "mygoframe.nonexistent.test-source", output: "", volume: 0)
        fatalError("A missing audio source was accepted.")
    } catch { print("Missing audio source rejected: \(error)") }
}

private func testAudioMonitor(source: String, seconds: Double) throws {
    let monitor = try AudioMonitor(source: source, output: "", volume: 0)
    Thread.sleep(forTimeInterval: seconds)
    let statistics = monitor.details
    let data = try JSONSerialization.data(withJSONObject: statistics, options: [.sortedKeys])
    print(String(decoding: data, as: UTF8.self))
    precondition(statistics["running"] as? Bool == true)
    precondition((statistics["capturedFrames"] as? UInt64 ?? 0) > 0)
    precondition((statistics["playedFrames"] as? UInt64 ?? 0) > 0)
}

private func processResourceCounts() throws -> (descriptors: Int, threads: Int) {
    (try FileManager.default.contentsOfDirectory(atPath: "/proc/self/fd").count,
     try FileManager.default.contentsOfDirectory(atPath: "/proc/self/task").count)
}

private func testPipeWireLifecycle() throws {
    let source = ProcessInfo.processInfo.environment["AUDIO_SOURCE", default: ""]
    _ = try AudioDiscovery.devices()
    testMissingAudioSource()
    if !source.isEmpty { try testAudioMonitor(source: source, seconds: 1) }
    let baseline = try processResourceCounts()
    for iteration in 1...5 {
        for _ in 0..<10 { _ = try AudioDiscovery.devices() }
        testMissingAudioSource()
        if !source.isEmpty { try testAudioMonitor(source: source, seconds: 1) }
        let current = try processResourceCounts()
        precondition(current.descriptors == baseline.descriptors, "PipeWire retained file descriptors after teardown.")
        precondition(current.threads == baseline.threads, "PipeWire retained threads after teardown.")
        print("PipeWire lifecycle cycle \(iteration): \(current.descriptors) descriptors, \(current.threads) threads.")
    }
    print("PipeWire lifecycle checks passed with optional monitoring \(source.isEmpty ? "disabled" : "enabled").")
}

@main
struct AudioTests {
    static func main() throws {
        testGainAndUnderrun()
        testStaleQueueDiscard()
        testOverflowRecoveryAndWrap()
        testConcurrentRing()
        if CommandLine.arguments.contains("--enumerate") {
            let devices = try AudioDiscovery.devices()
            let data = try JSONSerialization.data(withJSONObject: devices, options: [.sortedKeys])
            print(String(decoding: data, as: UTF8.self))
        }
        if CommandLine.arguments.contains("--missing-source") {
            testMissingAudioSource()
        }
        if let index = CommandLine.arguments.firstIndex(of: "--monitor"), index + 1 < CommandLine.arguments.count {
            try testAudioMonitor(source: CommandLine.arguments[index + 1], seconds: 3)
        }
        if CommandLine.arguments.contains("--integration") {
            try testPipeWireLifecycle()
        }
        print("Swift audio tests passed.")
    }
}
