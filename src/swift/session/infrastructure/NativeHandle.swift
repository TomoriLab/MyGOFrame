// A native allocation has one release owner even when a session is retained temporarily.
struct NativeHandle: ~Copyable {
    let pointer: UnsafeMutableRawPointer
    private let destroy: (UnsafeMutableRawPointer) -> Void

    init(pointer: UnsafeMutableRawPointer, destroy: @escaping (UnsafeMutableRawPointer) -> Void) {
        self.pointer = pointer
        self.destroy = destroy
    }

    deinit { destroy(pointer) }
}
