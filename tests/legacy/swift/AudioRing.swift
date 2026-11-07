import Glibc
import Synchronization

// Acquire/release positions transfer ownership of sample slots between the two callbacks.
struct AudioRing: ~Copyable {
    static let capacity = 4096
    static let channels = 2
    let samples: UnsafeMutablePointer<Float>
    let writePosition = Atomic<UInt64>(0)
    let readPosition = Atomic<UInt64>(0)
    let droppedFrames = Atomic<UInt64>(0)
    let underrunFrames = Atomic<UInt64>(0)
    let capturedFrames = Atomic<UInt64>(0)
    let playedFrames = Atomic<UInt64>(0)
    let overflowed = Atomic<Bool>(false)
    let captureQuantum = Atomic<UInt32>(0)
    private let volumeBits = Atomic<UInt32>(Float(1).bitPattern)

    init() {
        samples = .allocate(capacity: Self.capacity * Self.channels)
        samples.initialize(repeating: 0, count: Self.capacity * Self.channels)
    }

    deinit {
        samples.deinitialize(count: Self.capacity * Self.channels)
        samples.deallocate()
    }

    borrowing func setVolume(_ value: Float) {
        let gain = value.isFinite ? max(0, min(1, value)) : 0
        volumeBits.store(gain.bitPattern, ordering: .relaxed)
    }

    @inline(__always)
    borrowing func capture(_ input: UnsafePointer<Float>?, frames: UInt32) {
        captureQuantum.store(frames, ordering: .relaxed)
        let write = writePosition.load(ordering: .relaxed)
        let read = readPosition.load(ordering: .acquiring)
        let available = UInt64(Self.capacity) - (write &- read)
        let accepted = min(UInt64(frames), available)
        for index in 0..<Int(accepted) {
            let offset = Int((write &+ UInt64(index)) % UInt64(Self.capacity)) * Self.channels
            samples[offset] = input?[index * Self.channels] ?? 0
            samples[offset + 1] = input?[index * Self.channels + 1] ?? 0
        }
        writePosition.store(write &+ accepted, ordering: .releasing)
        capturedFrames.wrappingAdd(UInt64(frames), ordering: .relaxed)
        if accepted != UInt64(frames) {
            droppedFrames.wrappingAdd(UInt64(frames) - accepted, ordering: .relaxed)
            overflowed.store(true, ordering: .releasing)
        }
    }

    @inline(__always)
    borrowing func playback(_ output: UnsafeMutablePointer<Float>, frames: UInt32) {
        var read = readPosition.load(ordering: .relaxed)
        let write = writePosition.load(ordering: .acquiring)
        var available = write &- read
        let quantum = max(frames, captureQuantum.load(ordering: .relaxed))
        // A stalled output resumes with current samples instead of replaying queued audio.
        if overflowed.exchange(false, ordering: .acquiringAndReleasing) {
            droppedFrames.wrappingAdd(available, ordering: .relaxed)
            read = write
            available = 0
        } else if available > UInt64(quantum) * 2 {
            let skipped = available - UInt64(quantum)
            read &+= skipped
            available -= skipped
            droppedFrames.wrappingAdd(skipped, ordering: .relaxed)
        }
        let provided = min(UInt64(frames), available)
        let gain = Float(bitPattern: volumeBits.load(ordering: .relaxed))
        for index in 0..<Int(provided) {
            let offset = Int((read &+ UInt64(index)) % UInt64(Self.capacity)) * Self.channels
            output[index * Self.channels] = samples[offset] * gain
            output[index * Self.channels + 1] = samples[offset + 1] * gain
        }
        let missing = Int(UInt64(frames) - provided)
        memset(output.advanced(by: Int(provided) * Self.channels), 0, missing * Self.channels * MemoryLayout<Float>.stride)
        readPosition.store(read &+ provided, ordering: .releasing)
        underrunFrames.wrappingAdd(UInt64(missing), ordering: .relaxed)
        playedFrames.wrappingAdd(provided, ordering: .relaxed)
    }
}
