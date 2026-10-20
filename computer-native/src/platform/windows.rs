use super::Display;
use std::ffi::c_void;
use windows_sys::Win32::{
    Graphics::Gdi::{
        EnumDisplaySettingsW, GetMonitorInfoW, DEVMODEW, ENUM_CURRENT_SETTINGS, MONITORINFOEXW,
    },
    System::RemoteDesktop::{
        WTSActive, WTSConnectState, WTSFreeMemory, WTSQuerySessionInformationW,
    },
    UI::{
        HiDpi::{GetDpiForMonitor, MDT_EFFECTIVE_DPI},
        Input::KeyboardAndMouse::{
            SendInput, INPUT, INPUT_0, INPUT_MOUSE, MOUSEEVENTF_ABSOLUTE, MOUSEEVENTF_MOVE,
            MOUSEEVENTF_VIRTUALDESK, MOUSEINPUT,
        },
        WindowsAndMessaging::{
            GetSystemMetrics, SM_CXVIRTUALSCREEN, SM_CYVIRTUALSCREEN, SM_XVIRTUALSCREEN,
            SM_YVIRTUALSCREEN,
        },
    },
};

#[repr(C)]
#[derive(Default)]
struct Rect {
    left: i32,
    top: i32,
    right: i32,
    bottom: i32,
}
#[link(name = "user32")]
extern "system" {
    fn EnumDisplayMonitors(
        dc: *mut c_void,
        clip: *const Rect,
        callback: unsafe extern "system" fn(*mut c_void, *mut c_void, *mut Rect, isize) -> i32,
        data: isize,
    ) -> i32;
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
    let mut info = MONITORINFOEXW::default();
    info.monitorInfo.cbSize = std::mem::size_of::<MONITORINFOEXW>() as u32;
    if GetMonitorInfoW(handle, &mut info.monitorInfo) != 0 {
        let id = handle as usize;
        let mut mode = DEVMODEW {
            dmSize: std::mem::size_of::<DEVMODEW>() as u16,
            ..Default::default()
        };
        let rotation = if EnumDisplaySettingsW(
            info.szDevice.as_ptr(),
            ENUM_CURRENT_SETTINGS,
            &mut mode,
        ) != 0
        {
            (mode.Anonymous1.Anonymous2.dmDisplayOrientation % 4) as i32 * 90
        } else {
            0
        };
        let (mut dpi_x, mut dpi_y) = (96, 96);
        let scale = if GetDpiForMonitor(handle, MDT_EFFECTIVE_DPI, &mut dpi_x, &mut dpi_y) == 0 {
            f64::from(dpi_x) / 96.0
        } else {
            1.0
        };
        let rect = info.monitorInfo.rcMonitor;
        let len = info
            .szDevice
            .iter()
            .position(|v| *v == 0)
            .unwrap_or(info.szDevice.len());
        displays.push(Display {
            id: id.to_string(),
            name: String::from_utf16_lossy(&info.szDevice[..len]),
            x: rect.left,
            y: rect.top,
            width: rect.right - rect.left,
            height: rect.bottom - rect.top,
            scale,
            rotation,
        });
    }
    1
}

fn normalized_axis(coordinate: i32, origin: i32, extent: i32) -> Result<i32, &'static str> {
    let relative = i64::from(coordinate) - i64::from(origin);
    if extent < 1 || relative < 0 || relative >= i64::from(extent) {
        return Err("invalid_coordinates");
    }
    // Aim at the center of the physical pixel in the virtual desktop. The
    // primary display can have a different DPI and need not be at its origin.
    Ok(((relative * 65536 + 32768) / i64::from(extent)).min(65535) as i32)
}

pub fn move_mouse_absolute(x: i32, y: i32) -> Result<(), &'static str> {
    unsafe {
        let dx = normalized_axis(
            x,
            GetSystemMetrics(SM_XVIRTUALSCREEN),
            GetSystemMetrics(SM_CXVIRTUALSCREEN),
        )?;
        let dy = normalized_axis(
            y,
            GetSystemMetrics(SM_YVIRTUALSCREEN),
            GetSystemMetrics(SM_CYVIRTUALSCREEN),
        )?;
        let input = INPUT {
            r#type: INPUT_MOUSE,
            Anonymous: INPUT_0 {
                mi: MOUSEINPUT {
                    dx,
                    dy,
                    mouseData: 0,
                    dwFlags: MOUSEEVENTF_MOVE | MOUSEEVENTF_ABSOLUTE | MOUSEEVENTF_VIRTUALDESK,
                    time: 0,
                    dwExtraInfo: super::INPUT_MARKER as usize,
                },
            },
        };
        if SendInput(1, &input, std::mem::size_of::<INPUT>() as i32) == 1 {
            Ok(())
        } else {
            Err("input_failed")
        }
    }
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
    // Capture permission survives a locked input desktop. Availability is
    // checked separately so callers can ask the user to unlock their session.
    interactive()
}
pub fn input_permission() -> bool {
    interactive() && !locked()
}
pub fn interactive() -> bool {
    let mut session = 0;
    unsafe {
        if ProcessIdToSessionId(GetCurrentProcessId(), &mut session) == 0 || session == 0 {
            return false;
        }
        let mut buffer = std::ptr::null_mut();
        let mut size = 0;
        if WTSQuerySessionInformationW(
            std::ptr::null_mut(),
            session,
            WTSConnectState,
            &mut buffer,
            &mut size,
        ) == 0
        {
            return false;
        }
        let active = !buffer.is_null()
            && size as usize >= std::mem::size_of::<i32>()
            && std::ptr::read_unaligned(buffer.cast::<i32>()) == WTSActive;
        WTSFreeMemory(buffer.cast());
        active
    }
}

pub fn session_id() -> u32 {
    let mut session = 0;
    unsafe {
        ProcessIdToSessionId(GetCurrentProcessId(), &mut session);
    };
    session
}
pub fn locked() -> bool {
    if !interactive() {
        return true;
    }
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
            if !super::valid_capture_size(width as usize, height as usize) {
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
        .find(|m| (m.as_raw_hmonitor() as usize).to_string() == display.id)
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

#[cfg(test)]
mod tests {
    use super::normalized_axis;

    #[test]
    fn virtual_desktop_maps_negative_origins_and_pixel_edges() {
        for (origin, extent) in [(-2560, 6400), (0, 1920), (-1080, 3000), (0, 1)] {
            for offset in [0, extent / 2, extent - 1] {
                let normalized = normalized_axis(origin + offset, origin, extent).unwrap();
                assert_eq!(
                    (i64::from(normalized) * i64::from(extent)) >> 16,
                    i64::from(offset)
                );
            }
            assert!(normalized_axis(origin - 1, origin, extent).is_err());
            assert!(normalized_axis(origin + extent, origin, extent).is_err());
        }
    }
}
