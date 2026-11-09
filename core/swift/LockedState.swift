import Foundation

// NSLock uses the same pthread synchronization in production and sanitizer builds.
// The value is private, every access holds the lock, and returned values are Sendable.
final class LockedState<Value: ~Copyable>: @unchecked Sendable {
    private let lock = NSLock()
    private var value: Value

    init(_ value: consuming sending Value) { self.value = value }

    func withLock<Result: Sendable>(_ operation: (inout sending Value) throws -> sending Result) rethrows -> Result {
        lock.lock()
        defer { lock.unlock() }
        return try operation(&value)
    }
}
