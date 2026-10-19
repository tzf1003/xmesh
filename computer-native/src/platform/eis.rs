//! EIS sender bound to the RemoteDesktop Portal's authenticated file descriptor.
//! Use only resumed devices and stream mapping IDs from that same grant.
use super::Stream;
use futures_util::StreamExt;
use reis::{
    ei,
    event::{Connection, Device, DeviceCapability, EiEvent, Region},
    tokio::EiConvertEventStream,
};
use std::{
    os::{fd::OwnedFd, unix::net::UnixStream},
    time::Duration,
};

pub struct Eis {
    connection: Connection,
    events: EiConvertEventStream,
    active: Vec<Device>,
    sequence: u32,
}

fn region_coordinates(
    region: &Region,
    width: i32,
    height: i32,
    x: i32,
    y: i32,
) -> Result<(f32, f32), &'static str> {
    if width < 1
        || height < 1
        || x < 0
        || y < 0
        || x >= width
        || y >= height
        || region.width == 0
        || region.height == 0
    {
        return Err("invalid_coordinates");
    }
    // EIS regions use compositor logical coordinates, independently of the
    // PipeWire buffer's physical scale and the image sent to the model.
    Ok((
        region.x as f32 + x as f32 * region.width as f32 / width as f32,
        region.y as f32 + y as f32 * region.height as f32 / height as f32,
    ))
}

impl Eis {
    pub async fn new(fd: OwnedFd, streams: &[Stream]) -> Result<Self, &'static str> {
        let context = ei::Context::new(UnixStream::from(fd)).map_err(|_| "eis_connection")?;
        let initialize = async {
            let (connection, events) = context
                .handshake_tokio("xmesh-computer", ei::handshake::ContextType::Sender)
                .await
                .map_err(|_| "eis_handshake")?;
            let mut state = Self {
                connection,
                events,
                active: vec![],
                sequence: 0,
            };
            while !state.ready(streams) {
                state.next().await?;
            }
            Ok(state)
        };
        tokio::time::timeout(Duration::from_secs(2), initialize)
            .await
            .map_err(|_| "eis_capabilities_unavailable")?
    }

    fn ready(&self, streams: &[Stream]) -> bool {
        // Text keysyms support chords and Unicode without changing the system
        // keymap or borrowing clipboard access. Older EIS implementations are
        // served by a newly-created Portal Notify compatibility session.
        [
            DeviceCapability::Text,
            DeviceCapability::Button,
            DeviceCapability::Scroll,
        ]
        .into_iter()
        .all(|cap| self.active.iter().any(|d| d.has_capability(cap)))
            && streams
                .iter()
                .all(|s| self.pointer_region(s, streams.len()).is_some())
    }

    pub async fn next(&mut self) -> Result<(), &'static str> {
        let event = self
            .events
            .next()
            .await
            .ok_or("eis_closed")?
            .map_err(|_| "eis_protocol")?;
        match event {
            EiEvent::SeatAdded(event) => event.seat.bind_capabilities(
                DeviceCapability::PointerAbsolute
                    | DeviceCapability::Button
                    | DeviceCapability::Scroll
                    | DeviceCapability::Text,
            ),
            EiEvent::DeviceResumed(event) => {
                self.sequence = self.sequence.wrapping_add(1);
                event
                    .device
                    .device()
                    .start_emulating(self.connection.serial(), self.sequence);
                if !self.active.contains(&event.device) {
                    self.active.push(event.device);
                }
            }
            EiEvent::DevicePaused(event) => self.active.retain(|d| *d != event.device),
            EiEvent::DeviceRemoved(event) => self.active.retain(|d| *d != event.device),
            EiEvent::SeatRemoved(event) => self.active.retain(|d| *d.seat() != event.seat),
            EiEvent::Disconnected(_) => return Err("eis_closed"),
            _ => {}
        }
        self.connection.flush().map_err(|_| "eis_io")
    }

    fn pointer_region<'a>(
        &'a self,
        stream: &Stream,
        count: usize,
    ) -> Option<(&'a Device, &'a Region)> {
        self.active
            .iter()
            .filter(|d| d.has_capability(DeviceCapability::PointerAbsolute))
            .find_map(|device| {
                let region = if let Some(mapping) = &stream.mapping_id {
                    device
                        .regions()
                        .iter()
                        .find(|r| r.mapping_id.as_ref() == Some(mapping))
                } else if count == 1 && device.regions().len() == 1 {
                    device.regions().first()
                } else {
                    None
                }?;
                Some((device, region))
            })
    }

    fn frame(&self, device: &Device) -> Result<(), &'static str> {
        let mut time: libc::timespec = unsafe { std::mem::zeroed() };
        if unsafe { libc::clock_gettime(libc::CLOCK_MONOTONIC, &mut time) } != 0 {
            return Err("eis_clock");
        }
        let micros = time.tv_sec as u64 * 1_000_000 + time.tv_nsec as u64 / 1000;
        device.device().frame(self.connection.serial(), micros);
        self.connection.flush().map_err(|_| "eis_io")
    }

    pub fn move_pointer(
        &self,
        stream: &Stream,
        count: usize,
        x: i32,
        y: i32,
    ) -> Result<(), &'static str> {
        let (device, region) = self
            .pointer_region(stream, count)
            .ok_or("eis_region_unavailable")?;
        let (x, y) = region_coordinates(region, stream.display.width, stream.display.height, x, y)?;
        device
            .interface::<ei::PointerAbsolute>()
            .ok_or("eis_pointer_unavailable")?
            .motion_absolute(x, y);
        self.frame(device)
    }

    pub fn button(&self, button: i32, pressed: bool) -> Result<(), &'static str> {
        let device = self
            .active
            .iter()
            .find(|d| d.has_capability(DeviceCapability::Button))
            .ok_or("eis_button_unavailable")?;
        device
            .interface::<ei::Button>()
            .ok_or("eis_button_unavailable")?
            .button(
                button as u32,
                if pressed {
                    ei::button::ButtonState::Press
                } else {
                    ei::button::ButtonState::Released
                },
            );
        self.frame(device)
    }

    pub fn key(&self, symbol: u32, pressed: bool) -> Result<(), &'static str> {
        let device = self
            .active
            .iter()
            .find(|d| d.has_capability(DeviceCapability::Text))
            .ok_or("eis_text_unavailable")?;
        device
            .interface::<ei::Text>()
            .ok_or("eis_text_unavailable")?
            .keysym(
                symbol,
                if pressed {
                    ei::keyboard::KeyState::Press
                } else {
                    ei::keyboard::KeyState::Released
                },
            );
        self.frame(device)
    }

    pub fn scroll(&self, axis: i32, steps: i32) -> Result<(), &'static str> {
        let device = self
            .active
            .iter()
            .find(|d| d.has_capability(DeviceCapability::Scroll))
            .ok_or("eis_scroll_unavailable")?;
        // The EI protocol expresses discrete scroll in multiples of 120.
        let value = steps.checked_mul(120).ok_or("invalid_scroll")?;
        device
            .interface::<ei::Scroll>()
            .ok_or("eis_scroll_unavailable")?
            .scroll_discrete(
                if axis == 1 { value } else { 0 },
                if axis == 0 { value } else { 0 },
            );
        self.frame(device)
    }
}

impl Drop for Eis {
    fn drop(&mut self) {
        for device in &self.active {
            device.device().stop_emulating(self.connection.serial());
        }
        let _ = self.connection.flush();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn maps_scaled_stream_to_its_authorized_region() {
        let region = Region {
            x: 1920,
            y: 0,
            width: 1280,
            height: 720,
            scale: 2.0,
            mapping_id: Some("screen-two".into()),
        };
        assert_eq!(
            region_coordinates(&region, 2560, 1440, 1280, 720),
            Ok((2560.0, 360.0))
        );
        assert_eq!(
            region_coordinates(&region, 2560, 1440, 0, 0),
            Ok((1920.0, 0.0))
        );
        assert!(region_coordinates(&region, 2560, 1440, 2560, 0).is_err());
        assert!(region_coordinates(&region, 2560, 1440, -1, 0).is_err());
    }

    #[test]
    fn sender_frames_input_and_obeys_compositor_pause() {
        use reis::{
            eis,
            handshake::EisHandshaker,
            request::{EisRequest, EisRequestConverter},
            PendingRequestResult,
        };
        use std::{
            sync::{
                atomic::{AtomicBool, Ordering},
                mpsc, Arc,
            },
            thread,
            time::Instant,
        };
        let (server, client) = UnixStream::pair().unwrap();
        let stop = Arc::new(AtomicBool::new(false));
        let stopping = Arc::clone(&stop);
        let (observed, events) = mpsc::channel();
        let server = thread::spawn(move || {
            let context = eis::Context::new(server).unwrap();
            let mut handshake = EisHandshaker::new(&context, 1);
            let mut converter = None;
            let mut device = None;
            let mut frames = 0;
            let deadline = Instant::now() + Duration::from_secs(5);
            while !stopping.load(Ordering::Acquire) && Instant::now() < deadline {
                context.read().unwrap();
                while let Some(request) = context.pending_request() {
                    let PendingRequestResult::Request(request) = request else {
                        panic!("invalid EIS request")
                    };
                    if let Some(converter) = &mut converter {
                        let converter: &mut EisRequestConverter = converter;
                        converter.handle_request(request).unwrap();
                        while let Some(request) = converter.next_request() {
                            match request {
                                EisRequest::Bind(request) => {
                                    let caps = DeviceCapability::PointerAbsolute
                                        | DeviceCapability::Button
                                        | DeviceCapability::Scroll
                                        | DeviceCapability::Text;
                                    let d = request.seat.add_device(
                                        Some("authorized display"),
                                        eis::device::DeviceType::Virtual,
                                        caps,
                                        |d| {
                                            d.device().region_mapping_id("screen-two");
                                            d.device().region(1920, 0, 1280, 720, 2.0);
                                        },
                                    );
                                    d.resumed();
                                    device = Some(d);
                                }
                                EisRequest::PointerMotionAbsolute(e) => {
                                    observed
                                        .send(format!(
                                            "pointer:{},{}",
                                            e.dx_absolute, e.dy_absolute
                                        ))
                                        .unwrap();
                                }
                                EisRequest::TextKeysym(e) => {
                                    observed
                                        .send(format!("key:{}:{:?}", e.keysym, e.state))
                                        .unwrap();
                                }
                                EisRequest::Button(e) => {
                                    observed
                                        .send(format!("button:{}:{:?}", e.button, e.state))
                                        .unwrap();
                                }
                                EisRequest::ScrollDiscrete(e) => {
                                    observed
                                        .send(format!("scroll:{},{}", e.discrete_dx, e.discrete_dy))
                                        .unwrap();
                                }
                                EisRequest::Frame(_) => {
                                    frames += 1;
                                    observed.send("frame".into()).unwrap();
                                    if frames == 8 {
                                        device.as_ref().unwrap().paused();
                                    }
                                }
                                _ => {}
                            }
                        }
                    } else if let Some(response) = handshake.handle_request(request).unwrap() {
                        let state = EisRequestConverter::new(&context, response, 1);
                        let _ = state.handle().add_seat(
                            Some("desktop"),
                            DeviceCapability::PointerAbsolute
                                | DeviceCapability::Button
                                | DeviceCapability::Scroll
                                | DeviceCapability::Text,
                        );
                        converter = Some(state);
                    }
                }
                context.flush().unwrap();
                thread::sleep(Duration::from_millis(1));
            }
        });
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .unwrap();
        runtime.block_on(async {
            let streams = vec![Stream {
                node: 42,
                mapping_id: Some("screen-two".into()),
                display: super::super::Display {
                    id: "42".into(),
                    name: "screen".into(),
                    x: 0,
                    y: 0,
                    width: 2560,
                    height: 1440,
                    scale: 2.0,
                    rotation: 0,
                },
            }];
            let mut input = Eis::new(client.into(), &streams).await.unwrap();
            assert!(input
                .pointer_region(
                    &Stream {
                        node: 43,
                        mapping_id: Some("other-screen".into()),
                        display: streams[0].display.clone()
                    },
                    2
                )
                .is_none());
            input.move_pointer(&streams[0], 1, 1280, 720).unwrap();
            input.button(272, true).unwrap();
            input.button(272, false).unwrap();
            input.key(0x61, true).unwrap();
            input.key(0x61, false).unwrap();
            input
                .key(xkeysym::Keysym::from_char('中').raw(), true)
                .unwrap();
            input
                .key(xkeysym::Keysym::from_char('中').raw(), false)
                .unwrap();
            input.scroll(0, 3).unwrap();
            tokio::time::timeout(Duration::from_secs(1), async {
                while !input.active.is_empty() {
                    input.next().await.unwrap();
                }
            })
            .await
            .unwrap();
            assert_eq!(input.key(0x61, true), Err("eis_text_unavailable"));
            let messages: Vec<_> = events.try_iter().collect();
            assert_eq!(messages.iter().filter(|s| *s == "frame").count(), 8);
            assert!(
                messages.contains(&"pointer:2560,360".into()),
                "{messages:?}"
            );
            assert!(messages.contains(&"scroll:0,360".into()), "{messages:?}");
            assert_eq!(messages.iter().filter(|s| s.starts_with("key:")).count(), 4);
            assert_eq!(
                messages.iter().filter(|s| s.starts_with("button:")).count(),
                2
            );
            stop.store(true, Ordering::Release);
            server.join().unwrap();
        });
    }
}
