use crate::{ffi, make_c_string};
use std::ffi::{c_char, c_void};
use std::fs::File;
use std::io::{self, Read, Write};
use std::os::fd::{AsRawFd, FromRawFd, IntoRawFd};
use std::ptr;
use std::sync::{Arc, Mutex};

unsafe extern "C" {
    fn eventfd(initial: u32, flags: i32) -> i32;
}

/// A bounded kernel counter wakes the UI without retaining frames or calling foreign runtimes.
pub(crate) struct FrameNotification {
    file: File,
}

impl FrameNotification {
    pub(crate) fn new() -> io::Result<Self> {
        // A nonblocking descriptor prevents a stalled UI from blocking the producer.
        let descriptor = unsafe { eventfd(0, ffi::O_CLOEXEC as i32 | ffi::O_NONBLOCK as i32) };
        if descriptor < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(Self {
            file: unsafe { File::from_raw_fd(descriptor) },
        })
    }

    pub(crate) fn descriptor(&self) -> i32 {
        self.file.as_raw_fd()
    }
    pub(crate) fn clear(&self) {
        let mut bytes = [0_u8; 8];
        loop {
            match (&self.file).read(&mut bytes) {
                Ok(8) => continue,
                Err(error) if error.kind() == io::ErrorKind::Interrupted => continue,
                _ => break,
            }
        }
    }
    pub(crate) fn notify(&self) {
        loop {
            match (&self.file).write(&1_u64.to_ne_bytes()) {
                Err(error) if error.kind() == io::ErrorKind::Interrupted => continue,
                // Saturation still leaves the descriptor readable. Ownership keeps it valid.
                _ => return,
            }
        }
    }
}

#[derive(Default)]
pub(crate) struct NotificationSlot(Mutex<Option<Arc<FrameNotification>>>);

impl NotificationSlot {
    pub(crate) fn attach(&self, notification: Arc<FrameNotification>) {
        let mut current = self.0.lock().unwrap();
        *current = Some(notification);
        // The capture worker may have published a frame before attachment.
        current.as_ref().unwrap().notify();
    }

    pub(crate) fn notify(&self) {
        if let Some(notification) = self.0.lock().unwrap().as_ref() {
            notification.notify();
        }
    }
}

/// # Safety
/// A non-null error pointer must address writable pointer storage.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_notification_create(error: *mut *mut c_char) -> *mut c_void {
    if !error.is_null() {
        unsafe { *error = ptr::null_mut() };
    }
    match FrameNotification::new() {
        Ok(notification) => Box::into_raw(Box::new(Arc::new(notification))).cast(),
        Err(failure) => {
            if !error.is_null() {
                unsafe { *error = make_c_string(format!("Create frame notification: {failure}")) };
            }
            ptr::null_mut()
        }
    }
}

/// # Safety
/// The handle must remain live during the call. The caller owns the returned descriptor.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_notification_duplicate(handle: *mut c_void) -> i32 {
    if handle.is_null() {
        return -1;
    }
    let notification = unsafe { &*handle.cast::<Arc<FrameNotification>>() };
    notification
        .file
        .try_clone()
        .map(IntoRawFd::into_raw_fd)
        .unwrap_or(-1)
}

/// # Safety
/// The handle must be null or an unreleased notification owner. Capture attachments retain it.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_notification_destroy(handle: *mut c_void) {
    if !handle.is_null() {
        unsafe { drop(Box::from_raw(handle.cast::<Arc<FrameNotification>>())) };
    }
}

/// # Safety
/// The caller keeps the notification handle live until this reference is cloned.
pub(crate) unsafe fn retain(handle: *mut c_void) -> Arc<FrameNotification> {
    unsafe { Arc::clone(&*handle.cast::<Arc<FrameNotification>>()) }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Read;

    #[test]
    fn notifications_coalesce_and_survive_the_original_owner() {
        let notification = Arc::new(FrameNotification::new().unwrap());
        let mut reader = notification.file.try_clone().unwrap();
        let slot = NotificationSlot::default();
        slot.attach(Arc::clone(&notification));
        for _ in 0..100 {
            slot.notify();
        }
        drop(notification);
        let mut bytes = [0; 8];
        reader.read_exact(&mut bytes).unwrap();
        assert_eq!(u64::from_ne_bytes(bytes), 101);
        assert_eq!(
            reader.read(&mut bytes).unwrap_err().kind(),
            io::ErrorKind::WouldBlock
        );
        slot.notify();
        reader.read_exact(&mut bytes).unwrap();
        assert_eq!(u64::from_ne_bytes(bytes), 1);
        drop(slot);
        assert_eq!(
            reader.read(&mut bytes).unwrap_err().kind(),
            io::ErrorKind::WouldBlock
        );
    }

    #[test]
    fn concurrent_notifications_do_not_lose_wakeups() {
        let notification = Arc::new(FrameNotification::new().unwrap());
        let mut reader = notification.file.try_clone().unwrap();
        std::thread::scope(|scope| {
            for _ in 0..4 {
                let notification = &notification;
                scope.spawn(move || {
                    for _ in 0..1000 {
                        notification.notify();
                    }
                });
            }
        });
        let mut bytes = [0; 8];
        reader.read_exact(&mut bytes).unwrap();
        assert_eq!(u64::from_ne_bytes(bytes), 4000);
    }
}
