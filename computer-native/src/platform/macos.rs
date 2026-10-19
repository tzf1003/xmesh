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
    fn CGEventSetIntegerValueField(event: *mut c_void, field: u32, value: i64);
    fn CGEventGetIntegerValueField(event: *const c_void, field: u32) -> i64;
    fn CGEventTapCreate(
        tap: u32,
        placement: u32,
        options: u32,
        mask: u64,
        callback: extern "C" fn(*mut c_void, u32, *mut c_void, *mut c_void) -> *mut c_void,
        context: *mut c_void,
    ) -> *mut c_void;
    fn CGEventTapEnable(tap: *mut c_void, enabled: bool);
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
    fn CFMachPortCreateRunLoopSource(
        allocator: *const c_void,
        port: *mut c_void,
        order: isize,
    ) -> *mut c_void;
    fn CFRunLoopGetCurrent() -> *mut c_void;
    fn CFRunLoopAddSource(loop_: *mut c_void, source: *mut c_void, mode: *const c_void);
    fn CFRunLoopRun();
    static kCFRunLoopCommonModes: *const c_void;
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
        CGEventSetIntegerValueField(event, 42, super::INPUT_MARKER as i64);
        CGEventPost(0, event);
        CFRelease(event);
    }
    Ok(())
}

static INPUT_TAP: std::sync::atomic::AtomicPtr<c_void> =
    std::sync::atomic::AtomicPtr::new(std::ptr::null_mut());
extern "C" fn input_event(
    _: *mut c_void,
    kind: u32,
    event: *mut c_void,
    _: *mut c_void,
) -> *mut c_void {
    use std::sync::atomic::Ordering;
    if kind >= 0xfffffffe {
        let tap = INPUT_TAP.load(Ordering::Acquire);
        if !tap.is_null() {
            unsafe { CGEventTapEnable(tap, true) }
        };
        return event;
    }
    if !event.is_null()
        && unsafe { CGEventGetIntegerValueField(event, 42) } != super::INPUT_MARKER as i64
        && unsafe { CGEventGetIntegerValueField(event, 41) } != i64::from(std::process::id())
    {
        super::INPUT_SERIAL.fetch_add(1, Ordering::AcqRel);
    }
    event
}
pub fn start_input_monitor() {
    static START: std::sync::Once = std::sync::Once::new();
    START.call_once(|| {
        std::thread::spawn(|| unsafe {
            let mask = [1u32, 3, 10, 12, 25]
                .into_iter()
                .fold(0u64, |m, v| m | (1u64 << v));
            let tap = CGEventTapCreate(1, 0, 1, mask, input_event, std::ptr::null_mut());
            if tap.is_null() {
                return;
            }
            let source = CFMachPortCreateRunLoopSource(std::ptr::null(), tap, 0);
            if source.is_null() {
                CFRelease(tap);
                return;
            }
            INPUT_TAP.store(tap, std::sync::atomic::Ordering::Release);
            CFRunLoopAddSource(CFRunLoopGetCurrent(), source, kCFRunLoopCommonModes);
            CGEventTapEnable(tap, true);
            super::INPUT_MONITOR_READY.store(true, std::sync::atomic::Ordering::Release);
            CFRunLoopRun();
            super::INPUT_MONITOR_READY.store(false, std::sync::atomic::Ordering::Release);
            CFRelease(source);
            CFRelease(tap);
        });
    });
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
        let width = (f64::from(display.width) * display.scale).round() as usize;
        let height = (f64::from(display.height) * display.scale).round() as usize;
        if !super::valid_capture_size(width, height) {
            return Err("capture_size");
        }
        config.set_width(width);
        config.set_height(height);
        config.set_pixel_format(cv::PixelFormat::_32_BGRA);
        config.set_shows_cursor(true);
        let mut sample = sc::ScreenshotManager::capture_sample_buf(&filter, &config)
            .await
            .map_err(|_| "capture_failed")?;
        let pixels = sample.image_buf_mut().ok_or("capture_empty")?;
        let width = pixels.width();
        let height = pixels.height();
        if !super::valid_capture_size(width, height)
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
