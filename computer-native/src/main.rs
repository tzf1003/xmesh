mod input;
mod platform;
mod singleton;

use base64::{engine::general_purpose::STANDARD, Engine};
use enigo::{Axis, Button, Coordinate, Direction, Key};
use image::{DynamicImage, ImageFormat, RgbaImage};
use input::Input;
use serde::Deserialize;
use serde_json::{json, Value};
use std::{
    collections::HashMap,
    io::{self, BufRead, Cursor, Read, Write},
    sync::{
        atomic::{AtomicBool, AtomicU64, Ordering},
        mpsc, Arc, Mutex,
    },
    thread,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    id: u64,
    method: String,
    params: Value,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct Region {
    x: i32,
    y: i32,
    width: i32,
    height: i32,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct CaptureRequest {
    #[serde(default)]
    display_id: String,
    #[serde(default)]
    max_width: u32,
    region: Option<Region>,
    #[serde(default)]
    target_id: String,
}

#[derive(Clone, Deserialize)]
struct Frame {
    desktop_epoch: String,
    display_id: String,
    layout_version: String,
    image_width: i32,
    image_height: i32,
    capture_region: Region,
    #[serde(default)]
    target_id: String,
}

#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
struct Action {
    #[serde(rename = "type")]
    kind: String,
    x: Option<i32>,
    y: Option<i32>,
    to_x: Option<i32>,
    to_y: Option<i32>,
    #[serde(default)]
    button: String,
    #[serde(default)]
    clicks: u8,
    #[serde(default)]
    delta_x: i32,
    #[serde(default)]
    delta_y: i32,
    #[serde(default)]
    keys: Vec<String>,
    #[serde(default)]
    text: String,
    #[serde(default)]
    duration_ms: u64,
}

#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
struct Execution {
    #[serde(default)]
    user_input_serial: Option<u64>,
    desktop_epoch: String,
    lease_token: String,
    #[serde(default)]
    action_id: String,
    frame: Option<Frame>,
    #[serde(default)]
    actions: Vec<Action>,
    #[serde(default)]
    interaction: String,
    #[serde(default)]
    target_id: String,
}

struct Runtime {
    epoch: String,
    revoked: AtomicU64,
    input_ready: AtomicBool,
    pause_on_input: AtomicBool,
    input_baseline: AtomicU64,
    output: Mutex<io::Stdout>,
}

impl Runtime {
    fn reply(&self, id: u64, result: Value) {
        if let Ok(mut out) = self.output.lock() {
            let _ = serde_json::to_writer(&mut *out, &json!({"id":id,"result":result}));
            let _ = out.write_all(b"\n");
            let _ = out.flush();
        }
    }
    fn error(&self, id: u64, code: &str, reason: &str) {
        if let Ok(mut out) = self.output.lock() {
            let _ = serde_json::to_writer(
                &mut *out,
                &json!({"id":id,"error":{"error":code,"message":"桌面执行器请求未完成","reason":reason}}),
            );
            let _ = out.write_all(b"\n");
            let _ = out.flush();
        }
    }
    fn info(&self) -> Value {
        let displays = platform::displays();
        let state = if !platform::interactive() {
            "desktop_unavailable"
        } else if platform::locked() {
            "locked"
        } else if !platform::screen_permission() {
            "permission_required"
        } else {
            "ready"
        };
        let input = self.input_ready.load(Ordering::Acquire) && platform::input_permission();
        let mut capabilities = if state == "ready" && input {
            vec!["screenshot", "pointer", "keyboard", "text"]
        } else if state == "ready" {
            vec!["screenshot"]
        } else {
            vec![]
        };
        #[cfg(target_os = "linux")]
        if input && state == "ready" {
            if let Some(backend) = platform::input_backend() {
                capabilities.push(backend);
            }
        }
        #[cfg(not(target_os = "linux"))]
        let _ = &mut capabilities;
        let targets = if state == "ready" {
            platform::targets()
        } else {
            Vec::new()
        };
        #[cfg(target_os = "macos")]
        if state == "ready" {
            capabilities.push("background:screenshot");
            if input && platform::background_available() {
                capabilities.extend([
                    "background:pointer",
                    "background:keyboard",
                    "background:text",
                ]);
            }
        }
        json!({"protocol_version":1,"available":state=="ready","state":state,
            "reason":if state=="permission_required"{"screen_permission"}else if state=="ready"&&!input{"input_permission_required"}else{state},
            "desktop_id":"desktop","desktop_epoch":self.epoch,"layout_version":platform::layout(&displays),
            "capabilities":capabilities,"displays":displays,
            "user_input_monitor":platform::input_monitor_available(),"user_input_serial":platform::user_input_serial(),
            "targets":targets,"default_interaction":if cfg!(target_os="macos"){"background"}else{"foreground"}})
    }
    fn live(&self, token: u64, start: Instant, target: &str) -> bool {
        token > self.revoked.load(Ordering::Acquire)
            && start.elapsed() < Duration::from_secs(5)
            && !platform::locked()
            && platform::input_permission()
            && (!self.pause_on_input.load(Ordering::Acquire)
                || (platform::input_monitor_available()
                    && platform::target_input_serial(target)
                        == self.input_baseline.load(Ordering::Acquire)))
    }
}

fn capture(runtime: &Runtime, q: CaptureRequest) -> Result<Value, &'static str> {
    if !platform::screen_permission() || platform::locked() {
        return Err("permission_required");
    }
    let displays = platform::displays();
    let target = if q.target_id.is_empty() {
        None
    } else {
        Some(platform::target(&q.target_id)?)
    };
    if target.is_some() && !q.display_id.is_empty() {
        return Err("invalid_display");
    }
    let layout = target
        .as_ref()
        .map(|target| target.layout_version.clone())
        .unwrap_or_else(|| platform::layout(&displays));
    let display = if let Some(target) = &target {
        platform::Display {
            id: String::new(),
            name: target.name.clone(),
            x: 0,
            y: 0,
            width: target.width,
            height: target.height,
            scale: 1.0,
            rotation: 0,
        }
    } else {
        displays
            .iter()
            .find(|d| q.display_id.is_empty() || d.id == q.display_id)
            .ok_or("display_not_found")?
            .clone()
    };
    let region = q.region.unwrap_or(Region {
        x: 0,
        y: 0,
        width: display.width,
        height: display.height,
    });
    if region.x < 0
        || region.y < 0
        || region.width < 1
        || region.height < 1
        || region
            .x
            .checked_add(region.width)
            .is_none_or(|x| x > display.width)
        || region
            .y
            .checked_add(region.height)
            .is_none_or(|y| y > display.height)
        || q.max_width > 8192
    {
        return Err("invalid_region");
    }
    static RAW_CAPTURE_ACTIVE: AtomicBool = AtomicBool::new(false);
    if RAW_CAPTURE_ACTIVE.swap(true, Ordering::AcqRel) {
        return Err("capture_busy");
    }
    let (tx, rx) = mpsc::sync_channel(1);
    let capture_display = display.clone();
    let capture_target = target.clone();
    thread::spawn(move || {
        let result = std::panic::catch_unwind(|| match capture_target {
            Some(target) => platform::capture_target(&target),
            None => capture_raw(&capture_display),
        })
        .unwrap_or(Err("capture_unavailable"));
        RAW_CAPTURE_ACTIVE.store(false, Ordering::Release);
        let _ = tx.send(result);
    });
    let (width, height, mut pixels) = rx
        .recv_timeout(Duration::from_secs(3))
        .map_err(|_| "capture_timeout")??;
    if !platform::valid_capture_size(width as usize, height as usize)
        || pixels.len() != (width as usize) * (height as usize) * 4
    {
        return Err("capture_format");
    }
    for pixel in pixels.chunks_exact_mut(4) {
        pixel.swap(0, 2);
        pixel[3] = 255;
    }
    let full = RgbaImage::from_raw(width as u32, height as u32, pixels).ok_or("capture_format")?;
    let sx = width as f64 / display.width as f64;
    let sy = height as f64 / display.height as f64;
    let x = (region.x as f64 * sx).round() as u32;
    let y = (region.y as f64 * sy).round() as u32;
    let crop_width = (region.width as f64 * sx).round() as u32;
    let crop_height = (region.height as f64 * sy).round() as u32;
    if crop_width == 0
        || crop_height == 0
        || x + crop_width > width as u32
        || y + crop_height > height as u32
    {
        return Err("invalid_region");
    }
    let mut img = DynamicImage::ImageRgba8(full).crop_imm(x, y, crop_width, crop_height);
    let max_width = if q.max_width == 0 { 1600 } else { q.max_width };
    if img.width() > max_width {
        img = img.resize(max_width, 8192, image::imageops::FilterType::Triangle);
    }
    let mut encoded = Cursor::new(Vec::new());
    img.write_to(&mut encoded, ImageFormat::Png)
        .map_err(|_| "encode_failed")?;
    let mut mime = "image/png";
    if encoded.get_ref().len() > 4 << 20 {
        encoded = Cursor::new(Vec::new());
        img.to_rgb8()
            .write_to(&mut encoded, ImageFormat::Jpeg)
            .map_err(|_| "encode_failed")?;
        mime = "image/jpeg";
    }
    if encoded.get_ref().len() > 4 << 20 {
        return Err("image_too_large");
    }
    let current_layout = if let Some(target) = &target {
        platform::target(&target.id)?.layout_version
    } else {
        platform::layout(&platform::displays())
    };
    if current_layout != layout || platform::locked() {
        return Err("layout_changed");
    }
    Ok(
        json!({"desktop_epoch":runtime.epoch,"display_id":display.id,"layout_version":layout,"image_width":img.width(),"image_height":img.height(),
        "capture_region":{"x":region.x,"y":region.y,"width":region.width,"height":region.height},"target_id":q.target_id,"mime_type":mime,"image":STANDARD.encode(encoded.into_inner())}),
    )
}

fn capture_raw(display: &platform::Display) -> Result<(i32, i32, Vec<u8>), &'static str> {
    platform::capture(display)
}

fn key(s: &str) -> Result<Key, &'static str> {
    let k = match s.to_ascii_lowercase().as_str() {
        "ctrl" | "control" => Key::Control,
        "alt" | "option" => Key::Alt,
        "shift" => Key::Shift,
        "meta" | "super" | "command" | "win" => Key::Meta,
        "enter" | "return" => Key::Return,
        "tab" => Key::Tab,
        "escape" | "esc" => Key::Escape,
        "backspace" => Key::Backspace,
        "delete" => Key::Delete,
        "space" => Key::Space,
        "up" => Key::UpArrow,
        "down" => Key::DownArrow,
        "left" => Key::LeftArrow,
        "right" => Key::RightArrow,
        "home" => Key::Home,
        "end" => Key::End,
        "pageup" => Key::PageUp,
        "pagedown" => Key::PageDown,
        "f1" => Key::F1,
        "f2" => Key::F2,
        "f3" => Key::F3,
        "f4" => Key::F4,
        "f5" => Key::F5,
        "f6" => Key::F6,
        "f7" => Key::F7,
        "f8" => Key::F8,
        "f9" => Key::F9,
        "f10" => Key::F10,
        "f11" => Key::F11,
        "f12" => Key::F12,
        _ => {
            let mut chars = s.chars();
            let c = chars.next().ok_or("invalid_key")?;
            if chars.next().is_some() {
                return Err("invalid_key");
            }
            Key::Unicode(c)
        }
    };
    Ok(k)
}

fn mapped(frame: &Frame, x: i32, y: i32) -> Result<(i32, i32), &'static str> {
    if frame.image_width < 1
        || frame.image_height < 1
        || x < 0
        || y < 0
        || x >= frame.image_width
        || y >= frame.image_height
    {
        return Err("invalid_coordinates");
    }
    let displays = if frame.target_id.is_empty() {
        platform::displays()
    } else {
        let target = platform::target(&frame.target_id)?;
        if target.layout_version != frame.layout_version {
            return Err("layout_changed");
        }
        let r = &frame.capture_region;
        if r.x < 0
            || r.y < 0
            || r.width < 1
            || r.height < 1
            || r.x.checked_add(r.width).is_none_or(|v| v > target.width)
            || r.y.checked_add(r.height).is_none_or(|v| v > target.height)
        {
            return Err("invalid_region");
        }
        return Ok((
            r.x + ((x as i64 * r.width as i64) / frame.image_width as i64) as i32,
            r.y + ((y as i64 * r.height as i64) / frame.image_height as i64) as i32,
        ));
    };
    if platform::layout(&displays) != frame.layout_version {
        return Err("layout_changed");
    }
    let d = displays
        .iter()
        .find(|d| d.id == frame.display_id)
        .ok_or("display_not_found")?;
    let r = &frame.capture_region;
    if r.x < 0
        || r.y < 0
        || r.width < 1
        || r.height < 1
        || r.x.checked_add(r.width).is_none_or(|x| x > d.width)
        || r.y.checked_add(r.height).is_none_or(|y| y > d.height)
    {
        return Err("invalid_region");
    }
    Ok((
        d.x + r.x + ((x as i64 * r.width as i64) / frame.image_width as i64) as i32,
        d.y + r.y + ((y as i64 * r.height as i64) / frame.image_height as i64) as i32,
    ))
}

fn pause(
    runtime: &Runtime,
    token: u64,
    start: Instant,
    millis: u64,
    target: &str,
) -> Result<(), &'static str> {
    let end = Instant::now() + Duration::from_millis(millis);
    while Instant::now() < end {
        if !runtime.live(token, start, target) {
            return Err("cancelled");
        }
        thread::sleep(Duration::from_millis(10));
    }
    Ok(())
}

fn action(
    enigo: &mut Input,
    runtime: &Runtime,
    token: u64,
    start: Instant,
    q: &Execution,
    a: &Action,
) -> Result<(), &'static str> {
    let button = match a.button.as_str() {
        "" | "left" => Button::Left,
        "right" => Button::Right,
        "middle" => Button::Middle,
        _ => return Err("invalid_button"),
    };
    let pointer = |x, y| mapped(q.frame.as_ref().ok_or("observation_required")?, x, y);
    if !runtime.live(token, start, &q.target_id) {
        return Err("cancelled");
    }
    if let Some(frame) = q.frame.as_ref() {
        enigo.select_display(&frame.display_id);
    }
    match a.kind.as_str() {
        "move" | "click" | "scroll" | "drag" => {
            let (x, y) = pointer(
                a.x.ok_or("coordinates_required")?,
                a.y.ok_or("coordinates_required")?,
            )?;
            enigo
                .move_mouse(x, y, Coordinate::Abs)
                .map_err(|_| "input_failed")?;
            // Let the compositor route the pointer to the target before a button,
            // wheel or keyboard event uses its focus. This is cancellable.
            if a.kind != "move" {
                pause(runtime, token, start, 50, &q.target_id)?;
            }
            match a.kind.as_str() {
                "click" => {
                    for _ in 0..a.clicks.max(1) {
                        if !runtime.live(token, start, &q.target_id) {
                            return Err("cancelled");
                        }
                        enigo
                            .button(button, Direction::Click)
                            .map_err(|_| "input_failed")?;
                    }
                }
                "scroll" => {
                    if a.delta_x != 0 {
                        enigo
                            .scroll(a.delta_x, Axis::Horizontal)
                            .map_err(|_| "input_failed")?;
                    }
                    if a.delta_y != 0 {
                        enigo
                            .scroll(a.delta_y, Axis::Vertical)
                            .map_err(|_| "input_failed")?;
                    }
                }
                "drag" => {
                    let (tx, ty) = pointer(
                        a.to_x.ok_or("coordinates_required")?,
                        a.to_y.ok_or("coordinates_required")?,
                    )?;
                    enigo
                        .button(button, Direction::Press)
                        .map_err(|_| "input_failed")?;
                    let result: Result<(), &'static str> = (|| {
                        for i in 1..=20 {
                            pause(
                                runtime,
                                token,
                                start,
                                a.duration_ms.max(200) / 20,
                                &q.target_id,
                            )?;
                            enigo
                                .move_mouse(
                                    x + (tx - x) * i / 20,
                                    y + (ty - y) * i / 20,
                                    Coordinate::Abs,
                                )
                                .map_err(|_| "input_failed")?;
                        }
                        Ok(())
                    })();
                    let release = enigo
                        .button(button, Direction::Release)
                        .map_err(|_| "input_failed");
                    result?;
                    release?;
                }
                _ => {}
            }
        }
        "key" => {
            let keys = a
                .keys
                .iter()
                .map(|s| key(s))
                .collect::<Result<Vec<_>, _>>()?;
            if keys.is_empty() {
                return Err("invalid_key");
            }
            let mut held = vec![];
            let mut result = Ok(());
            for k in keys {
                if let Err(e) = enigo.key(k, Direction::Press) {
                    let _ = e;
                    result = Err("input_failed");
                    break;
                }
                held.push(k);
            }
            for k in held.into_iter().rev() {
                if enigo.key(k, Direction::Release).is_err() {
                    result = Err("input_failed");
                }
            }
            result?;
            pause(runtime, token, start, 50, &q.target_id)?;
        }
        "text" => {
            if a.text.is_empty() || a.text.len() > 65536 || a.text.contains('\0') {
                return Err("invalid_text");
            }
            let mut chunk = String::new();
            for ch in a.text.chars() {
                chunk.push(ch);
                if chunk.chars().count() >= 8 {
                    if !runtime.live(token, start, &q.target_id) {
                        return Err("cancelled");
                    }
                    enigo.text(&chunk).map_err(|_| "input_failed")?;
                    chunk.clear();
                }
            }
            if !chunk.is_empty() {
                if !runtime.live(token, start, &q.target_id) {
                    return Err("cancelled");
                }
                enigo.text(&chunk).map_err(|_| "input_failed")?;
            }
        }
        "wait" => pause(runtime, token, start, a.duration_ms, &q.target_id)?,
        _ => return Err("invalid_action"),
    };
    Ok(())
}

fn background_action(
    runtime: &Runtime,
    token: u64,
    start: Instant,
    q: &Execution,
    a: &Action,
) -> Result<(), &'static str> {
    if !runtime.live(token, start, &q.target_id) {
        return Err("cancelled");
    }
    if a.kind == "wait" {
        return pause(runtime, token, start, a.duration_ms, &q.target_id);
    }
    #[cfg(target_os = "macos")]
    {
        platform::execute_background(runtime, token, start, q, a)
    }
    #[cfg(not(target_os = "macos"))]
    {
        Err("background_unavailable")
    }
}

fn input_worker(runtime: Arc<Runtime>, rx: mpsc::Receiver<(u64, Execution)>, authorize: bool) {
    let mut input = Input::new(authorize);
    if input.is_some() {
        platform::start_input_monitor();
    }
    runtime
        .input_ready
        .store(input.is_some(), Ordering::Release);
    let mut previous_token = 0;
    let mut last_input_attempt = Instant::now();
    let mut cache: HashMap<String, (Value, Value)> = HashMap::new();
    loop {
        let (id, q) = match rx.recv_timeout(Duration::from_millis(100)) {
            Ok(job) => job,
            Err(mpsc::RecvTimeoutError::Timeout) => {
                // Native desktop permissions and display connections can become
                // ready after process startup. Retry without opening prompts;
                // Wayland grants are renewed through explicit local setup.
                let retry_native =
                    !cfg!(target_os = "linux") || std::env::var_os("WAYLAND_DISPLAY").is_none();
                if input.is_none()
                    && retry_native
                    && last_input_attempt.elapsed() >= Duration::from_secs(1)
                {
                    last_input_attempt = Instant::now();
                    input = Input::new(false);
                    if input.is_some() {
                        platform::start_input_monitor();
                        runtime.input_ready.store(true, Ordering::Release);
                    }
                }
                if previous_token <= runtime.revoked.load(Ordering::Acquire) {
                    cache.clear();
                }
                continue;
            }
            Err(mpsc::RecvTimeoutError::Disconnected) => break,
        };
        let token = q.lease_token.parse::<u64>().unwrap_or(0);
        if token == 0
            || token <= runtime.revoked.load(Ordering::Acquire)
            || q.desktop_epoch != runtime.epoch
            || token < previous_token
        {
            runtime.error(id, "lease_required", "stale_authority");
            continue;
        }
        if token != previous_token {
            cache.clear();
            previous_token = token;
        }
        let payload = json!({"interaction":q.interaction,"target":q.target_id,"user_input_serial":q.user_input_serial,"actions":q.actions.iter().map(|a|json!({"type":a.kind,"x":a.x,"y":a.y,"to_x":a.to_x,"to_y":a.to_y,"button":a.button,"clicks":a.clicks,"keys":a.keys,"text":a.text,"delta_x":a.delta_x,"delta_y":a.delta_y,"duration_ms":a.duration_ms})).collect::<Vec<_>>(),"frame":q.frame.as_ref().map(|f|json!({"epoch":f.desktop_epoch,"target":f.target_id,"display":f.display_id,"layout":f.layout_version,"w":f.image_width,"h":f.image_height,"region":[f.capture_region.x,f.capture_region.y,f.capture_region.width,f.capture_region.height]}))});
        if let Some((old, result)) = cache.get(&q.action_id) {
            if *old == payload {
                runtime.reply(id, result.clone());
            } else {
                runtime.error(id, "conflict", "action_id_reused");
            }
            continue;
        }
        if cache.len() >= 128
            || q.action_id.is_empty()
            || q.action_id.len() > 128
            || q.actions.is_empty()
            || q.actions.len() > 8
            || q.actions
                .iter()
                .any(|a| a.duration_ms > 2000 || a.clicks > 3 || a.keys.len() > 8)
        {
            runtime.error(id, "invalid_args", "invalid_execution");
            continue;
        }
        if q.frame
            .as_ref()
            .is_some_and(|f| f.desktop_epoch != runtime.epoch)
        {
            runtime.error(id, "conflict", "desktop_epoch");
            continue;
        }
        if !matches!(q.interaction.as_str(), "" | "foreground" | "background")
            || (q.interaction == "background") != !q.target_id.is_empty()
            || q.frame
                .as_ref()
                .is_some_and(|frame| frame.target_id != q.target_id)
        {
            runtime.error(id, "invalid_args", "invalid_target");
            continue;
        }
        let mut executed = 0;
        runtime
            .pause_on_input
            .store(q.user_input_serial.is_some(), Ordering::Release);
        runtime
            .input_baseline
            .store(q.user_input_serial.unwrap_or(0), Ordering::Release);
        let mut error = None;
        let start = Instant::now();
        if q.interaction == "background" {
            for a in &q.actions {
                match background_action(&runtime, token, start, &q, a) {
                    Ok(()) => executed += 1,
                    Err(reason) => {
                        error = Some(reason);
                        break;
                    }
                }
            }
        } else if let Some(enigo) = input.as_mut() {
            for a in &q.actions {
                match action(enigo, &runtime, token, start, &q, a) {
                    Ok(()) => executed += 1,
                    Err(reason) => {
                        error = Some(reason);
                        break;
                    }
                }
            }
        } else {
            error.get_or_insert("permission_required");
        }
        if q.user_input_serial
            .is_some_and(|serial| serial != platform::target_input_serial(&q.target_id))
        {
            error = Some("human_intervention");
        }
        let result = json!({"action_id":q.action_id,"state":match error{Some("input_failed"|"cancelled"|"human_intervention"|"focus_changed")=>"unknown",Some(_)=>"failed",None=>"executed"},"executed":executed,"error":error});
        cache.insert(q.action_id.clone(), (payload, result.clone()));
        runtime.reply(id, result);
    }
    drop(input);
}

fn main() {
    if std::env::args().any(|a| a == "--version") {
        println!(
            "xmesh-computer {} protocol=1",
            option_env!("XMESH_BUILD_VERSION").unwrap_or(env!("CARGO_PKG_VERSION"))
        );
        return;
    }
    if !std::env::args().any(|a| a == "--stdio") {
        eprintln!("xmesh-computer requires a local broker (--stdio)");
        std::process::exit(2);
    }
    let _singleton = match singleton::acquire() {
        Ok(lock) => lock,
        Err(_) => {
            eprintln!("desktop executor is already bound or its local lock is unavailable");
            std::process::exit(3);
        }
    };
    let authorize = std::env::args().any(|a| a == "--authorize");
    #[cfg(target_os = "macos")]
    if authorize && !platform::screen_permission() {
        platform::request_screen_permission();
    }
    let runtime = Arc::new(Runtime {
        epoch: format!(
            "{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap_or_default()
                .as_nanos()
        ),
        revoked: AtomicU64::new(0),
        input_ready: AtomicBool::new(false),
        pause_on_input: AtomicBool::new(false),
        input_baseline: AtomicU64::new(0),
        output: Mutex::new(io::stdout()),
    });
    let (tx, rx) = mpsc::sync_channel(8);
    let worker = Arc::clone(&runtime);
    let input_thread = thread::spawn(move || input_worker(worker, rx, authorize));
    let capture_busy = Arc::new(AtomicBool::new(false));
    let stdin = io::stdin();
    let mut input = stdin.lock();
    let mut raw = Vec::new();
    loop {
        raw.clear();
        let mut bounded = (&mut input).take((1 << 20) + 1);
        let Ok(n) = bounded.read_until(b'\n', &mut raw) else {
            break;
        };
        if n == 0 {
            break;
        }
        if n > 1 << 20 {
            break;
        }
        let Ok(q) = serde_json::from_slice::<Request>(&raw) else {
            break;
        };
        match q.method.as_str() {
            "info" => runtime.reply(q.id, runtime.info()),
            "release" => {
                if let Ok(e) = serde_json::from_value::<Execution>(q.params) {
                    if e.desktop_epoch == runtime.epoch {
                        runtime
                            .revoked
                            .fetch_max(e.lease_token.parse().unwrap_or(0), Ordering::AcqRel);
                    }
                    runtime.reply(q.id, json!({"released":true}));
                } else {
                    runtime.error(q.id, "invalid_args", "release");
                }
            }
            "execute" => match serde_json::from_value::<Execution>(q.params) {
                Ok(e) => {
                    if tx.try_send((q.id, e)).is_err() {
                        runtime.error(q.id, "resource_busy", "input_queue_full");
                    }
                }
                Err(_) => runtime.error(q.id, "invalid_args", "execution"),
            },
            "capture" => {
                if capture_busy.swap(true, Ordering::AcqRel) {
                    runtime.error(q.id, "resource_busy", "capture_busy");
                    continue;
                }
                let rt = Arc::clone(&runtime);
                let busy = Arc::clone(&capture_busy);
                thread::spawn(move || {
                    match serde_json::from_value(q.params) {
                        Ok(r) => match capture(&rt, r) {
                            Ok(v) => rt.reply(q.id, v),
                            Err(reason) => rt.error(q.id, "desktop_unavailable", reason),
                        },
                        Err(_) => rt.error(q.id, "invalid_args", "capture"),
                    };
                    busy.store(false, Ordering::Release);
                });
            }
            _ => runtime.error(q.id, "invalid_args", "method"),
        }
    }
    runtime.revoked.store(u64::MAX, Ordering::Release);
    drop(tx);
    let _ = input_thread.join();
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn release_request_accepts_the_go_metadata_only_envelope() {
        let request: Execution = serde_json::from_str(
            r#"{"desktop_epoch":"epoch","lease_token":"1","action_id":"","frame":null}"#,
        )
        .unwrap();
        assert!(request.actions.is_empty());
        assert!(request.interaction.is_empty());
        assert!(request.target_id.is_empty());
    }
    #[test]
    fn keys_are_explicit() {
        assert!(key("Control").is_ok());
        assert!(key("a").is_ok());
        assert!(key("shell-command").is_err());
    }
    #[test]
    fn protocol_rejects_unknown_operations_fields() {
        assert!(serde_json::from_value::<Execution>(
            json!({"desktop_epoch":"e","lease_token":"1","cmd":"calc"})
        )
        .is_err());
    }
    #[test]
    fn layout_includes_origin_and_scaling() {
        let mut ds = vec![platform::Display {
            id: "1".into(),
            name: "Display".into(),
            x: 0,
            y: 0,
            width: 100,
            height: 80,
            scale: 1.0,
            rotation: 0,
        }];
        let a = platform::layout(&ds);
        ds[0].x = -100;
        assert_ne!(a, platform::layout(&ds));
        ds[0].x = 0;
        ds[0].scale = 2.0;
        assert_ne!(a, platform::layout(&ds));
    }
    #[test]
    fn native_capture_pixel_budget_accepts_8k_and_rejects_excessive_allocations() {
        assert!(platform::valid_capture_size(7680, 4320));
        assert!(!platform::valid_capture_size(16384, 16384));
        assert!(!platform::valid_capture_size(0, 100));
        assert!(!platform::valid_capture_size(usize::MAX, 2));
    }
}
