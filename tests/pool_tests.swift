import Foundation

@main
struct PoolTests {
    static func main() throws {
        var pool: FramePool? = try FramePool(width: 2, height: 2, stride: 8)
        let first = pool!.reserve()!
        first.allocation.pointer[0] = 17
        pool!.publish(first, sequence: 1, timestampNanoseconds: 100)
        var firstLease: FrameLease? = pool!.acquire(after: 0)
        precondition(pool!.acquire(after: 1) == nil)

        let second = pool!.reserve()!
        second.allocation.pointer[0] = 29
        pool!.publish(second, sequence: 2, timestampNanoseconds: 200)
        var secondLease: FrameLease? = pool!.acquire(after: 1)

        let third = pool!.reserve()!
        third.allocation.pointer[0] = 43
        pool!.publish(third, sequence: 3, timestampNanoseconds: 300)
        var thirdLease: FrameLease? = pool!.acquire(after: 2)
        precondition(pool!.reserve() == nil)
        precondition(firstLease!.storage.allocation.pointer[0] == 17)
        precondition(secondLease!.storage.allocation.pointer[0] == 29)
        firstLease = nil
        let recycled = pool!.reserve()!
        precondition(recycled === first)
        pool!.cancel(recycled)
        pool = nil
        precondition(secondLease!.storage.allocation.pointer[0] == 29)
        precondition(thirdLease!.storage.allocation.pointer[0] == 43)
        secondLease = nil
        thirdLease = nil
        do {
            _ = try FramePool(width: 16385, height: 1, stride: 65540)
            fatalError("Invalid dimensions were accepted.")
        } catch {}
        print("Frame pool tests passed: lease pinning, bounded capacity, recycling, detached lifetime, dimension validation.")
    }
}
