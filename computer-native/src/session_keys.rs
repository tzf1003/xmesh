//! Virtual key codes for the macOS session-level input backend: the Carbon HIToolbox `kVK_*` values, which are
//! stable across macOS releases. The table makes no platform calls, so every host can test it.
use enigo::Key;

/// The virtual key code of a key the executor accepts, or `None` when the key has no code here.
pub fn virtual_key(key: Key) -> Option<u16> {
    let code = match key {
        Key::Control => 0x3B,
        Key::Alt => 0x3A,
        Key::Shift => 0x38,
        Key::Meta => 0x37,
        Key::Return => 0x24,
        Key::Tab => 0x30,
        Key::Escape => 0x35,
        Key::Backspace => 0x33,
        Key::Delete => 0x75,
        Key::Space => 0x31,
        Key::UpArrow => 0x7E,
        Key::DownArrow => 0x7D,
        Key::LeftArrow => 0x7B,
        Key::RightArrow => 0x7C,
        Key::Home => 0x73,
        Key::End => 0x77,
        Key::PageUp => 0x74,
        Key::PageDown => 0x79,
        Key::F1 => 0x7A,
        Key::F2 => 0x78,
        Key::F3 => 0x63,
        Key::F4 => 0x76,
        Key::F5 => 0x60,
        Key::F6 => 0x61,
        Key::F7 => 0x62,
        Key::F8 => 0x64,
        Key::F9 => 0x65,
        Key::F10 => 0x6D,
        Key::F11 => 0x67,
        Key::F12 => 0x6F,
        Key::Unicode(c) => return character(c),
        _ => return None,
    };
    Some(code)
}

/// Whether a character is typed with Shift on the US layout. Only upper-case ASCII letters are supported.
pub fn needs_shift(c: char) -> bool {
    c.is_ascii_uppercase()
}

// Upper-case letters share the code of their lower-case form; `needs_shift` reports the Shift they require.
fn character(c: char) -> Option<u16> {
    let code = match c.to_ascii_lowercase() {
        'a' => 0x00,
        's' => 0x01,
        'd' => 0x02,
        'f' => 0x03,
        'h' => 0x04,
        'g' => 0x05,
        'z' => 0x06,
        'x' => 0x07,
        'c' => 0x08,
        'v' => 0x09,
        'b' => 0x0B,
        'q' => 0x0C,
        'w' => 0x0D,
        'e' => 0x0E,
        'r' => 0x0F,
        'y' => 0x10,
        't' => 0x11,
        '1' => 0x12,
        '2' => 0x13,
        '3' => 0x14,
        '4' => 0x15,
        '6' => 0x16,
        '5' => 0x17,
        '=' => 0x18,
        '9' => 0x19,
        '7' => 0x1A,
        '-' => 0x1B,
        '8' => 0x1C,
        '0' => 0x1D,
        ']' => 0x1E,
        'o' => 0x1F,
        'u' => 0x20,
        '[' => 0x21,
        'i' => 0x22,
        'p' => 0x23,
        'l' => 0x25,
        'j' => 0x26,
        '\'' => 0x27,
        'k' => 0x28,
        ';' => 0x29,
        '\\' => 0x2A,
        ',' => 0x2B,
        '/' => 0x2C,
        'n' => 0x2D,
        'm' => 0x2E,
        '.' => 0x2F,
        '`' => 0x32,
        ' ' => 0x31,
        _ => return None,
    };
    Some(code)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn letters_digits_and_named_keys_have_macos_codes() {
        assert_eq!(virtual_key(Key::Unicode('a')), Some(0x00));
        assert_eq!(virtual_key(Key::Unicode('A')), Some(0x00));
        assert!(needs_shift('A') && !needs_shift('a') && !needs_shift('1'));
        assert_eq!(virtual_key(Key::Unicode('1')), Some(0x12));
        assert_eq!(virtual_key(Key::Return), Some(0x24));
        assert_eq!(virtual_key(Key::Meta), Some(0x37));
        assert_eq!(virtual_key(Key::F12), Some(0x6F));
        assert_eq!(virtual_key(Key::Unicode('中')), None);
    }

    #[test]
    fn every_letter_has_a_distinct_code() {
        let mut seen = std::collections::HashSet::new();
        for c in 'a'..='z' {
            assert!(seen.insert(virtual_key(Key::Unicode(c)).unwrap()), "{c}");
        }
    }
}
