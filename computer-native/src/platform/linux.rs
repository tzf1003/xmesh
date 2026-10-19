use super::Display;
#[path = "wayland.rs"]
mod wayland_backend;
use dbus::blocking::stdintf::org_freedesktop_dbus::Properties;
use std::time::Duration;
pub use wayland_backend::PortalInput;
use x11rb::{
    connection::Connection,
    protocol::{
        randr::ConnectionExt as RandrExt,
        screensaver::{ConnectionExt as SaverExt, State},
        xproto::{ConnectionExt, ImageFormat, ImageOrder},
    },
};

fn wayland() -> bool {
    std::env::var_os("WAYLAND_DISPLAY").is_some()
}

pub fn displays() -> Vec<Display> {
    if wayland() {
        return wayland_backend::displays();
    }
    let Ok((conn, n)) = x11rb::connect(None) else {
        return vec![];
    };
    let screen = &conn.setup().roots[n];
    if let Ok(cookie) = conn.randr_get_monitors(screen.root, true) {
        if let Ok(reply) = cookie.reply() {
            if !reply.monitors.is_empty() {
                return reply
                    .monitors
                    .iter()
                    .map(|m| Display {
                        id: m.name.to_string(),
                        name: format!("Display {}", m.name),
                        x: m.x as i32,
                        y: m.y as i32,
                        width: m.width as i32,
                        height: m.height as i32,
                        scale: 1.0,
                        rotation: 0,
                    })
                    .collect();
            }
        }
    }
    vec![Display {
        id: screen.root.to_string(),
        name: "X11 screen".into(),
        x: 0,
        y: 0,
        width: screen.width_in_pixels as i32,
        height: screen.height_in_pixels as i32,
        scale: 1.0,
        rotation: 0,
    }]
}

pub fn screen_permission() -> bool {
    if wayland() {
        return wayland_backend::available();
    }
    !displays().is_empty()
}
pub fn input_permission() -> bool {
    screen_permission()
}
pub fn interactive() -> bool {
    std::env::var_os("WAYLAND_DISPLAY").is_some() || std::env::var_os("DISPLAY").is_some()
}
pub fn locked() -> bool {
    if !interactive() {
        return true;
    }
    if let Ok(conn) = dbus::blocking::Connection::new_system() {
        let p = conn.with_proxy(
            "org.freedesktop.login1",
            "/org/freedesktop/login1",
            Duration::from_millis(200),
        );
        let session: Result<(dbus::Path<'static>,), _> = p.method_call(
            "org.freedesktop.login1.Manager",
            "GetSessionByPID",
            (std::process::id(),),
        );
        if let Ok((path,)) = session {
            let p = conn.with_proxy("org.freedesktop.login1", path, Duration::from_millis(200));
            if let Ok(locked) = p.get::<bool>("org.freedesktop.login1.Session", "LockedHint") {
                return locked;
            }
        }
    }
    if wayland() {
        return !wayland_backend::available();
    }
    if let Ok((conn, n)) = x11rb::connect(None) {
        if let Ok(cookie) = conn.screensaver_query_info(conn.setup().roots[n].root) {
            if let Ok(reply) = cookie.reply() {
                return reply.state != u8::from(State::OFF);
            }
        }
    }
    true
}

pub fn capture(display: &Display) -> Result<(i32, i32, Vec<u8>), &'static str> {
    if wayland() {
        return wayland_backend::capture(display);
    }
    let (conn, n) = x11rb::connect(None).map_err(|_| "x11_unavailable")?;
    let setup = conn.setup();
    let screen = &setup.roots[n];
    if display.width < 1 || display.height < 1 || display.width > 16384 || display.height > 16384 {
        return Err("invalid_display");
    }
    let reply = conn
        .get_image(
            ImageFormat::Z_PIXMAP,
            screen.root,
            display.x.try_into().map_err(|_| "invalid_display")?,
            display.y.try_into().map_err(|_| "invalid_display")?,
            display.width as u16,
            display.height as u16,
            u32::MAX,
        )
        .map_err(|_| "capture_failed")?
        .reply()
        .map_err(|_| "capture_failed")?;
    let format = setup
        .pixmap_formats
        .iter()
        .find(|v| v.depth == reply.depth)
        .ok_or("pixel_format")?;
    let visual = screen
        .allowed_depths
        .iter()
        .flat_map(|d| &d.visuals)
        .find(|v| v.visual_id == reply.visual)
        .ok_or("pixel_format")?;
    let bpp = format.bits_per_pixel as usize / 8;
    if bpp != 3 && bpp != 4 {
        return Err("pixel_format");
    }
    let pad = format.scanline_pad as usize;
    let stride =
        ((display.width as usize * format.bits_per_pixel as usize).div_ceil(pad) * pad) / 8;
    if reply.data.len() < stride * display.height as usize {
        return Err("pixel_format");
    }
    let channel = |pixel: u32, mask: u32| -> u8 {
        if mask == 0 {
            return 0;
        }
        let shift = mask.trailing_zeros();
        (((pixel & mask) >> shift) as u64 * 255 / (mask >> shift) as u64) as u8
    };
    let mut rgba = Vec::with_capacity(display.width as usize * display.height as usize * 4);
    for row in reply
        .data
        .chunks_exact(stride)
        .take(display.height as usize)
    {
        for p in row[..display.width as usize * bpp].chunks_exact(bpp) {
            let mut b = [0; 4];
            if setup.image_byte_order == ImageOrder::LSB_FIRST {
                b[..bpp].copy_from_slice(p);
            } else {
                b[4 - bpp..].copy_from_slice(p);
            }
            let pixel = if setup.image_byte_order == ImageOrder::LSB_FIRST {
                u32::from_le_bytes(b)
            } else {
                u32::from_be_bytes(b)
            };
            rgba.extend([
                channel(pixel, visual.blue_mask),
                channel(pixel, visual.green_mask),
                channel(pixel, visual.red_mask),
                255,
            ]);
        }
    }
    Ok((display.width, display.height, rgba))
}
