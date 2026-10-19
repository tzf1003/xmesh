use enigo::{Axis, Button, Coordinate, Direction, Enigo, Key, Keyboard, Mouse, Settings};

pub enum Input {
    Native(Box<Enigo>),
    #[cfg(target_os = "linux")]
    Portal(crate::platform::PortalInput),
}

impl Input {
    pub fn select_display(&mut self, id: &str) {
        #[cfg(target_os = "linux")]
        if let Self::Portal(input) = self {
            input.select_display(id);
        }
        #[cfg(not(target_os = "linux"))]
        let _ = id;
    }
    pub fn new(authorize: bool) -> Option<Self> {
        #[cfg(target_os = "linux")]
        if std::env::var_os("WAYLAND_DISPLAY").is_some() {
            return crate::platform::PortalInput::new(authorize).map(Self::Portal);
        }
        if !crate::platform::input_permission() && !authorize {
            return None;
        }
        let settings = Settings {
            windows_dw_extra_info: Some(crate::platform::INPUT_MARKER as usize),
            event_source_user_data: Some(crate::platform::INPUT_MARKER as i64),
            open_prompt_to_get_permissions: authorize,
            release_keys_when_dropped: true,
            ..Default::default()
        };
        Enigo::new(&settings).ok().map(Box::new).map(Self::Native)
    }

    pub fn move_mouse(
        &mut self,
        x: i32,
        y: i32,
        coordinate: Coordinate,
    ) -> Result<(), &'static str> {
        #[cfg(target_os = "windows")]
        if coordinate == Coordinate::Abs {
            return crate::platform::move_mouse_absolute(x, y);
        }
        match self {
            Self::Native(input) => input
                .move_mouse(x, y, coordinate)
                .map_err(|_| "input_failed"),
            #[cfg(target_os = "linux")]
            Self::Portal(input) => input.move_mouse(x, y, coordinate),
        }
    }
    pub fn button(&mut self, button: Button, direction: Direction) -> Result<(), &'static str> {
        match self {
            Self::Native(input) => input.button(button, direction).map_err(|_| "input_failed"),
            #[cfg(target_os = "linux")]
            Self::Portal(input) => input.button(button, direction),
        }
    }
    pub fn scroll(&mut self, length: i32, axis: Axis) -> Result<(), &'static str> {
        #[cfg(target_os = "macos")]
        {
            crate::platform::scroll(length, axis)
        }
        #[cfg(not(target_os = "macos"))]
        match self {
            Self::Native(input) => input.scroll(length, axis).map_err(|_| "input_failed"),
            #[cfg(target_os = "linux")]
            Self::Portal(input) => input.scroll(length, axis),
        }
    }
    pub fn key(&mut self, key: Key, direction: Direction) -> Result<(), &'static str> {
        match self {
            Self::Native(input) => input.key(key, direction).map_err(|_| "input_failed"),
            #[cfg(target_os = "linux")]
            Self::Portal(input) => input.key(key, direction),
        }
    }
    pub fn text(&mut self, text: &str) -> Result<(), &'static str> {
        match self {
            Self::Native(input) => input.text(text).map_err(|_| "input_failed"),
            #[cfg(target_os = "linux")]
            Self::Portal(input) => input.text(text),
        }
    }
}
