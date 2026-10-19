use serde::Serialize;

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
