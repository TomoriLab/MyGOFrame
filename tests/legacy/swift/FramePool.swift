import Foundation
import Glibc

enum CoreError: Error, CustomStringConvertible {
    case message(String)
    var description: String {
        switch self { case .message(let text): return text }
    }
}

struct PixelAllocation: ~Copyable {
    let pointer: UnsafeMutablePointer<UInt8>
    let capacity: Int

    init(capacity: Int) throws {
        guard capacity > 0, let memory = malloc(capacity) else {
            throw CoreError.message("Unable to allocate the video frame pool.")
        }
        self.pointer = memory.assumingMemoryBound(to: UInt8.self)
        self.capacity = capacity
    }

    deinit { free(pointer) }
}

final class FrameStorage {
    let allocation: PixelAllocation
    var readers = 0
    var writing = false
    var sequence: UInt64 = 0
    var timestampNanoseconds: UInt64 = 0

    init(allocation: consuming PixelAllocation) { self.allocation = allocation }
}

// The pool lock protects slot state; a writer can only access an unleased, unpublished slot.
final class FramePool: @unchecked Sendable {
    let width: Int
    let height: Int
    let stride: Int
    private let lock = NSLock()
    private var slots: [FrameStorage] = []
    private var latest: FrameStorage?

    init(width: Int, height: Int, stride: Int) throws {
        guard width > 0, height > 0, width <= 16384, height <= 16384,
              stride >= width * 4, stride <= 16384 * 8,
              height <= (512 * 1024 * 1024) / stride else {
            throw CoreError.message("Negotiated video dimensions exceed the frame pool limits.")
        }
        self.width = width
        self.height = height
        self.stride = stride
        for _ in 0..<3 { slots.append(FrameStorage(allocation: try PixelAllocation(capacity: stride * height))) }
    }

    func reserve() -> FrameStorage? {
        lock.lock()
        defer { lock.unlock() }
        guard let storage = slots.first(where: { $0 !== latest && !$0.writing && $0.readers == 0 }) else {
            return nil
        }
        storage.writing = true
        return storage
    }

    func cancel(_ storage: FrameStorage) {
        lock.lock()
        storage.writing = false
        lock.unlock()
    }

    func publish(_ storage: FrameStorage, sequence: UInt64, timestampNanoseconds: UInt64) {
        lock.lock()
        storage.sequence = sequence
        storage.timestampNanoseconds = timestampNanoseconds
        storage.writing = false
        latest = storage
        lock.unlock()
    }

    func acquire(after sequence: UInt64) -> FrameLease? {
        lock.lock()
        defer { lock.unlock() }
        guard let storage = latest, storage.sequence > sequence else { return nil }
        storage.readers += 1
        return FrameLease(pool: self, storage: storage)
    }

    fileprivate func release(_ storage: FrameStorage) {
        lock.lock()
        storage.readers -= 1
        lock.unlock()
    }
}

final class FrameLease {
    let pool: FramePool
    let storage: FrameStorage

    init(pool: FramePool, storage: FrameStorage) {
        self.pool = pool
        self.storage = storage
    }

    deinit { pool.release(storage) }
}
