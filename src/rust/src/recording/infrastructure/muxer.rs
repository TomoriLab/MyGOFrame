use super::codec::{Codec, Packet, checked};
use crate::ffi;
use std::ffi::{CString, c_void};
use std::fs::{File, OpenOptions};
use std::io::{Seek, SeekFrom, Write};
use std::path::{Path, PathBuf};
use std::ptr;

struct Output {
    file: File,
}
unsafe extern "C" fn write_packet(opaque: *mut c_void, bytes: *const u8, count: i32) -> i32 {
    if opaque.is_null() || bytes.is_null() || count < 0 {
        return -22;
    }
    let output = unsafe { &mut *opaque.cast::<Output>() };
    let data = unsafe { std::slice::from_raw_parts(bytes, count as usize) };
    match output.file.write_all(data) {
        Ok(()) => count,
        Err(error) => -error.raw_os_error().unwrap_or(5).abs(),
    }
}
unsafe extern "C" fn seek(opaque: *mut c_void, offset: i64, whence: i32) -> i64 {
    if opaque.is_null() {
        return -22;
    }
    let output = unsafe { &mut *opaque.cast::<Output>() };
    if whence & ffi::AVSEEK_SIZE as i32 != 0 {
        return output
            .file
            .metadata()
            .ok()
            .and_then(|value| i64::try_from(value.len()).ok())
            .unwrap_or(-5);
    }
    let position = match whence & !(ffi::AVSEEK_FORCE as i32) {
        0 if offset >= 0 => SeekFrom::Start(offset as u64),
        1 => SeekFrom::Current(offset),
        2 => SeekFrom::End(offset),
        _ => return -22,
    };
    output
        .file
        .seek(position)
        .ok()
        .and_then(|value| i64::try_from(value).ok())
        .unwrap_or(-5)
}
unsafe extern "C" {
    fn renameat2(
        old_directory: i32,
        old_path: *const i8,
        new_directory: i32,
        new_path: *const i8,
        flags: u32,
    ) -> i32;
}

pub(crate) struct Muxer {
    context: *mut ffi::AVFormatContext,
    io: *mut ffi::AVIOContext,
    output: Box<Output>,
    temporary: PathBuf,
    destination: PathBuf,
    pub video_stream: *mut ffi::AVStream,
    pub audio_stream: *mut ffi::AVStream,
    header_written: bool,
}
impl Drop for Muxer {
    fn drop(&mut self) {
        unsafe {
            if !self.context.is_null() {
                (*self.context).pb = ptr::null_mut();
                ffi::avformat_free_context(self.context);
            }
            if !self.io.is_null() {
                ffi::av_free((*self.io).buffer.cast());
                (*self.io).buffer = ptr::null_mut();
                ffi::avio_context_free(&mut self.io);
            }
        }
    }
}
impl Muxer {
    pub fn new(destination: &str, video: &Codec, audio: Option<&Codec>) -> Result<Self, String> {
        let destination = PathBuf::from(destination);
        if destination.exists() {
            return Err("The recording output file already exists.".into());
        }
        if !destination.is_absolute() {
            return Err("The recording output path must be absolute.".into());
        }
        if let Some(parent) = destination.parent() {
            std::fs::create_dir_all(parent)
                .map_err(|error| format!("Create recording directory: {error}"))?;
        }
        let temporary = PathBuf::from(format!("{}.part", destination.display()));
        let file = OpenOptions::new()
            .write(true)
            .read(true)
            .create_new(true)
            .open(&temporary)
            .map_err(|error| format!("Create recording output: {error}"))?;
        let mut value = Self {
            context: ptr::null_mut(),
            io: ptr::null_mut(),
            output: Box::new(Output { file }),
            temporary,
            destination,
            video_stream: ptr::null_mut(),
            audio_stream: ptr::null_mut(),
            header_written: false,
        };
        let container = if value
            .destination
            .extension()
            .and_then(|extension| extension.to_str())
            .is_some_and(|extension| extension.eq_ignore_ascii_case("mp4"))
        {
            c"mp4"
        } else {
            c"matroska"
        };
        unsafe {
            checked(
                ffi::avformat_alloc_output_context2(
                    &mut value.context,
                    ptr::null(),
                    container.as_ptr(),
                    ptr::null(),
                ),
                "Allocate recording container",
            )?;
            if value.context.is_null() {
                return Err("Unable to allocate the recording muxer.".into());
            }
            let buffer = ffi::av_malloc(65536).cast::<u8>();
            if buffer.is_null() {
                return Err("Unable to allocate recording file buffers.".into());
            }
            value.io = ffi::avio_alloc_context(
                buffer,
                65536,
                1,
                (value.output.as_mut() as *mut Output).cast(),
                None,
                Some(write_packet),
                Some(seek),
            );
            if value.io.is_null() {
                ffi::av_free(buffer.cast());
                return Err("Unable to allocate recording IO context.".into());
            }
            (*value.context).pb = value.io;
            (*value.context).flags |= ffi::AVFMT_FLAG_CUSTOM_IO as i32;
            (*value.context).max_interleave_delta = 500_000;
            value.video_stream = value.add_stream(video)?;
            (*value.video_stream).avg_frame_rate = (*video.0).framerate;
            if let Some(audio) = audio {
                value.audio_stream = value.add_stream(audio)?;
            }
            checked(
                ffi::avformat_write_header(value.context, ptr::null_mut()),
                "Write recording header",
            )?;
            value.header_written = true;
        }
        Ok(value)
    }
    unsafe fn add_stream(&mut self, codec: &Codec) -> Result<*mut ffi::AVStream, String> {
        let stream = unsafe { ffi::avformat_new_stream(self.context, ptr::null()) };
        if stream.is_null() {
            return Err("Unable to allocate a recording stream.".into());
        }
        unsafe {
            (*stream).time_base = (*codec.0).time_base;
            checked(
                ffi::avcodec_parameters_from_context((*stream).codecpar, codec.0),
                "Copy recording codec parameters",
            )?;
        }
        Ok(stream)
    }
    pub fn drain(
        &mut self,
        codec: &Codec,
        stream: *mut ffi::AVStream,
        packet: &mut Packet,
    ) -> Result<u64, String> {
        let mut count = 0;
        loop {
            let code = unsafe { ffi::avcodec_receive_packet(codec.0, packet.0) };
            if code == -11 || code == ffi::MGF_AVERROR_EOF {
                break;
            }
            checked(code, "Receive recording packet")?;
            unsafe {
                (*packet.0).stream_index = (*stream).index;
                ffi::av_packet_rescale_ts(packet.0, (*codec.0).time_base, (*stream).time_base);
                let code = ffi::av_interleaved_write_frame(self.context, packet.0);
                ffi::av_packet_unref(packet.0);
                checked(code, "Write recording packet")?;
            }
            count += 1;
        }
        Ok(count)
    }
    pub fn bytes(&self) -> u64 {
        self.output.file.metadata().map_or(0, |value| value.len())
    }
    pub fn cancel(&mut self) -> Result<(), String> {
        // Only this session's exclusive temporary file is removed when no video was recorded.
        std::fs::remove_file(&self.temporary)
            .map_err(|error| format!("Remove empty recording: {error}"))
    }
    pub fn finish(&mut self) -> Result<(), String> {
        if !self.header_written {
            return Err("Recording header was not written.".into());
        }
        unsafe {
            checked(
                ffi::av_write_trailer(self.context),
                "Finalize recording container",
            )?;
            self.header_written = false;
            ffi::avio_flush(self.io);
            checked((*self.io).error, "Flush recording output")?;
        }
        self.output
            .file
            .sync_all()
            .map_err(|error| format!("Synchronize recording output: {error}"))?;
        rename_without_overwrite(&self.temporary, &self.destination)
    }
}
fn rename_without_overwrite(source: &Path, destination: &Path) -> Result<(), String> {
    use std::os::unix::ffi::OsStrExt;
    let source = CString::new(source.as_os_str().as_bytes())
        .map_err(|_| "Invalid recording output path.")?;
    let destination = CString::new(destination.as_os_str().as_bytes())
        .map_err(|_| "Invalid recording output path.")?;
    if unsafe { renameat2(-100, source.as_ptr(), -100, destination.as_ptr(), 1) } != 0 {
        return Err(format!(
            "Publish recording without overwrite: {}",
            std::io::Error::last_os_error()
        ));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn native_file_callback_propagates_disk_full() {
        let mut output = Output {
            file: OpenOptions::new().write(true).open("/dev/full").unwrap(),
        };
        let bytes = [0_u8; 32];
        let result = unsafe {
            write_packet(
                (&mut output as *mut Output).cast(),
                bytes.as_ptr(),
                bytes.len() as i32,
            )
        };
        assert_eq!(result, -28);
    }

    #[test]
    fn publication_never_overwrites_existing_file() {
        let directory = std::env::temp_dir().join(format!(
            "mygoframe-publish-{}-{}",
            std::process::id(),
            crate::media::infrastructure::packets::monotonic_ns()
        ));
        std::fs::create_dir(&directory).unwrap();
        let source = directory.join("output.part");
        let target = directory.join("output.mkv");
        std::fs::write(&source, b"new").unwrap();
        std::fs::write(&target, b"original").unwrap();
        assert!(rename_without_overwrite(&source, &target).is_err());
        assert_eq!(std::fs::read(&target).unwrap(), b"original");
        assert_eq!(std::fs::read(&source).unwrap(), b"new");
        std::fs::remove_file(&target).unwrap();
        rename_without_overwrite(&source, &target).unwrap();
        assert_eq!(std::fs::read(&target).unwrap(), b"new");
        assert!(!source.exists());
        std::fs::remove_dir_all(directory).unwrap();
    }
}
