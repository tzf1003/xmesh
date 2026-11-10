//! Session-level input for a macOS session that does not own the physical screen. Events are posted to the
//! session event tap, which stays inside this session: they never reach the physical keyboard and pointer, or
//! another user's session. The hardware event tap is never used here.
use super::Point;
use crate::session_keys;
use enigo::{Axis, Button, Coordinate, Direction, Key};
use std::ffi::c_void;

const SESSION_TAP: u32 = 1; // kCGSessionEventTap
const COMBINED_SESSION_STATE: i32 = 0; // kCGEventSourceStateCombinedSessionState
const SOURCE_USER_DATA: u32 = 42; // kCGEventSourceUserData
const MOUSE_MOVED: u32 = 5; // kCGEventMouseMoved
const FLAG_SHIFT: u64 = 1 << 17;
const FLAG_CONTROL: u64 = 1 << 18;
const FLAG_ALTERNATE: u64 = 1 << 19;
const FLAG_COMMAND: u64 = 1 << 20;

/// Pointer and keyboard input for this session only. The pointer position, held button and held modifiers are
/// tracked here, so drags and Shift, Option or Command combinations carry the flags a real keyboard would set.
pub struct SessionInput {
    source: *mut c_void,
    position: Point,
    held: Option<Button>,
    flags: u64,
}

impl SessionInput {
    pub fn open() -> Self {
        let source = unsafe { super::CGEventSourceCreate(COMBINED_SESSION_STATE) };
        let position = current_position(source);
        Self {
            source,
            position,
            held: None,
            flags: 0,
        }
    }

    pub fn move_mouse(
        &mut self,
        x: i32,
        y: i32,
        coordinate: Coordinate,
    ) -> Result<(), &'static str> {
        if coordinate != Coordinate::Abs {
            return Err("input_failed");
        }
        self.position = Point {
            x: f64::from(x),
            y: f64::from(y),
        };
        let (kind, index) = match self.held {
            None => (MOUSE_MOVED, 0),
            Some(button) => {
                let (_, _, dragged, index) = button_events(button)?;
                (dragged, index)
            }
        };
        self.post_mouse(kind, index)
    }

    pub fn button(&mut self, button: Button, direction: Direction) -> Result<(), &'static str> {
        match direction {
            Direction::Press => self.press(button),
            Direction::Release => self.release(button),
            Direction::Click => {
                self.press(button)?;
                self.release(button)
            }
        }
    }

    pub fn scroll(&mut self, length: i32, axis: Axis) -> Result<(), &'static str> {
        let (dy, dx) = if axis == Axis::Vertical {
            (length, 0)
        } else {
            (0, length)
        };
        let event = unsafe { super::CGEventCreateScrollWheelEvent2(self.source, 1, 2, dy, dx, 0) };
        if event.is_null() {
            return Err("input_failed");
        }
        unsafe { super::CGEventSetLocation(event, self.position) };
        self.post(event, self.flags)
    }

    pub fn key(&mut self, key: Key, direction: Direction) -> Result<(), &'static str> {
        match direction {
            Direction::Press => self.key_state(key, true),
            Direction::Release => self.key_state(key, false),
            Direction::Click => {
                self.key_state(key, true)?;
                self.key_state(key, false)
            }
        }
    }

    pub fn text(&mut self, text: &str) -> Result<(), &'static str> {
        let units: Vec<u16> = text.encode_utf16().collect();
        for down in [true, false] {
            // The keycode is ignored once a Unicode string is attached.
            let event = unsafe { super::CGEventCreateKeyboardEvent(self.source, 0, down) };
            if event.is_null() {
                return Err("input_failed");
            }
            unsafe { super::CGEventKeyboardSetUnicodeString(event, units.len(), units.as_ptr()) };
            self.post(event, self.flags)?;
        }
        Ok(())
    }

    fn press(&mut self, button: Button) -> Result<(), &'static str> {
        let (down, _, _, index) = button_events(button)?;
        self.held = Some(button);
        self.post_mouse(down, index)
    }

    fn release(&mut self, button: Button) -> Result<(), &'static str> {
        let (_, up, _, index) = button_events(button)?;
        self.held = None;
        self.post_mouse(up, index)
    }

    fn key_state(&mut self, key: Key, down: bool) -> Result<(), &'static str> {
        let code = session_keys::virtual_key(key).ok_or("invalid_key")?;
        if let Some(flag) = modifier_flag(key) {
            if down {
                self.flags |= flag;
            } else {
                self.flags &= !flag;
            }
        }
        let shift = matches!(key, Key::Unicode(c) if session_keys::needs_shift(c));
        let flags = if shift {
            self.flags | FLAG_SHIFT
        } else {
            self.flags
        };
        let event = unsafe { super::CGEventCreateKeyboardEvent(self.source, code, down) };
        self.post(event, flags)
    }

    fn post_mouse(&self, kind: u32, button: u32) -> Result<(), &'static str> {
        let event =
            unsafe { super::CGEventCreateMouseEvent(self.source, kind, self.position, button) };
        self.post(event, self.flags)
    }

    fn post(&self, event: *mut c_void, flags: u64) -> Result<(), &'static str> {
        if event.is_null() {
            return Err("input_failed");
        }
        unsafe {
            super::CGEventSetFlags(event, flags);
            // The input monitor ignores events that carry the executor's marker.
            super::CGEventSetIntegerValueField(
                event,
                SOURCE_USER_DATA,
                crate::platform::INPUT_MARKER as i64,
            );
            super::CGEventPost(SESSION_TAP, event);
            super::CFRelease(event);
        }
        Ok(())
    }
}

impl Drop for SessionInput {
    fn drop(&mut self) {
        if !self.source.is_null() {
            unsafe { super::CFRelease(self.source) };
        }
    }
}

// CGEventType values for pressing, releasing and dragging a button, and its CGMouseButton index.
fn button_events(button: Button) -> Result<(u32, u32, u32, u32), &'static str> {
    match button {
        Button::Left => Ok((1, 2, 6, 0)),
        Button::Right => Ok((3, 4, 7, 1)),
        Button::Middle => Ok((25, 26, 27, 2)),
        _ => Err("invalid_button"),
    }
}

fn modifier_flag(key: Key) -> Option<u64> {
    match key {
        Key::Shift => Some(FLAG_SHIFT),
        Key::Control => Some(FLAG_CONTROL),
        Key::Alt => Some(FLAG_ALTERNATE),
        Key::Meta => Some(FLAG_COMMAND),
        _ => None,
    }
}

fn current_position(source: *mut c_void) -> Point {
    let event = unsafe { super::CGEventCreate(source) };
    if event.is_null() {
        return Point { x: 0.0, y: 0.0 };
    }
    let point = unsafe { super::CGEventGetLocation(event) };
    unsafe { super::CFRelease(event) };
    point
}
