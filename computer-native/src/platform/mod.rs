use serde::Serialize;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};

pub const INPUT_MARKER: u64 = 0x584d455348;
pub(crate) static INPUT_SERIAL: AtomicU64 = AtomicU64::new(0);
pub(crate) static INPUT_MONITOR_READY: AtomicBool = AtomicBool::new(false);
pub fn user_input_serial() -> u64 {
    INPUT_SERIAL.load(Ordering::Acquire)
}
pub fn input_monitor_available() -> bool {
    INPUT_MONITOR_READY.load(Ordering::Acquire)
}

#[cfg(target_os = "macos")]
mod macos;
#[cfg(target_os = "macos")]
pub use macos::*;
#[cfg(target_os = "windows")]
mod windows;
#[cfg(target_os = "windows")]
pub use windows::*;
#[cfg(target_os = "linux")]
mod linux;
#[cfg(target_os = "linux")]
pub use linux::*;

#[derive(Clone, Debug, Serialize)]
pub struct Display {
    pub id: String,
    pub name: String,
    pub x: i32,
    pub y: i32,
    pub width: i32,
    pub height: i32,
    pub scale: f64,
    pub rotation: i32,
}

pub fn layout(displays: &[Display]) -> String {
    // Complete topology, including scaling and origins, is the version identity.
    serde_json::to_string(displays).unwrap_or_default()
}
