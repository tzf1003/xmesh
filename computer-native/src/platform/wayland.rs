//! A single user-authorized RemoteDesktop session supplies both input and
//! ScreenCast streams. Coordinates and stream IDs never come from separate grants.
use super::Display;
use ashpd::desktop::{
    remote_desktop::{Axis as PortalAxis, DeviceType, KeyState, RemoteDesktop},
    screencast::{CursorMode, Screencast, SourceType},
    PersistMode,
};
use enigo::{Axis, Button, Coordinate, Direction, Key};
use futures_util::StreamExt;
use std::{
    collections::HashSet,
    fs::{self, OpenOptions},
    io::Write,
    os::{
        fd::OwnedFd,
        unix::fs::{MetadataExt, OpenOptionsExt, PermissionsExt},
    },
    path::PathBuf,
    sync::{
        atomic::{AtomicBool, Ordering},
        mpsc, Arc, OnceLock,
    },
    thread,
    time::{Duration, Instant},
};

struct Stream {
    node: u32,
    display: Display,
}

struct Shared {
    active: AtomicBool,
    streams: Vec<Stream>,
    remote: OwnedFd,
    commands: tokio::sync::mpsc::Sender<Command>,
}

enum Event {
    Move(String, i32, i32),
    Button(i32, bool),
    Key(u32, bool),
    Scroll(i32, i32),
    Close,
}

struct Command {
    event: Event,
    result: mpsc::SyncSender<Result<(), &'static str>>,
}

static PORTAL: OnceLock<Arc<Shared>> = OnceLock::new();

pub fn available() -> bool {
    PORTAL
        .get()
        .is_some_and(|s| s.active.load(Ordering::Acquire))
}
pub fn displays() -> Vec<Display> {
    PORTAL
        .get()
        .filter(|_| available())
        .map(|s| s.streams.iter().map(|v| v.display.clone()).collect())
        .unwrap_or_default()
}

fn token_path() -> Result<PathBuf, &'static str> {
    let root = std::env::var_os("XDG_CONFIG_HOME")
        .map(PathBuf::from)
        .or_else(|| std::env::var_os("HOME").map(|v| PathBuf::from(v).join(".config")))
        .ok_or("user_config_unavailable")?;
    let directory = root.join("xmesh").join("computer");
    fs::create_dir_all(&directory).map_err(|_| "user_config_unavailable")?;
    let metadata = fs::symlink_metadata(&directory).map_err(|_| "user_config_unavailable")?;
    if !metadata.is_dir()
        || metadata.file_type().is_symlink()
        || metadata.uid() != unsafe { libc::geteuid() }
    {
        return Err("user_config_owner");
    }
    fs::set_permissions(&directory, fs::Permissions::from_mode(0o700))
        .map_err(|_| "user_config_unavailable")?;
    Ok(directory.join("portal-restore-token"))
}

fn save_token(path: &PathBuf, token: &str) -> Result<(), &'static str> {
    let tmp = path.with_extension("new");
    let mut file = OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .custom_flags(libc::O_NOFOLLOW)
        .open(&tmp)
        .map_err(|_| "restore_token_write")?;
    file.write_all(token.as_bytes())
        .map_err(|_| "restore_token_write")?;
    file.sync_all().map_err(|_| "restore_token_write")?;
    fs::rename(tmp, path).map_err(|_| "restore_token_write")
}

pub struct PortalInput {
    shared: Arc<Shared>,
    selected: String,
}

impl PortalInput {
    pub fn new(authorize: bool) -> Option<Self> {
        if let Some(s) = PORTAL.get() {
            return s.active.load(Ordering::Acquire).then(|| Self {
                shared: Arc::clone(s),
                selected: String::new(),
            });
        }
        let path = token_path().ok()?;
        let restore = OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(&path)
            .ok()
            .and_then(|mut f| {
                use std::io::Read;
                let m = f.metadata().ok()?;
                if m.uid() != unsafe { libc::geteuid() } || m.mode() & 0o077 != 0 || m.len() > 4096
                {
                    return None;
                }
                let mut token = String::new();
                f.read_to_string(&mut token).ok()?;
                Some(token)
            });
        if !authorize && restore.is_none() {
            return None;
        }
        let (tx, rx) = mpsc::sync_channel(1);
        thread::spawn(move || {
            let Ok(runtime) = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
            else {
                let _ = tx.send(Err("portal_runtime"));
                return;
            };
            runtime.block_on(async move{
                let remote=match RemoteDesktop::new().await{Ok(v)=>v,Err(_)=>{let _=tx.send(Err("portal_unavailable"));return;}};
                let screencast=match Screencast::new().await{Ok(v)=>v,Err(_)=>{let _=tx.send(Err("portal_unavailable"));return;}};
                let session=match remote.create_session().await{Ok(v)=>v,Err(_)=>{let _=tx.send(Err("portal_session"));return;}};
                let initialized=async{
                    remote.select_devices(&session,DeviceType::Keyboard|DeviceType::Pointer,restore.as_deref(),PersistMode::ExplicitlyRevoked).await.map_err(|_|"portal_select_devices")?.response().map_err(|_|"portal_permission")?;
                    screencast.select_sources(&session,CursorMode::Embedded,SourceType::Monitor.into(),true,None,PersistMode::DoNot).await.map_err(|_|"portal_select_sources")?.response().map_err(|_|"portal_permission")?;
                    let response=remote.start(&session,None).await.map_err(|_|"portal_start")?.response().map_err(|_|"portal_permission")?;
                    if !response.devices().contains(DeviceType::Keyboard)||!response.devices().contains(DeviceType::Pointer){return Err("portal_devices_missing");}
                    let mut streams=Vec::new();
                    for stream in response.streams().ok_or("portal_streams_missing")?{
                        let(width,height)=stream.size().ok_or("portal_logical_size_missing")?;
                        if width<1||height<1||width>16384||height>16384{return Err("portal_logical_size_invalid");}
                        let(x,y)=stream.position().unwrap_or((0,0));
                        streams.push(Stream{node:stream.pipe_wire_node_id(),display:Display{id:stream.pipe_wire_node_id().to_string(),name:stream.id().unwrap_or("Shared display").to_owned(),x,y,width,height,scale:1.0,rotation:0}});
                    }
                    if streams.is_empty()||streams.len()>32{return Err("portal_streams_missing");}
                    let fd=screencast.open_pipe_wire_remote(&session).await.map_err(|_|"portal_pipewire")?;
                    if let Some(token)=response.restore_token(){save_token(&path,token)?;}
                    Ok((streams,fd))
                };
                let limit=if authorize{Duration::from_secs(120)}else{Duration::from_secs(3)};
                let(streams,fd)=match tokio::time::timeout(limit,initialized).await{Ok(Ok(v))=>v,Ok(Err(e))=>{let _=session.close().await;let _=tx.send(Err(e));return;},Err(_)=>{let _=session.close().await;let _=tx.send(Err("portal_authorization_required"));return;}};
                let(commands,mut requests)=tokio::sync::mpsc::channel::<Command>(32);
                let shared=Arc::new(Shared{active:AtomicBool::new(true),streams,remote:fd,commands});
                if tx.send(Ok(Arc::clone(&shared))).is_err(){let _=session.close().await;return;}
                let mut closed=match session.receive_closed().await{Ok(v)=>v,Err(_)=>{shared.active.store(false,Ordering::Release);let _=session.close().await;return;}};
                let mut keys=HashSet::new();let mut buttons=HashSet::new();
                loop{
                    tokio::select!{
                        _ = closed.next()=>break,
                        command=requests.recv()=>{
                            let Some(command)=command else{break;};
                            if matches!(command.event,Event::Close){let _=command.result.send(Ok(()));break;}
                            let operation=async{
                                match command.event{
                                    Event::Move(id,x,y)=>{let stream=shared.streams.iter().find(|s|s.display.id==id).ok_or("display_not_found")?;let d=&stream.display;let x=x-d.x;let y=y-d.y;if x<0||y<0||x>=d.width||y>=d.height{return Err("invalid_coordinates");}remote.notify_pointer_motion_absolute(&session,stream.node,f64::from(x),f64::from(y)).await.map_err(|_|"input_failed")?;}
                                    Event::Button(button,pressed)=>{if pressed{buttons.insert(button);}remote.notify_pointer_button(&session,button,if pressed{KeyState::Pressed}else{KeyState::Released}).await.map_err(|_|"input_failed")?;if !pressed{buttons.remove(&button);}}
                                    Event::Key(key,pressed)=>{if pressed{keys.insert(key);}remote.notify_keyboard_keysym(&session,key as i32,if pressed{KeyState::Pressed}else{KeyState::Released}).await.map_err(|_|"input_failed")?;if !pressed{keys.remove(&key);}}
                                    Event::Scroll(axis,steps)=>remote.notify_pointer_axis_discrete(&session,if axis==1{PortalAxis::Horizontal}else{PortalAxis::Vertical},steps).await.map_err(|_|"input_failed")?,
                                    Event::Close=>{}
                                };Ok(())
                            };
                            let result=tokio::time::timeout(Duration::from_millis(500),operation).await.unwrap_or(Err("input_timeout"));
                            let failed=result.is_err();let _=command.result.send(result);if failed{break;}
                        }
                    }
                }
                shared.active.store(false,Ordering::Release);
                for key in keys{let _=tokio::time::timeout(Duration::from_millis(100),remote.notify_keyboard_keysym(&session,key as i32,KeyState::Released)).await;}
                for button in buttons{let _=tokio::time::timeout(Duration::from_millis(100),remote.notify_pointer_button(&session,button,KeyState::Released)).await;}
                let _=session.close().await;
            });
        });
        let shared = rx
            .recv_timeout(if authorize {
                Duration::from_secs(125)
            } else {
                Duration::from_secs(5)
            })
            .ok()?
            .ok()?;
        let _ = PORTAL.set(Arc::clone(&shared));
        Some(Self {
            shared,
            selected: String::new(),
        })
    }

    pub fn select_display(&mut self, id: &str) {
        self.selected = id.to_owned();
    }
    fn call(&self, event: Event) -> Result<(), &'static str> {
        if !self.shared.active.load(Ordering::Acquire) {
            return Err("portal_closed");
        }
        let (tx, rx) = mpsc::sync_channel(1);
        self.shared
            .commands
            .try_send(Command { event, result: tx })
            .map_err(|_| "input_queue_full")?;
        rx.recv_timeout(Duration::from_millis(750))
            .map_err(|_| "input_timeout")?
    }
    pub fn move_mouse(
        &mut self,
        x: i32,
        y: i32,
        coordinate: Coordinate,
    ) -> Result<(), &'static str> {
        if coordinate != Coordinate::Abs {
            return Err("relative_pointer_unsupported");
        }
        self.call(Event::Move(self.selected.clone(), x, y))
    }
    pub fn button(&mut self, button: Button, direction: Direction) -> Result<(), &'static str> {
        let button = match button {
            Button::Left => 272,
            Button::Right => 273,
            Button::Middle => 274,
            _ => return Err("invalid_button"),
        };
        self.directed(direction, |pressed| Event::Button(button, pressed))
    }
    pub fn scroll(&mut self, length: i32, axis: Axis) -> Result<(), &'static str> {
        self.call(Event::Scroll(
            if axis == Axis::Horizontal { 1 } else { 0 },
            length,
        ))
    }
    pub fn key(&mut self, key: Key, direction: Direction) -> Result<(), &'static str> {
        let symbol = xkeysym::Keysym::from(key).raw();
        self.directed(direction, |pressed| Event::Key(symbol, pressed))
    }
    pub fn text(&mut self, text: &str) -> Result<(), &'static str> {
        for ch in text.chars() {
            let symbol = xkeysym::Keysym::from_char(ch).raw();
            self.directed(Direction::Click, |pressed| Event::Key(symbol, pressed))?;
        }
        Ok(())
    }
    fn directed(
        &self,
        direction: Direction,
        event: impl Fn(bool) -> Event,
    ) -> Result<(), &'static str> {
        match direction {
            Direction::Press => self.call(event(true)),
            Direction::Release => self.call(event(false)),
            Direction::Click => {
                self.call(event(true))?;
                self.call(event(false))
            }
        }
    }
}

impl Drop for PortalInput {
    fn drop(&mut self) {
        let _ = self.call(Event::Close);
    }
}

pub fn capture(display: &Display) -> Result<(i32, i32, Vec<u8>), &'static str> {
    let shared = PORTAL
        .get()
        .filter(|_| available())
        .ok_or("portal_session_required")?;
    let stream = shared
        .streams
        .iter()
        .find(|s| s.display.id == display.id)
        .ok_or("display_not_found")?;
    let fd = shared.remote.try_clone().map_err(|_| "portal_pipewire")?;
    pipewire_frame(fd, stream.node, &shared.active)
}

fn pipewire_frame(
    fd: OwnedFd,
    node: u32,
    active: &AtomicBool,
) -> Result<(i32, i32, Vec<u8>), &'static str> {
    use pipewire as pw;
    use pw::{properties::properties, spa};
    use std::{cell::RefCell, rc::Rc};
    pw::init();
    let mainloop = pw::main_loop::MainLoop::new(None).map_err(|_| "pipewire_loop")?;
    let context = pw::context::Context::new(&mainloop).map_err(|_| "pipewire_context")?;
    let core = context
        .connect_fd(fd, None)
        .map_err(|_| "pipewire_connect")?;
    let stream=pw::stream::Stream::new(&core,"xmesh-computer",properties!{*pw::keys::MEDIA_TYPE=>"Video",*pw::keys::MEDIA_CATEGORY=>"Capture",*pw::keys::MEDIA_ROLE=>"Screen"}).map_err(|_|"pipewire_stream")?;
    struct Data {
        format: spa::param::video::VideoInfoRaw,
        frame: Rc<RefCell<Option<(i32, i32, Vec<u8>)>>>,
    }
    let frame = Rc::new(RefCell::new(None));
    let failed = Rc::new(RefCell::new(false));
    let fail = Rc::clone(&failed);
    let _listener = stream
        .add_local_listener_with_user_data(Data {
            format: Default::default(),
            frame: Rc::clone(&frame),
        })
        .state_changed(move |_, _, _, state| {
            if matches!(state, pw::stream::StreamState::Error(_)) {
                *fail.borrow_mut() = true;
            }
        })
        .param_changed(|_, data, id, param| {
            if id == spa::param::ParamType::Format.as_raw() {
                if let Some(param) = param {
                    let _ = data.format.parse(param);
                }
            }
        })
        .process(|stream, data| {
            let Some(mut buffer) = stream.dequeue_buffer() else {
                return;
            };
            let width = data.format.size().width as usize;
            let height = data.format.size().height as usize;
            if width == 0 || height == 0 || width > 16384 || height > 16384 {
                return;
            }
            let datas = buffer.datas_mut();
            let Some(data_buffer) = datas.first_mut() else {
                return;
            };
            let offset = data_buffer.chunk().offset() as usize;
            let stride = data_buffer.chunk().stride();
            let size = data_buffer.chunk().size() as usize;
            if stride < 0 || (stride as usize) < width * 4 {
                return;
            }
            let stride = stride as usize;
            let Some(bytes) = data_buffer.data() else {
                return;
            };
            let Some(end) = offset
                .checked_add(stride.saturating_mul(height - 1))
                .and_then(|v| v.checked_add(width * 4))
            else {
                return;
            };
            if end > bytes.len() || end > offset.saturating_add(size) {
                return;
            }
            let mut pixels = Vec::with_capacity(width * height * 4);
            for row in 0..height {
                pixels.extend_from_slice(
                    &bytes[offset + row * stride..offset + row * stride + width * 4],
                );
            }
            *data.frame.borrow_mut() = Some((width as i32, height as i32, pixels));
        })
        .register()
        .map_err(|_| "pipewire_listener")?;
    let format = spa::pod::object!(
        spa::utils::SpaTypes::ObjectParamFormat,
        spa::param::ParamType::EnumFormat,
        spa::pod::property!(
            spa::param::format::FormatProperties::MediaType,
            Id,
            spa::param::format::MediaType::Video
        ),
        spa::pod::property!(
            spa::param::format::FormatProperties::MediaSubtype,
            Id,
            spa::param::format::MediaSubtype::Raw
        ),
        spa::pod::property!(
            spa::param::format::FormatProperties::VideoFormat,
            Id,
            spa::param::video::VideoFormat::BGRA
        )
    );
    let values = spa::pod::serialize::PodSerializer::serialize(
        std::io::Cursor::new(Vec::new()),
        &spa::pod::Value::Object(format),
    )
    .map_err(|_| "pipewire_format")?
    .0
    .into_inner();
    let mut params = [spa::pod::Pod::from_bytes(&values).ok_or("pipewire_format")?];
    stream
        .connect(
            spa::utils::Direction::Input,
            Some(node),
            pw::stream::StreamFlags::AUTOCONNECT | pw::stream::StreamFlags::MAP_BUFFERS,
            &mut params,
        )
        .map_err(|_| "pipewire_connect")?;
    let start = Instant::now();
    while start.elapsed() < Duration::from_secs(2)
        && active.load(Ordering::Acquire)
        && !*failed.borrow()
    {
        mainloop.loop_().iterate(Duration::from_millis(20));
        if let Some(frame) = frame.borrow_mut().take() {
            return Ok(frame);
        }
    }
    Err("pipewire_frame_unavailable")
}
