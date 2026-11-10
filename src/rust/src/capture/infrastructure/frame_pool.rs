use crate::capture::domain::layout::{BUFFER_COUNT, frame_layout};
use std::cell::UnsafeCell;
use std::sync::{Arc, Mutex};

pub(crate) struct FrameSlot {
    pixels: UnsafeCell<Box<[u8]>>,
}

// Pool state prevents a writer while any lease retains the slot.
unsafe impl Send for FrameSlot {}
unsafe impl Sync for FrameSlot {}

impl FrameSlot {
    fn new(length: usize) -> Result<Self, String> {
        let mut pixels = Vec::new();
        pixels
            .try_reserve_exact(length)
            .map_err(|_| "Unable to allocate the video frame pool.")?;
        pixels.resize(length, 0);
        Ok(Self {
            pixels: UnsafeCell::new(pixels.into_boxed_slice()),
        })
    }

    pub(crate) fn address(&self) -> *const u8 {
        unsafe { (*self.pixels.get()).as_ptr() }
    }

    fn writable_address(&self) -> *mut u8 {
        unsafe { (*self.pixels.get()).as_mut_ptr() }
    }
}

struct PoolState {
    latest: Option<usize>,
    writing: [bool; BUFFER_COUNT],
    sequence: [u64; BUFFER_COUNT],
    timestamp: [u64; BUFFER_COUNT],
}

pub(crate) struct FramePool {
    slots: Vec<Arc<FrameSlot>>,
    state: Mutex<PoolState>,
    pub(crate) width: u32,
    pub(crate) height: u32,
    pub(crate) stride: u32,
    pub(crate) byte_length: usize,
}

impl FramePool {
    pub(crate) fn new(width: u32, height: u32) -> Result<Self, String> {
        let (stride, byte_length) = frame_layout(width, height)
            .ok_or("Negotiated video dimensions exceed the frame pool limits.")?;
        let mut slots = Vec::with_capacity(BUFFER_COUNT);
        for _ in 0..BUFFER_COUNT {
            slots.push(Arc::new(FrameSlot::new(byte_length)?));
        }
        Ok(Self {
            slots,
            state: Mutex::new(PoolState {
                latest: None,
                writing: [false; BUFFER_COUNT],
                sequence: [0; BUFFER_COUNT],
                timestamp: [0; BUFFER_COUNT],
            }),
            width,
            height,
            stride,
            byte_length,
        })
    }

    pub(crate) fn reserve(&self) -> Option<FrameReservation<'_>> {
        let mut state = self.state.lock().unwrap();
        for index in 0..BUFFER_COUNT {
            if state.latest != Some(index)
                && !state.writing[index]
                && Arc::strong_count(&self.slots[index]) == 1
            {
                state.writing[index] = true;
                return Some(FrameReservation {
                    pool: self,
                    index,
                    published: false,
                });
            }
        }
        None
    }

    fn cancel(&self, index: usize) {
        self.state.lock().unwrap().writing[index] = false;
    }

    fn publish(&self, index: usize, sequence: u64, timestamp: u64) {
        let mut state = self.state.lock().unwrap();
        state.sequence[index] = sequence;
        state.timestamp[index] = timestamp;
        state.writing[index] = false;
        state.latest = Some(index);
    }

    pub(crate) fn acquire(&self, after_sequence: u64) -> Option<(Arc<FrameSlot>, u64, u64)> {
        let state = self.state.lock().unwrap();
        let index = state.latest?;
        if state.sequence[index] <= after_sequence {
            return None;
        }
        Some((
            Arc::clone(&self.slots[index]),
            state.sequence[index],
            state.timestamp[index],
        ))
    }
}

/// A reservation exclusively borrows a writable slot until publication or cancellation.
pub(crate) struct FrameReservation<'pool> {
    pool: &'pool FramePool,
    index: usize,
    published: bool,
}

impl FrameReservation<'_> {
    pub(crate) fn pixels(&mut self) -> &mut [u8] {
        // The pool's lock reserves a slot only when no reader owns it. The returned
        // slice borrows this reservation, so publication cannot overlap a writer.
        unsafe {
            std::slice::from_raw_parts_mut(
                self.pool.slots[self.index].writable_address(),
                self.pool.byte_length,
            )
        }
    }

    pub(crate) fn publish(mut self, sequence: u64, timestamp: u64) {
        self.pool.publish(self.index, sequence, timestamp);
        self.published = true;
    }
}

impl Drop for FrameReservation<'_> {
    fn drop(&mut self) {
        if !self.published {
            self.pool.cancel(self.index);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn frame_lease_survives_pool_destroy_and_blocks_reuse() {
        let pool = FramePool::new(2, 2).unwrap();
        let mut first = pool.reserve().unwrap();
        let first_index = first.index;
        first.pixels()[0] = 17;
        first.publish(1, 100);
        let (first_lease, sequence, timestamp) = pool.acquire(0).unwrap();
        assert_eq!((sequence, timestamp), (1, 100));
        let second = pool.reserve().unwrap();
        let second_index = second.index;
        assert_ne!(first_index, second_index);
        second.publish(2, 200);
        let (second_lease, _, _) = pool.acquire(1).unwrap();
        let third = pool.reserve().unwrap();
        assert_ne!(third.index, first_index);
        assert_ne!(third.index, second_index);
        third.publish(3, 300);
        let (third_lease, _, _) = pool.acquire(2).unwrap();
        assert!(pool.reserve().is_none());
        drop(second_lease);
        let released = pool.reserve().unwrap();
        assert_eq!(released.index, second_index);
        drop(released);
        drop(pool);
        assert_eq!(unsafe { *first_lease.address() }, 17);
        drop(third_lease);
    }

    #[test]
    fn cancelled_and_unwound_writes_return_the_reservation() {
        let pool = FramePool::new(2, 2).unwrap();
        let first = pool.reserve().unwrap();
        let index = first.index;
        drop(first);
        assert_eq!(pool.reserve().unwrap().index, index);
        let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            let mut reservation = pool.reserve().unwrap();
            reservation.pixels().fill(31);
            panic!("injected conversion failure");
        }));
        assert!(result.is_err());
        assert_eq!(pool.reserve().unwrap().index, index);
        assert!(pool.acquire(0).is_none());
    }

    #[test]
    fn simultaneous_writers_receive_distinct_slots() {
        let pool = FramePool::new(2, 2).unwrap();
        let mut first = pool.reserve().unwrap();
        let mut second = pool.reserve().unwrap();
        assert_ne!(first.index, second.index);
        first.pixels().fill(17);
        second.pixels().fill(29);
        assert_eq!(first.pixels()[0], 17);
        first.publish(1, 100);
        let (lease, _, _) = pool.acquire(0).unwrap();
        second.publish(2, 200);
        assert_eq!(unsafe { *lease.address() }, 17);
    }
}
