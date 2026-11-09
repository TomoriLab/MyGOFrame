use std::sync::atomic::{AtomicBool, Ordering};

/// Excludes reentrant callbacks without locks or allocation on the realtime path.
pub(crate) struct CallbackGuard<'gate>(&'gate AtomicBool);

impl<'gate> CallbackGuard<'gate> {
    pub(crate) fn enter(gate: &'gate AtomicBool) -> Option<Self> {
        gate.compare_exchange(false, true, Ordering::Acquire, Ordering::Relaxed)
            .ok()
            .map(|_| Self(gate))
    }
}

impl Drop for CallbackGuard<'_> {
    fn drop(&mut self) {
        self.0.store(false, Ordering::Release);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reentrant_callback_is_rejected_and_return_restores_entry() {
        let gate = AtomicBool::new(false);
        let guard = CallbackGuard::enter(&gate).unwrap();
        assert!(CallbackGuard::enter(&gate).is_none());
        drop(guard);
        assert!(CallbackGuard::enter(&gate).is_some());
        assert!(!gate.load(Ordering::Acquire));
    }
}
