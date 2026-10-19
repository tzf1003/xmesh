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
    fn SetWindowsHookExW(
        kind: i32,
        callback: unsafe extern "system" fn(i32, usize, isize) -> isize,
        module: *mut c_void,
        thread: u32,
    ) -> *mut c_void;
    fn CallNextHookEx(hook: *mut c_void, code: i32, wparam: usize, lparam: isize) -> isize;
    fn UnhookWindowsHookEx(hook: *mut c_void) -> i32;
    fn GetMessageW(message: *mut Message, window: *mut c_void, min: u32, max: u32) -> i32;
}
#[link(name = "kernel32")]
extern "system" {
    fn GetCurrentProcessId() -> u32;
    fn ProcessIdToSessionId(pid: u32, session: *mut u32) -> i32;
    fn GetModuleHandleW(name: *const u16) -> *mut c_void;
}

#[repr(C)]
struct Message {
    window: *mut c_void,
    message: u32,
    wparam: usize,
    lparam: isize,
    time: u32,
    x: i32,
    y: i32,
    private: u32,
}
#[repr(C)]
struct KeyboardHook {
    vk: u32,
    scan: u32,
    flags: u32,
    time: u32,
    extra: usize,
}
#[repr(C)]
struct MouseHook {
    x: i32,
    y: i32,
    data: u32,
    flags: u32,
    time: u32,
    extra: usize,
}
unsafe extern "system" fn keyboard_event(code: i32, wparam: usize, lparam: isize) -> isize {
    if code >= 0 && (wparam == 0x100 || wparam == 0x104) {
        let event = &*(lparam as *const KeyboardHook);
        if event.extra != super::INPUT_MARKER as usize {
            super::INPUT_SERIAL.fetch_add(1, std::sync::atomic::Ordering::AcqRel);
        }
    }
    CallNextHookEx(std::ptr::null_mut(), code, wparam, lparam)
}
unsafe extern "system" fn mouse_event(code: i32, wparam: usize, lparam: isize) -> isize {
    if code >= 0 && wparam != 0x200 {
        let event = &*(lparam as *const MouseHook);
        if event.extra != super::INPUT_MARKER as usize {
            super::INPUT_SERIAL.fetch_add(1, std::sync::atomic::Ordering::AcqRel);
        }
    }
    CallNextHookEx(std::ptr::null_mut(), code, wparam, lparam)
}
pub fn start_input_monitor() {
    static START: std::sync::Once = std::sync::Once::new();
    START.call_once(|| {
        std::thread::spawn(|| unsafe {
            let module = GetModuleHandleW(std::ptr::null());
            let keyboard = SetWindowsHookExW(13, keyboard_event, module, 0);
            let mouse = SetWindowsHookExW(14, mouse_event, module, 0);
            if keyboard.is_null() || mouse.is_null() {
                if !keyboard.is_null() {
                    UnhookWindowsHookEx(keyboard);
                }
                if !mouse.is_null() {
                    UnhookWindowsHookEx(mouse);
                }
                return;
            }
            super::INPUT_MONITOR_READY.store(true, std::sync::atomic::Ordering::Release);
            let mut message: Message = std::mem::zeroed();
            while GetMessageW(&mut message, std::ptr::null_mut(), 0, 0) > 0 {}
            super::INPUT_MONITOR_READY.store(false, std::sync::atomic::Ordering::Release);
            UnhookWindowsHookEx(keyboard);
            UnhookWindowsHookEx(mouse);
        });
    });
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

pub fn capture(display: &Display) -> Result<(i32, i32, Vec<u8>), &'static str> {
    use std::sync::{Arc, Mutex};
    use windows_capture::{
        capture::{Context, GraphicsCaptureApiHandler},
        frame::Frame,
        graphics_capture_api::InternalCaptureControl,
        monitor::Monitor,
        settings::{
            ColorFormat, CursorCaptureSettings, DirtyRegionSettings, DrawBorderSettings,
            MinimumUpdateIntervalSettings, SecondaryWindowSettings, Settings,
        },
    };
    type Pixels = Option<(i32, i32, Vec<u8>)>;
    struct SingleFrame {
        result: Arc<Mutex<Pixels>>,
    }
    impl GraphicsCaptureApiHandler for SingleFrame {
        type Flags = Arc<Mutex<Pixels>>;
        type Error = Box<dyn std::error::Error + Send + Sync>;
        fn new(ctx: Context<Self::Flags>) -> Result<Self, Self::Error> {
            Ok(Self { result: ctx.flags })
        }
        fn on_frame_arrived(
            &mut self,
            frame: &mut Frame,
            control: InternalCaptureControl,
        ) -> Result<(), Self::Error> {
            let width = frame.width() as i32;
            let height = frame.height() as i32;
            if width < 1 || height < 1 || width > 16384 || height > 16384 {
                control.stop();
                return Ok(());
            }
            let mut buffer = frame.buffer()?;
            let data = buffer.as_nopadding_buffer()?.to_vec();
            *self.result.lock().map_err(|_| "capture result poisoned")? =
                Some((width, height, data));
            control.stop();
            Ok(())
        }
    }
    let target = Monitor::enumerate()
        .map_err(|_| "display_unavailable")?
        .into_iter()
        .find(|m| (m.as_raw_hmonitor() as usize as u32).to_string() == display.id)
        .ok_or("display_not_found")?;
    let result = Arc::new(Mutex::new(None));
    let settings = Settings::new(
        target,
        CursorCaptureSettings::Default,
        DrawBorderSettings::Default,
        SecondaryWindowSettings::Default,
        MinimumUpdateIntervalSettings::Default,
        DirtyRegionSettings::Default,
        ColorFormat::Bgra8,
        Arc::clone(&result),
    );
    SingleFrame::start(settings).map_err(|_| "capture_failed")?;
    let mut guard = result.lock().map_err(|_| "capture_failed")?;
    guard.take().ok_or("capture_empty")
}
