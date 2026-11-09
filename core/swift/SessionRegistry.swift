import Foundation

private final class EngineOwner: Sendable {
    let state: LockedState<CaptureState>
    init() throws { state = LockedState(try CaptureState()) }
}

private struct RegistryState {
    var nextHandle: UInt64 = 1
    var owners: [UInt64: EngineOwner] = [:]
}

enum EngineRegistry {
    private static let storage = LockedState(RegistryState())

    static func create() -> UInt64 {
        storage.withLock { registry in
            guard registry.nextHandle < UInt64.max else { return 0 }
            let handle = registry.nextHandle
            registry.nextHandle += 1
            guard let owner = try? EngineOwner() else { return 0 }
            registry.owners[handle] = owner
            return handle
        }
    }

    static func withState<Result: Sendable>(_ handle: UInt64, _ operation: (inout sending CaptureState) throws -> sending Result) throws -> Result {
        guard let owner = storage.withLock({ $0.owners[handle] }) else {
            throw CoreError.message("The capture engine is closed.")
        }
        // The FFI closure borrows state only while this lock is held; only Sendable results may escape.
        return try owner.state.withLock { state in
            guard !state.closed else { throw CoreError.message("The capture engine is closed.") }
            return try operation(&state)
        }
    }

    static func destroy(_ handle: UInt64) {
        // Taking the owner out first prevents new calls while an existing call finishes.
        let owner = storage.withLock { $0.owners.removeValue(forKey: handle) }
        owner?.state.withLock { $0.shutdown() }
    }
}
