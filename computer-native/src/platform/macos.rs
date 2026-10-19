use super::Display;
use std::ffi::c_void;

#[repr(C)]
#[derive(Clone, Copy)]
struct Point {
    x: f64,
    y: f64,
}
#[repr(C)]
#[derive(Clone, Copy)]
struct Size {
    width: f64,
    height: f64,
}
#[repr(C)]
struct Rect {
    origin: Point,
    size: Size,
}

#[link(name = "CoreGraphics", kind = "framework")]
extern "C" {
    fn CGGetActiveDisplayList(max: u32, displays: *mut u32, count: *mut u32) -> i32;
    fn CGDisplayBounds(display: u32) -> Rect;
    fn CGDisplayPixelsWide(display: u32) -> usize;
    fn CGDisplayRotation(display: u32) -> f64;
    fn CGPreflightScreenCaptureAccess() -> bool;
    fn CGRequestScreenCaptureAccess() -> bool;
    fn CGSessionCopyCurrentDictionary() -> *const c_void;
    fn CGEventCreate(source: *const c_void) -> *mut c_void;
    fn CGEventGetLocation(event: *const c_void) -> Point;
    fn CGEventSetLocation(event: *mut c_void, point: Point);
    fn CGEventCreateScrollWheelEvent(
        source: *const c_void,
        units: u32,
        count: u32,
        wheel1: i32,
        ...
    ) -> *mut c_void;
    fn CGEventPost(tap: u32, event: *const c_void);
}
#[link(name = "ApplicationServices", kind = "framework")]
extern "C" {
    fn AXIsProcessTrusted() -> bool;
}
#[link(name = "CoreFoundation", kind = "framework")]
extern "C" {
    fn CFStringCreateWithCString(
        allocator: *const c_void,
        text: *const i8,
        encoding: u32,
    ) -> *const c_void;
    fn CFDictionaryGetValue(dictionary: *const c_void, key: *const c_void) -> *const c_void;
    fn CFBooleanGetValue(value: *const c_void) -> bool;
    fn CFRelease(value: *const c_void);
}

pub fn displays() -> Vec<Display> {
    let mut ids = [0u32; 32];
    let mut count = 0;
    // CGGetActiveDisplayList initializes only count entries on success.
    if unsafe { CGGetActiveDisplayList(ids.len() as u32, ids.as_mut_ptr(), &mut count) } != 0 {
        return vec![];
    }
    ids[..(count as usize).min(ids.len())]
        .iter()
        .map(|id| {
            let bounds = unsafe { CGDisplayBounds(*id) };
            Display {
                id: id.to_string(),
                name: format!("Display {id}"),
                x: bounds.origin.x as i32,
                y: bounds.origin.y as i32,
                width: bounds.size.width as i32,
                height: bounds.size.height as i32,
                scale: unsafe { CGDisplayPixelsWide(*id) } as f64 / bounds.size.width.max(1.0),
                rotation: unsafe { CGDisplayRotation(*id) } as i32,
            }
        })
        .collect()
}

pub fn screen_permission() -> bool {
    unsafe { CGPreflightScreenCaptureAccess() }
}
pub fn request_screen_permission() {
    unsafe {
        CGRequestScreenCaptureAccess();
    }
}

pub fn scroll(length: i32, axis: enigo::Axis) -> Result<(), &'static str> {
    unsafe {
        let current = CGEventCreate(std::ptr::null());
        if current.is_null() {
            return Err("input_failed");
        }
        let location = CGEventGetLocation(current);
        CFRelease(current);
        let (dy, dx) = if axis == enigo::Axis::Vertical {
            (length, 0)
        } else {
            (0, length)
        };
        let event = CGEventCreateScrollWheelEvent(std::ptr::null(), 1, 2, dy, dx, 0i32);
        if event.is_null() {
            return Err("input_failed");
        }
        CGEventSetLocation(event, location);
        CGEventPost(0, event);
        CFRelease(event);
    }
    Ok(())
}
pub fn input_permission() -> bool {
    unsafe { AXIsProcessTrusted() }
}
pub fn interactive() -> bool {
    !displays().is_empty()
}
pub fn locked() -> bool {
    unsafe {
        let dictionary = CGSessionCopyCurrentDictionary();
        if dictionary.is_null() {
            return true;
        }
        let key = CFStringCreateWithCString(
            std::ptr::null(),
            c"CGSSessionScreenIsLocked".as_ptr(),
            0x08000100,
        );
        let value = CFDictionaryGetValue(dictionary, key);
        let locked = !value.is_null() && CFBooleanGetValue(value);
        CFRelease(key);
        CFRelease(dictionary);
        locked
    }
}

pub fn capture(display: &Display) -> Result<(i32, i32, Vec<u8>), &'static str> {
    use cidre::{cv, ns, sc};
    futures::executor::block_on(async {
        let content = sc::ShareableContent::current()
            .await
            .map_err(|_| "capture_unavailable")?;
        let displays = content.displays();
        let target = displays
            .iter()
            .find(|d| d.display_id().0.to_string() == display.id)
            .ok_or("display_not_found")?;
        let filter = sc::ContentFilter::with_display_excluding_windows(target, &ns::Array::new());
        let mut config = sc::StreamCfg::new();
        config.set_width((f64::from(display.width) * display.scale).round() as usize);
        config.set_height((f64::from(display.height) * display.scale).round() as usize);
        config.set_pixel_format(cv::PixelFormat::_32_BGRA);
        config.set_shows_cursor(true);
        let mut sample = sc::ScreenshotManager::capture_sample_buf(&filter, &config)
            .await
            .map_err(|_| "capture_failed")?;
        let pixels = sample.image_buf_mut().ok_or("capture_empty")?;
        let width = pixels.width();
        let height = pixels.height();
        if width < 1
            || height < 1
            || width > 16384
            || height > 16384
            || pixels.pixel_format() != cv::PixelFormat::_32_BGRA
        {
            return Err("capture_format");
        }
        unsafe {
            pixels
                .lock_base_addr(cv::pixel_buffer::LockFlags::READ_ONLY)
                .result()
                .map_err(|_| "capture_lock")?;
        }
        let ptr = pixels.plane_base_address(0);
        let stride = pixels.plane_bytes_per_row(0);
        let result = if ptr.is_null() || stride < width * 4 {
            Err("capture_format")
        } else {
            let mut data = Vec::with_capacity(width * height * 4);
            for row in 0..height {
                // CoreVideo owns the locked buffer until unlock below. Each row is
                // copied without its alignment padding into the bounded output.
                let line = unsafe { std::slice::from_raw_parts(ptr.add(row * stride), width * 4) };
                data.extend_from_slice(line);
            }
            Ok((width as i32, height as i32, data))
        };
        let unlocked = unsafe {
            pixels
                .unlock_lock_base_addr(cv::pixel_buffer::LockFlags::READ_ONLY)
                .result()
        };
        unlocked.map_err(|_| "capture_unlock")?;
        result
    })
}
