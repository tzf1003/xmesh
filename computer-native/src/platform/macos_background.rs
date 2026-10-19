//! Window-scoped capture and process-directed input. This module never posts to
//! a global event tap, warps the cursor, activates an app, or uses the clipboard.
use crate::platform::Target;
use cidre::sc;
use std::{
    collections::{HashMap, HashSet},
    ffi::c_void,
    sync::{Mutex, OnceLock},
};

#[derive(Clone)]
struct WindowTarget {
    view: Target,
    pid: i32,
    started: (u64, u64),
    window: u32,
    x: f64,
    y: f64,
}

#[derive(Default)]
struct Registry {
    windows: HashMap<u32, WindowTarget>,
    input: HashMap<i32, u64>,
    generation: u64,
}
static REGISTRY: OnceLock<Mutex<Registry>> = OnceLock::new();
fn registry() -> &'static Mutex<Registry> {
    REGISTRY.get_or_init(|| Mutex::new(Registry::default()))
}

fn process_start(pid: i32) -> Option<(u64, u64)> {
    let mut info = std::mem::MaybeUninit::<libc::proc_bsdinfo>::uninit();
    let size = std::mem::size_of::<libc::proc_bsdinfo>();
    // proc_pidinfo initializes the full buffer only on an exact-size result.
    let read = unsafe {
        libc::proc_pidinfo(
            pid,
            libc::PROC_PIDTBSDINFO,
            0,
            info.as_mut_ptr().cast(),
            size as i32,
        )
    };
    if read != size as i32 {
        return None;
    }
    let info = unsafe { info.assume_init() };
    (info.pbi_uid == unsafe { libc::geteuid() })
        .then_some((info.pbi_start_tvsec, info.pbi_start_tvusec))
}

pub fn record_input(pid: i32) {
    if pid > 0 {
        if let Ok(mut registry) = registry().lock() {
            // Track only apps that currently own a discovered target window.
            if registry.windows.values().any(|target| target.pid == pid) {
                let serial = registry.input.entry(pid).or_default();
                *serial = serial.saturating_add(1);
            }
        }
    }
}

pub fn input_serial(id: &str) -> u64 {
    let Ok(registry) = registry().lock() else {
        return u64::MAX;
    };
    registry
        .windows
        .values()
        .find(|target| target.view.id == id)
        .map(|target| *registry.input.get(&target.pid).unwrap_or(&0))
        .unwrap_or(u64::MAX)
}

fn snapshot(window: &sc::Window) -> Option<WindowTarget> {
    if window.window_layer() != 0 || !window.is_on_screen() {
        return None;
    }
    let app = window.owning_app()?;
    let pid = app.process_id();
    if pid == std::process::id() as i32 {
        return None;
    }
    let started = process_start(pid)?;
    let frame = window.frame();
    let width = frame.size.width.round() as i32;
    let height = frame.size.height.round() as i32;
    if width < 1
        || height < 1
        || !crate::platform::valid_capture_size(width as usize, height as usize)
    {
        return None;
    }
    let app_id = app.bundle_id().to_string();
    if app_id.is_empty() {
        return None;
    }
    let layout = serde_json::to_string(&(
        window.id(),
        pid,
        started,
        frame.origin.x,
        frame.origin.y,
        width,
        height,
    ))
    .ok()?;
    Some(WindowTarget {
        view: Target {
            id: String::new(),
            app_id,
            name: app.app_name().to_string(),
            layout_version: layout,
            width,
            height,
            user_input_serial: 0,
        },
        pid,
        started,
        window: window.id(),
        x: frame.origin.x,
        y: frame.origin.y,
    })
}

pub fn targets() -> Vec<Target> {
    let Ok(content) = futures::executor::block_on(sc::ShareableContent::current()) else {
        return Vec::new();
    };
    let windows: Vec<_> = content
        .windows()
        .iter()
        .filter_map(snapshot)
        .take(256)
        .collect();
    let Ok(mut registry) = registry().lock() else {
        return Vec::new();
    };
    let mut active = HashSet::new();
    let mut result = Vec::new();
    for mut target in windows {
        active.insert(target.window);
        if let Some(previous) = registry.windows.get(&target.window).filter(|old| {
            old.pid == target.pid
                && old.started == target.started
                && old.view.app_id == target.view.app_id
        }) {
            target.view.id = previous.view.id.clone();
        } else {
            registry.generation = registry.generation.saturating_add(1);
            target.view.id = format!("window-{}-{}", std::process::id(), registry.generation);
        }
        target.view.user_input_serial = *registry.input.get(&target.pid).unwrap_or(&0);
        result.push(target.view.clone());
        registry.windows.insert(target.window, target);
    }
    registry.windows.retain(|id, _| active.contains(id));
    let pids: HashSet<_> = registry.windows.values().map(|target| target.pid).collect();
    registry.input.retain(|pid, _| pids.contains(pid));
    result
}

fn selected(id: &str) -> Result<WindowTarget, &'static str> {
    registry()
        .lock()
        .map_err(|_| "target_unavailable")?
        .windows
        .values()
        .find(|target| target.view.id == id)
        .cloned()
        .ok_or("target_unavailable")
}

fn same_window(expected: &WindowTarget, actual: &WindowTarget) -> bool {
    expected.pid == actual.pid
        && expected.started == actual.started
        && expected.window == actual.window
        && expected.view.app_id == actual.view.app_id
        && expected.view.layout_version == actual.view.layout_version
}

pub fn capture(target: &Target) -> Result<(i32, i32, Vec<u8>), &'static str> {
    let expected = selected(&target.id)?;
    futures::executor::block_on(async {
        let content = sc::ShareableContent::current()
            .await
            .map_err(|_| "capture_unavailable")?;
        let windows = content.windows();
        let window = windows
            .iter()
            .find(|window| snapshot(window).is_some_and(|actual| same_window(&expected, &actual)))
            .ok_or("target_unavailable")?;
        if expected.view.layout_version != target.layout_version {
            return Err("layout_changed");
        }
        let filter = sc::ContentFilter::with_desktop_independent_window(window);
        let scale = f64::from(sc::ShareableContent::info_for_filter(&filter).point_pixel_scale());
        let width = (f64::from(target.width) * scale).round() as usize;
        let height = (f64::from(target.height) * scale).round() as usize;
        super::capture_filter(&filter, width, height, false).await
    })
}

extern "C" {
    fn xmesh_background_available() -> i32;
    fn xmesh_background_dispatch(
        bytes: *const u8,
        count: usize,
        check: extern "C" fn(*mut c_void) -> bool,
        context: *mut c_void,
    ) -> i32;
    fn xmesh_background_event_target(event: *mut c_void, kind: u32) -> i32;
}

pub fn event_target(event: *mut c_void, kind: u32) -> i32 {
    unsafe { xmesh_background_event_target(event, kind) }
}

pub fn available() -> bool {
    unsafe { xmesh_background_available() == 1 }
}

struct Guard<'a> {
    runtime: &'a crate::Runtime,
    token: u64,
    start: std::time::Instant,
    target: &'a WindowTarget,
}
extern "C" fn check(context: *mut c_void) -> bool {
    // The Swift bridge calls this synchronously; execute owns the stack frame
    // and all references until xmesh_background_dispatch returns.
    let guard = unsafe { &*(context.cast::<Guard<'_>>()) };
    guard
        .runtime
        .live(guard.token, guard.start, &guard.target.view.id)
        && process_start(guard.target.pid) == Some(guard.target.started)
}

pub fn execute(
    runtime: &crate::Runtime,
    token: u64,
    start: std::time::Instant,
    q: &crate::Execution,
    action: &crate::Action,
) -> Result<(), &'static str> {
    let target = selected(&q.target_id)?;
    if let Some(frame) = &q.frame {
        if frame.target_id != target.view.id || frame.layout_version != target.view.layout_version {
            return Err("layout_changed");
        }
    }
    let point = match (action.x, action.y) {
        (Some(x), Some(y)) => Some(crate::mapped(
            q.frame.as_ref().ok_or("observation_required")?,
            x,
            y,
        )?),
        (None, None) => None,
        _ => return Err("invalid_coordinates"),
    };
    let to = match (action.to_x, action.to_y) {
        (Some(x), Some(y)) => Some(crate::mapped(
            q.frame.as_ref().ok_or("observation_required")?,
            x,
            y,
        )?),
        (None, None) => None,
        _ => return Err("invalid_coordinates"),
    };
    let bytes=serde_json::to_vec(&serde_json::json!({"type":action.kind,"pid":target.pid,"window":target.window,
        "originX":target.x,"originY":target.y,"width":target.view.width,"height":target.view.height,
        "x":point.map(|v|v.0),"y":point.map(|v|v.1),"toX":to.map(|v|v.0),"toY":to.map(|v|v.1),
        "clicks":action.clicks.max(1),"button":if action.button.is_empty(){"left"}else{&action.button},
        "deltaX":action.delta_x,"deltaY":action.delta_y,"durationMS":action.duration_ms,"keys":action.keys,"text":action.text
    })).map_err(|_|"invalid_action")?;
    let guard = Guard {
        runtime,
        token,
        start,
        target: &target,
    };
    let status = unsafe {
        xmesh_background_dispatch(
            bytes.as_ptr(),
            bytes.len(),
            check,
            (&guard as *const Guard<'_>).cast_mut().cast(),
        )
    };
    match status {
        0 => Ok(()),
        1 => Err("invalid_action"),
        3 => Err("human_intervention"),
        4 => Err("target_unavailable"),
        5 => Err("background_action_unsupported"),
        6 => Err("focus_changed"),
        _ => Err("input_failed"),
    }
}
