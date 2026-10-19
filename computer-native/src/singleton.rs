use fs2::FileExt;
use std::{
    fs::{self, File, OpenOptions},
    io,
};

pub fn acquire() -> io::Result<File> {
    #[cfg(unix)]
    let suffix = unsafe { libc::geteuid() }.to_string();
    #[cfg(windows)]
    let suffix = crate::platform::session_id().to_string();
    let dir = std::env::temp_dir().join(format!("xmesh-computer-{suffix}"));
    match fs::create_dir(&dir) {
        Ok(()) => {}
        Err(e) if e.kind() == io::ErrorKind::AlreadyExists => {}
        Err(e) => return Err(e),
    }
    let st = fs::symlink_metadata(&dir)?;
    if !st.is_dir() || st.file_type().is_symlink() {
        return Err(io::Error::other("desktop lock directory is invalid"));
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::{MetadataExt, PermissionsExt};
        if st.uid() != unsafe { libc::geteuid() } {
            return Err(io::Error::other("desktop lock owner mismatch"));
        }
        fs::set_permissions(&dir, fs::Permissions::from_mode(0o700))?;
    }
    let mut options = OpenOptions::new();
    options.read(true).write(true).create(true).truncate(false);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600).custom_flags(libc::O_NOFOLLOW);
    }
    let file = options.open(dir.join("executor.lock"))?;
    if !file.metadata()?.is_file() {
        return Err(io::Error::other("desktop lock is not a regular file"));
    }
    file.try_lock_exclusive()?;
    Ok(file)
}
