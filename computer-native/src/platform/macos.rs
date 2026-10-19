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
    fn CGSessionCopyCurrentDictionary() -> *const c_void;
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
