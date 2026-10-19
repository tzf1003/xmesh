use super::Display;
use std::ffi::c_void;

#[repr(C)]
#[derive(Default)]
struct Rect {
    left: i32,
    top: i32,
    right: i32,
    bottom: i32,
}
#[repr(C)]
struct MonitorInfo {
    size: u32,
    monitor: Rect,
    work: Rect,
    flags: u32,
}

#[link(name = "user32")]
extern "system" {
    fn EnumDisplayMonitors(
        dc: *mut c_void,
        clip: *const Rect,
        callback: unsafe extern "system" fn(*mut c_void, *mut c_void, *mut Rect, isize) -> i32,
        data: isize,
    ) -> i32;
    fn GetMonitorInfoW(monitor: *mut c_void, info: *mut MonitorInfo) -> i32;
    fn OpenInputDesktop(flags: u32, inherit: i32, access: u32) -> *mut c_void;
    fn GetUserObjectInformationW(
        handle: *mut c_void,
        index: i32,
        buffer: *mut c_void,
        len: u32,
        needed: *mut u32,
    ) -> i32;
    fn CloseDesktop(desktop: *mut c_void) -> i32;
    fn SetThreadDpiAwarenessContext(value: isize) -> isize;
}
#[link(name = "kernel32")]
extern "system" {
    fn GetCurrentProcessId() -> u32;
    fn ProcessIdToSessionId(pid: u32, session: *mut u32) -> i32;
}

unsafe extern "system" fn monitor(
    handle: *mut c_void,
    _: *mut c_void,
    _: *mut Rect,
    data: isize,
) -> i32 {
    let displays = &mut *(data as *mut Vec<Display>);
    let mut info = MonitorInfo {
        size: std::mem::size_of::<MonitorInfo>() as u32,
        monitor: Rect::default(),
        work: Rect::default(),
        flags: 0,
    };
    if GetMonitorInfoW(handle, &mut info) != 0 {
        // scap identifies displays by the same HMONITOR numeric handle.
        let id = handle as usize as u32;
        displays.push(Display {
            id: id.to_string(),
            name: format!("Display {id}"),
            x: info.monitor.left,
            y: info.monitor.top,
            width: info.monitor.right - info.monitor.left,
            height: info.monitor.bottom - info.monitor.top,
            scale: 1.0,
            rotation: 0,
        });
    }
    1
}
pub fn displays() -> Vec<Display> {
    let mut out = vec![];
    unsafe {
        let old = SetThreadDpiAwarenessContext(-4);
        EnumDisplayMonitors(
            std::ptr::null_mut(),
            std::ptr::null(),
            monitor,
            &mut out as *mut Vec<Display> as isize,
        );
        if old != 0 {
            SetThreadDpiAwarenessContext(old);
        }
    };
    out
}
pub fn screen_permission() -> bool {
    interactive() && !locked()
}
pub fn input_permission() -> bool {
    screen_permission()
}
pub fn interactive() -> bool {
    let mut session = 0;
    unsafe { ProcessIdToSessionId(GetCurrentProcessId(), &mut session) != 0 && session != 0 }
}

pub fn session_id() -> u32 {
    let mut session = 0;
    unsafe {
        ProcessIdToSessionId(GetCurrentProcessId(), &mut session);
    };
    session
}
pub fn locked() -> bool {
    unsafe {
        let desk = OpenInputDesktop(0, 0, 1);
        if desk.is_null() {
            return true;
        }
        let mut name = [0u16; 128];
        let mut needed = 0;
        let ok = GetUserObjectInformationW(desk, 2, name.as_mut_ptr().cast(), 256, &mut needed);
        CloseDesktop(desk);
        if ok == 0 {
            return true;
        }
        let len = name.iter().position(|v| *v == 0).unwrap_or(name.len());
        String::from_utf16_lossy(&name[..len]) != "Default"
    }
}
