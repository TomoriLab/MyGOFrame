use crate::{ffi, media::infrastructure::callback_gate::CallbackGuard};
use std::cell::UnsafeCell;
use std::sync::atomic::{AtomicBool, AtomicU32, AtomicU64, Ordering};
use std::sync::{Arc, Mutex, Weak};

pub(crate) fn monotonic_ns() -> u64 {
    let mut time: ffi::timespec = unsafe { std::mem::zeroed() };
    if unsafe { ffi::clock_gettime(ffi::CLOCK_MONOTONIC as i32, &mut time) } == 0 {
        (time.tv_sec as u64)
            .saturating_mul(1_000_000_000)
            .saturating_add(time.tv_nsec as u64)
    } else {
        0
    }
}

pub(crate) fn video_timestamp(buffer: &ffi::v4l2_buffer) -> u64 {
    if buffer.flags & ffi::V4L2_BUF_FLAG_TIMESTAMP_MASK == ffi::V4L2_BUF_FLAG_TIMESTAMP_MONOTONIC
        && buffer.timestamp.tv_sec >= 0
        && (0..1_000_000).contains(&buffer.timestamp.tv_usec)
    {
        (buffer.timestamp.tv_sec as u64)
            .saturating_mul(1_000_000_000)
            .saturating_add(buffer.timestamp.tv_usec as u64 * 1000)
    } else {
        monotonic_ns()
    }
}

pub(crate) struct VideoBlock {
    pub bytes: Vec<u8>,
    pub timestamp: u64,
}

pub(crate) struct VideoInput {
    slots: Box<[UnsafeCell<VideoBlock>]>,
    write: AtomicU64,
    read: AtomicU64,
    writer: AtomicBool,
    reader: AtomicBool,
    pub active: AtomicBool,
    pub dropped: AtomicU64,
    pub oversized: AtomicBool,
    capacity: usize,
    pub signal: crate::platform::infrastructure::notification::FrameNotification,
}
// Release/acquire positions transfer each preallocated slot between the capture and recording threads.
unsafe impl Sync for VideoInput {}

pub(crate) struct VideoRead<'a> {
    input: &'a VideoInput,
    position: u64,
    _guard: CallbackGuard<'a>,
}
impl std::ops::Deref for VideoRead<'_> {
    type Target = VideoBlock;
    fn deref(&self) -> &VideoBlock {
        unsafe { &*self.input.slots[self.position as usize % 3].get() }
    }
}
impl Drop for VideoRead<'_> {
    fn drop(&mut self) {
        self.input
            .read
            .store(self.position.wrapping_add(1), Ordering::Release);
    }
}
impl VideoInput {
    pub fn new(capacity: usize) -> Result<Arc<Self>, String> {
        if !(64..=128 * 1024 * 1024).contains(&capacity) {
            return Err("Recording frame capacity exceeds the memory limit.".into());
        }
        let mut slots = Vec::new();
        for _ in 0..3 {
            let mut bytes = Vec::new();
            bytes
                .try_reserve_exact(capacity)
                .map_err(|_| "Unable to reserve recording frame storage.")?;
            slots.push(UnsafeCell::new(VideoBlock {
                bytes,
                timestamp: 0,
            }));
        }
        Ok(Arc::new(Self {
            slots: slots.into_boxed_slice(),
            write: AtomicU64::new(0),
            read: AtomicU64::new(0),
            writer: AtomicBool::new(false),
            reader: AtomicBool::new(false),
            active: AtomicBool::new(true),
            dropped: AtomicU64::new(0),
            oversized: AtomicBool::new(false),
            capacity,
            signal: crate::platform::infrastructure::notification::FrameNotification::new()
                .map_err(|error| error.to_string())?,
        }))
    }
    pub fn submit(&self, source: &[u8], timestamp: u64) {
        if !self.active.load(Ordering::Acquire) {
            return;
        }
        if source.len().saturating_add(64) > self.capacity {
            self.oversized.store(true, Ordering::Release);
            return;
        }
        let Some(_guard) = CallbackGuard::enter(&self.writer) else {
            self.dropped.fetch_add(1, Ordering::Relaxed);
            return;
        };
        let write = self.write.load(Ordering::Relaxed);
        if write.wrapping_sub(self.read.load(Ordering::Acquire)) >= 3 {
            self.dropped.fetch_add(1, Ordering::Relaxed);
            return;
        }
        let slot = unsafe { &mut *self.slots[write as usize % 3].get() };
        slot.bytes.clear();
        slot.bytes.extend_from_slice(source);
        slot.bytes.resize(source.len() + 64, 0);
        slot.timestamp = timestamp;
        self.write.store(write.wrapping_add(1), Ordering::Release);
        self.signal.notify();
    }
    pub fn take(&self) -> Option<VideoRead<'_>> {
        let guard = CallbackGuard::enter(&self.reader)?;
        let read = self.read.load(Ordering::Relaxed);
        if read == self.write.load(Ordering::Acquire) {
            return None;
        }
        Some(VideoRead {
            input: self,
            position: read,
            _guard: guard,
        })
    }
    pub fn recycle(&self, block: VideoRead<'_>) {
        drop(block);
    }
}

#[derive(Default)]
pub(crate) struct VideoTap {
    enabled: AtomicBool,
    target: Mutex<Weak<VideoInput>>,
}
impl VideoTap {
    pub fn attach(&self, input: &Arc<VideoInput>) -> Result<(), String> {
        let mut target = self.target.lock().unwrap();
        if target
            .upgrade()
            .is_some_and(|current| current.active.load(Ordering::Acquire))
        {
            return Err("This video capture already has a recording consumer.".into());
        }
        *target = Arc::downgrade(input);
        self.enabled.store(true, Ordering::Release);
        Ok(())
    }
    pub fn is_active(&self) -> bool {
        self.enabled.load(Ordering::Acquire)
            && self
                .target
                .try_lock()
                .ok()
                .and_then(|value| value.upgrade())
                .is_some_and(|input| input.active.load(Ordering::Acquire))
    }
    pub fn submit(&self, bytes: &[u8], timestamp: u64) {
        if !self.enabled.load(Ordering::Acquire) {
            return;
        }
        if let Some(input) = self
            .target
            .try_lock()
            .ok()
            .and_then(|value| value.upgrade())
        {
            input.submit(bytes, timestamp);
        }
    }
}

const AUDIO_SLOTS: usize = 64;
const AUDIO_FRAMES: usize = 2048;
#[derive(Clone)]
pub(crate) struct AudioBlock {
    pub timestamp: u64,
    pub frames: usize,
    pub samples: [f32; AUDIO_FRAMES * 2],
}

pub(crate) struct AudioInput {
    slots: Box<[UnsafeCell<AudioBlock>]>,
    write: AtomicU64,
    read: AtomicU64,
    writer: AtomicBool,
    reader: AtomicBool,
    pub peak: AtomicU32,
    pub active: AtomicBool,
    pub overflow: AtomicBool,
    pub signal: Option<crate::platform::infrastructure::notification::FrameNotification>,
}
// Release/acquire positions transfer exclusive slot ownership between one callback and one recorder.
unsafe impl Sync for AudioInput {}
impl AudioInput {
    pub fn new() -> Arc<Self> {
        Arc::new(Self {
            slots: (0..AUDIO_SLOTS)
                .map(|_| {
                    UnsafeCell::new(AudioBlock {
                        timestamp: 0,
                        frames: 0,
                        samples: [0.0; AUDIO_FRAMES * 2],
                    })
                })
                .collect::<Vec<_>>()
                .into_boxed_slice(),
            write: AtomicU64::new(0),
            read: AtomicU64::new(0),
            writer: AtomicBool::new(false),
            reader: AtomicBool::new(false),
            peak: AtomicU32::new(0),
            active: AtomicBool::new(false),
            overflow: AtomicBool::new(false),
            signal: crate::platform::infrastructure::notification::FrameNotification::new().ok(),
        })
    }
    pub fn activate(&self) -> Result<(), String> {
        if self
            .active
            .compare_exchange(false, true, Ordering::AcqRel, Ordering::Acquire)
            .is_err()
        {
            return Err("This audio input already has a recording consumer.".into());
        }
        // Absolute positions persist between recordings; startup discards pre-origin timestamps.
        self.overflow.store(false, Ordering::Release);
        Ok(())
    }
    pub fn capture(&self, source: Option<&[f32]>, frames: usize, timestamp: u64) {
        let peak = source
            .map_or(0.0, |samples| {
                samples
                    .iter()
                    .copied()
                    .filter(|value| value.is_finite())
                    .map(f32::abs)
                    .fold(0.0, f32::max)
            })
            .min(1.0);
        let previous = f32::from_bits(self.peak.load(Ordering::Relaxed));
        self.peak
            .store(peak.max(previous * 0.97).to_bits(), Ordering::Relaxed);
        if !self.active.load(Ordering::Acquire) {
            return;
        }
        let Some(_guard) = CallbackGuard::enter(&self.writer) else {
            self.overflow.store(true, Ordering::Release);
            return;
        };
        let mut offset = 0;
        while offset < frames {
            let write = self.write.load(Ordering::Relaxed);
            if write.wrapping_sub(self.read.load(Ordering::Acquire)) >= AUDIO_SLOTS as u64 {
                self.overflow.store(true, Ordering::Release);
                return;
            }
            let count = (frames - offset).min(AUDIO_FRAMES);
            let slot = unsafe { &mut *self.slots[write as usize % AUDIO_SLOTS].get() };
            slot.timestamp = timestamp.saturating_add(offset as u64 * 1_000_000_000 / 48000);
            slot.frames = count;
            for index in 0..count * 2 {
                slot.samples[index] = source
                    .and_then(|value| value.get(offset * 2 + index))
                    .copied()
                    .unwrap_or(0.0);
            }
            self.write.store(write.wrapping_add(1), Ordering::Release);
            offset += count;
        }
        if let Some(signal) = &self.signal {
            signal.notify();
        }
    }
    pub fn take(&self) -> Option<AudioBlock> {
        let _guard = CallbackGuard::enter(&self.reader)?;
        let read = self.read.load(Ordering::Relaxed);
        if read == self.write.load(Ordering::Acquire) {
            return None;
        }
        let value = unsafe { (&*self.slots[read as usize % AUDIO_SLOTS].get()).clone() };
        self.read.store(read.wrapping_add(1), Ordering::Release);
        Some(value)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn video_lease_prevents_overwrite_and_preserves_padding() {
        let input = VideoInput::new(128).unwrap();
        input.submit(&[1; 32], 10);
        let first = input.take().unwrap();
        assert!(input.take().is_none());
        input.submit(&[2; 32], 20);
        input.submit(&[3; 32], 30);
        input.submit(&[4; 32], 40);
        assert_eq!(&first.bytes[..32], &[1; 32]);
        assert!(first.bytes[32..].iter().all(|value| *value == 0));
        assert_eq!(input.dropped.load(Ordering::Relaxed), 1);
        drop(first);
        input.submit(&[4; 32], 40);
        for expected in [2, 3, 4] {
            let block = input.take().unwrap();
            assert_eq!(block.bytes[0], expected);
        }
        assert!(input.take().is_none());
        input.submit(&[0; 65], 0);
        assert!(input.oversized.load(Ordering::Acquire));
    }

    #[test]
    fn video_concurrent_handoff_keeps_each_frame_consistent() {
        let input = VideoInput::new(256).unwrap();
        let producer = Arc::clone(&input);
        let worker = std::thread::spawn(move || {
            for position in 1..=10000_u64 {
                producer.submit(&[position as u8; 128], position);
            }
            producer.active.store(false, Ordering::Release);
        });
        let mut previous = 0;
        loop {
            if let Some(block) = input.take() {
                assert!(block.timestamp > previous);
                assert!(
                    block.bytes[..128]
                        .iter()
                        .all(|value| *value == block.timestamp as u8)
                );
                previous = block.timestamp;
            } else if !input.active.load(Ordering::Acquire) {
                break;
            } else {
                std::thread::yield_now();
            }
        }
        worker.join().unwrap();
        assert!(previous > 0);
    }

    #[test]
    fn audio_is_pre_gain_bounded_and_restartable() {
        let input = AudioInput::new();
        input.capture(Some(&[0.5, -0.75]), 1, 100);
        assert!(input.take().is_none());
        assert_eq!(f32::from_bits(input.peak.load(Ordering::Relaxed)), 0.75);
        input.activate().unwrap();
        assert!(input.activate().is_err());
        for index in 0..AUDIO_SLOTS {
            input.capture(Some(&[0.5, -0.75]), 1, index as u64);
        }
        input.capture(None, 1, 99);
        assert!(input.overflow.load(Ordering::Acquire));
        for index in 0..AUDIO_SLOTS {
            let block = input.take().unwrap();
            assert_eq!(block.timestamp, index as u64);
            assert_eq!(&block.samples[..2], &[0.5, -0.75]);
        }
        input.active.store(false, Ordering::Release);
        input.activate().unwrap();
        assert!(!input.overflow.load(Ordering::Acquire));
        input.capture(None, AUDIO_FRAMES + 1, 1000);
        assert_eq!(input.take().unwrap().frames, AUDIO_FRAMES);
        let last = input.take().unwrap();
        assert_eq!(last.frames, 1);
        assert_eq!(
            last.timestamp,
            1000 + AUDIO_FRAMES as u64 * 1_000_000_000 / 48000
        );
    }
}
