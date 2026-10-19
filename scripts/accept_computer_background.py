#!/usr/bin/env python3
"""Opt-in background acceptance; sends input only to a disposable fixture."""
import argparse
import base64
import ctypes
import json
import os
import pathlib
import plistlib
import queue
import subprocess
import tempfile
import threading
import time
import uuid
import secrets
from contextlib import ExitStack
from smoke_local import MCP, address, wait_for_gateway

observation_file=None


class MouseTrace:
    """Read-only input provenance; records neither key codes nor input text."""
    def __init__(self):
        self.events = []
        self.ready = threading.Event()
        self.loop = None
        self.available = False
        self.thread = threading.Thread(target=self.run, daemon=True)

    def run(self):
        cg = ctypes.CDLL("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics")
        cf = ctypes.CDLL("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
        callback_type = ctypes.CFUNCTYPE(ctypes.c_void_p, ctypes.c_void_p, ctypes.c_uint32, ctypes.c_void_p, ctypes.c_void_p)
        cg.CGEventGetIntegerValueField.argtypes = [ctypes.c_void_p, ctypes.c_uint32]
        cg.CGEventGetIntegerValueField.restype = ctypes.c_int64
        @callback_type
        def callback(proxy, kind, event, context):
            if event and kind < 0xFFFFFFFE:
                self.events.append((time.monotonic(),kind,cg.CGEventGetIntegerValueField(event, 41),cg.CGEventGetIntegerValueField(event,42)))
            return event
        cg.CGEventTapCreate.argtypes = [ctypes.c_uint32, ctypes.c_uint32, ctypes.c_uint32, ctypes.c_uint64, callback_type, ctypes.c_void_p]
        cg.CGEventTapCreate.restype = ctypes.c_void_p
        cf.CFMachPortCreateRunLoopSource.argtypes = [ctypes.c_void_p, ctypes.c_void_p, ctypes.c_long]
        cf.CFMachPortCreateRunLoopSource.restype = ctypes.c_void_p
        cf.CFRunLoopGetCurrent.restype = ctypes.c_void_p
        cf.CFRunLoopAddSource.argtypes = [ctypes.c_void_p, ctypes.c_void_p, ctypes.c_void_p]
        cf.CFRelease.argtypes = [ctypes.c_void_p]
        tap = cg.CGEventTapCreate(1, 0, 1, sum(1 << kind for kind in (1,3,5,6,7,10,12,25,27)), callback, None)
        if not tap: self.ready.set(); return
        source = cf.CFMachPortCreateRunLoopSource(None, tap, 0)
        if not source: cf.CFRelease(tap); self.ready.set(); return
        self.loop = cf.CFRunLoopGetCurrent()
        modes = ctypes.c_void_p.in_dll(cf, "kCFRunLoopCommonModes")
        cf.CFRunLoopAddSource(self.loop, source, modes)
        self.available = True
        self.ready.set()
        cf.CFRunLoopRun()
        cf.CFRelease(source); cf.CFRelease(tap)

    def start(self):
        self.thread.start(); self.ready.wait(2)
        assert self.available, "read-only movement monitor unavailable"

    def stop(self):
        if self.loop:
            cf = ctypes.CDLL("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
            cf.CFRunLoopStop.argtypes = [ctypes.c_void_p]
            cf.CFRunLoopStop(self.loop)
            self.thread.join(timeout=2)


def pointer_and_focus(fresh_after=None):
    assert observation_file is not None, "desktop observer unavailable"
    if fresh_after is not None:
        deadline=time.monotonic()+2
        while observation_file.stat().st_mtime<=fresh_after:
            assert time.monotonic()<deadline,"desktop observer did not publish a fresh baseline"
            time.sleep(.005)
    assert time.time() - observation_file.stat().st_mtime < .5, "desktop observer stale"
    value = json.loads(observation_file.read_text())
    return value["x"], value["y"], value["pid"],value.get("app_name",""),value["focus_generation"],value["focus_available"]


def stop_process(child):
    if child.poll() is None:
        child.terminate()
        try:
            child.wait(timeout=5)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait()


def main():
    global observation_file
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--helper", type=pathlib.Path, required=True)
    parser.add_argument("--binary", type=pathlib.Path, help="Use the actual local Gateway / Agent / protected Host MCP chain")
    parser.add_argument("--steps",default="click,text,key,drag,scroll")
    parser.add_argument("--fixture",choices=["appkit","electron"],default="appkit")
    parser.add_argument("--occluded",action="store_true",help="Cover the AppKit target with another window of the same fixture")
    parser.add_argument("--cancel-drag",action="store_true",help="Verify native cancellation during an AppKit drag and button release")
    parser.add_argument("--pause-on-user-input",action="store_true",help="Enable the kernel's target-scoped user-intervention policy")
    parser.add_argument("--reject-other-window-key",action="store_true",help="Verify native keyboard delivery refuses another window in the same app")
    options = parser.parse_args()
    assert os.environ.get("XMESH_TEST_ALLOW_BACKGROUND_INPUT") == "1", "obtain desktop-user approval and set XMESH_TEST_ALLOW_BACKGROUND_INPUT=1"
    assert options.helper.is_absolute() and options.helper.is_file()
    assert not options.occluded or options.fixture=="appkit","occlusion fixture requires AppKit"
    assert not options.cancel_drag or (options.fixture=="appkit" and not options.binary),"native cancellation fixture requires AppKit without --binary"
    assert not options.pause_on_user_input or options.binary,"user-intervention policy requires --binary"
    assert not options.reject_other_window_key or (options.binary and options.occluded),"other-window rejection requires MCP and the occluded AppKit fixture"
    steps=options.steps.split(',')
    assert steps and all(step in ('click','sky_click','text','append','key','native_key','drag','scroll') for step in steps)
    root = pathlib.Path(__file__).resolve().parents[1]
    with tempfile.TemporaryDirectory(prefix="xmesh-background-accept-", dir="/tmp") as temporary, ExitStack() as cleanup:
        directory = pathlib.Path(temporary)
        observer_binary=directory/'observer'
        subprocess.run(['xcrun','swiftc','-swift-version','5','-parse-as-library','-o',str(observer_binary),str(root/'computer-native/macos/Tests/DesktopObserver.swift')],check=True)
        observer=subprocess.Popen([str(observer_binary),str(directory/'observation.json')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        cleanup.callback(stop_process,observer)
        deadline=time.monotonic()+5
        while not (directory/'observation.json').exists():
            assert observer.poll() is None and time.monotonic()<deadline,'observer failed to start'
            time.sleep(.03)
        observation_file=directory/'observation.json'
        app_id = "app.xmesh.background-test." + uuid.uuid4().hex
        if options.fixture=="appkit":
            app = directory / "Xmesh Background Fixture.app"
            executable = app / "Contents/MacOS/fixture"
            executable.parent.mkdir(parents=True)
            with (app / "Contents/Info.plist").open("wb") as stream:
                plistlib.dump({"CFBundleIdentifier": app_id, "CFBundleExecutable": "fixture", "CFBundleName": "Xmesh Background Fixture", "CFBundlePackageType": "APPL", "LSUIElement": True}, stream)
            subprocess.run(["xcrun", "swiftc", "-swift-version", "5", "-parse-as-library", "-o", str(executable), str(root / "computer-native/macos/Tests/BackgroundFixture.swift")], check=True)
            subprocess.run(["codesign", "--force", "--sign", "-", str(app)], check=True, capture_output=True)
            fixture_command=[str(executable),str(directory)]
        else:
            executable=root/"ui/node_modules/electron/dist/Electron.app/Contents/MacOS/Electron"
            assert executable.is_file(),"existing Electron dependency is required"
            fixture_command=[str(executable),"--user-data-dir="+str(directory/"electron"),str(root/"ui/scripts/background-desktop-fixture.cjs")]
        baseline = pointer_and_focus()
        fixture_log=directory/"fixture.log"
        with fixture_log.open('w') as log:
            fixture = subprocess.Popen(fixture_command, stdout=log, stderr=log,env={**os.environ,"XMESH_BACKGROUND_FIXTURE_DIR":str(directory),"XMESH_BACKGROUND_FIXTURE_OCCLUDED":"1" if options.occluded else "0"})
        cleanup.callback(stop_process,fixture)
        helper = None
        mcp = None
        identity = None
        stopped = threading.Event()
        interference = []
        mouse_trace = MouseTrace()
        try:
            deadline = time.monotonic() + 10
            state_file = directory / "state.json"
            while not state_file.exists():
                assert fixture.poll() is None and time.monotonic() < deadline, {"fixture_start_failed":fixture_log.read_text()[-4000:]}
                time.sleep(.03)
            launched=pointer_and_focus()
            assert launched[2] != fixture.pid, {"phase":"fixture-launch","fixture_activated":True}
            serial = 0
            responses = queue.Queue()
            if options.binary:
                assert options.binary.is_absolute() and options.binary.is_file()
                host_state = directory / "host"
                helper = subprocess.Popen([str(options.binary), "computer", "host", "--state-dir", str(host_state), "--helper", str(options.helper)], stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
                cleanup.callback(stop_process, helper)
                endpoint = json.loads(helper.stdout.readline())["endpoint"]
                gateway, direct = address(), address()
                token = secrets.token_urlsafe(32)
                token_file = directory / "gateway.token"
                token_file.touch(mode=0o600)
                token_file.write_text(token)
                config = {"node": {"name": "background-accept", "roles": ["agent", "gateway"]},
                          "mesh": {"mode": "lan", "lan": {"mdns": True}},
                          "gateway": {"listen": gateway, "pats": {"accept": {"token_file": str(token_file), "scopes": ["devices:read", "computer:control:background-accept"]}}},
                          "agent": {"default_cwd": str(directory), "cwd_policy": {"allow": [str(directory)]},
                                    "policy": {"isolation": "L1"}, "computer": {"enabled": True, "endpoint": endpoint,"pause_on_user_input":options.pause_on_user_input},
                                    "authz": {"tag:peer": {"tools": ["*"], "backends": ["*"]}}}}
                config_file = directory / "xmesh.json"
                config_file.write_text(json.dumps(config))
                log = cleanup.enter_context((directory / "kernel.log").open("w+"))
                kernel = subprocess.Popen([str(options.binary), "serve", "--local", "--config", str(config_file), "--state-dir", str(directory / "node"), "--listen", direct], stdout=log, stderr=log)
                cleanup.callback(stop_process, kernel)
                mcp = wait_for_gateway(kernel, gateway, log, token)
                cleanup.callback(mcp.close)
            else:
                helper = subprocess.Popen([str(options.helper), "--stdio"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
                cleanup.callback(stop_process, helper)
                def read():
                    for line in helper.stdout:
                        try: responses.put(json.loads(line))
                        except ValueError: responses.put({"error": "invalid_native_response"})
                    responses.put({"error": "native_executor_exited"})
                threading.Thread(target=read, daemon=True).start()

            def tool(name, args):
                result = mcp.request("tools/call", {"name": "local/computer." + name, "arguments": args})
                assert not result.get("isError"), {"error":result.get("structuredContent"),"interference":interference,"fixture_state":json.loads(state_file.read_text())}
                return result

            def call(method, params):
                nonlocal serial
                assert not interference, {"phase": method, "interference": interference}
                if mcp:
                    if method == "info": return tool("info", {})["structuredContent"]
                    if method == "capture":
                        result = tool("observe", {**identity, "max_width": params["max_width"]})
                        frame = result["structuredContent"]
                        frame["image"] = next(item["data"] for item in result["content"] if item["type"] == "image")
                        return frame
                    if method == "execute":
                        request = {**identity, "action_id": params["action_id"], "frame_id": params["frame"]["frame_id"], "actions": params["actions"]}
                        result = tool("act", request)["structuredContent"]
                        assert tool("act", request)["structuredContent"] == result, "duplicate action changed result"
                        assert tool("action_status", {**identity, "action_id": request["action_id"]})["structuredContent"] == result
                        return result
                    if method == "release": return tool("close", identity)["structuredContent"]
                    raise AssertionError("unexpected acceptance method")
                serial += 1
                helper.stdin.write(json.dumps({"id": serial, "method": method, "params": params}) + "\n")
                helper.stdin.flush()
                try: response = responses.get(timeout=15)
                except queue.Empty: raise AssertionError({"phase": method, "error": "response_timeout", "interference": interference})
                assert not interference, {"phase": method, "interference": interference}
                assert response.get("id") == serial and "error" not in response, response.get("error")
                return response["result"]
            info = call("info", {})
            assert info.get("state")=="ready",{"desktop_not_ready":info.get("state"),"reason":info.get("reason")}
            deadline=time.monotonic()+5
            while "background:keyboard" not in info.get("capabilities",[]) or (options.pause_on_user_input and not info.get("user_input_monitor")):
                assert time.monotonic()<deadline,"native background input not ready"
                time.sleep(.05);info=call("info",{})
            def fixture_target(info):
                if options.fixture=="appkit":
                    window=json.loads(state_file.read_text())["window"]
                    return next((item for item in info.get("targets",[]) if item["app_id"]==app_id and json.loads(item["layout_version"])[0]==window),None)
                # This acceptance fixture owns this exact process. Window layout
                # metadata is executor-issued; no arbitrary app is selected.
                return next((item for item in info.get("targets",[]) if json.loads(item["layout_version"])[1]==fixture.pid),None)
            target = fixture_target(info)
            assert target, "disposable fixture window not discovered"
            if mcp:
                session = tool("open", {"mode": "control", "interaction": "background", "target_id": target["id"], "ttl_seconds": 30})["structuredContent"]
                identity = {key: session[key] for key in ("session_id", "desktop_epoch", "lease_token")}
                status = json.loads(subprocess.check_output([str(options.binary), "computer", "status", "--state-dir", str(host_state)], text=True))
                native_pid = status["native_pid"]
            else:
                native_pid = helper.pid
            def monitor():
                nonlocal baseline
                while not stopped.wait(.015):
                    try:
                        current = pointer_and_focus()
                        if current != baseline:
                            # WindowServer can briefly retain windows of other
                            # just-exited test processes. Recheck liveness before
                            # classifying an order-list change as a focus event.
                            try:os.kill(current[2],0)
                            except ProcessLookupError:continue
                            recent=[(kind,pid,marker) for stamp,kind,pid,marker in mouse_trace.events if time.monotonic()-stamp<.2]
                            motion=(5,6,7,27)
                            own=sum(kind in motion and (pid==native_pid or marker==0x584D5348) for kind,pid,marker in recent)
                            hardware_motion=sum(kind in motion and pid==0 and marker==0 for kind,pid,marker in recent)
                            hardware_focus=sum(kind in (1,3,10,12,25) and pid==0 and marker==0 for kind,pid,marker in recent)
                            pointer_changed=current[:2]!=baseline[:2]
                            foreground_changed=current[2]!=baseline[2]
                            control_focus_changed=current[4:]!=baseline[4:]
                            if current[2]!=fixture.pid and own==0 and (not pointer_changed or hardware_motion) and (not foreground_changed or hardware_focus) and (not control_focus_changed or hardware_focus):
                                baseline=current
                                continue
                            interference.append({"pointer_changed":pointer_changed,"foreground_changed":foreground_changed,"control_focus_changed":control_focus_changed,"foreground_focus_available":current[5],"foreground_before_pid":baseline[2],"foreground_after_pid":current[2],"foreground_before_app":baseline[3],"foreground_after_app":current[3],"fixture_pid":fixture.pid,"fixture_activated":current[2]==fixture.pid,"executor_activated":current[2]==native_pid,"hardware_motion_events":hardware_motion,"executor_motion_events":own,"hardware_focus_events":hardware_focus})
                            helper.kill()
                            return
                    except Exception:
                        interference.append("interference monitor unavailable"); helper.kill(); return
            # AppKit can finish initial placement after its first state write.
            # Re-observation before input is safe; input actions are never retried.
            for attempt in range(5):
                try:
                    frame = call("capture", {"target_id": target["id"], "max_width": 1280})
                    break
                except AssertionError:
                    if attempt==4:raise
                    time.sleep(.15)
                    info=call("info",{})
                    target=fixture_target(info)
                    assert target,"disposable target disappeared"
            assert frame["target_id"] == target["id"] and len(base64.b64decode(frame.pop("image"))) > 100
            baseline = pointer_and_focus()
            assert baseline[2] not in (fixture.pid,native_pid), {"phase":"capture","test_process_became_foreground":True,"fixture":baseline[2]==fixture.pid}
            print(json.dumps({"background_fixture_pid":fixture.pid,"native_executor_pid":native_pid,"baseline_foreground_pid":baseline[2],"baseline_foreground_app":baseline[3]}),flush=True)
            mouse_trace.start()
            # The listener must be live before the baseline: user movement
            # during listener setup cannot have provenance in its event list.
            baseline=pointer_and_focus(fresh_after=time.time())
            assert baseline[2] not in (fixture.pid,native_pid),"test process became foreground before input"
            assert baseline[5],"foreground AX focus identity unavailable; no input emitted"
            watch = threading.Thread(target=monitor, daemon=True); watch.start()
            state = json.loads(state_file.read_text())
            assert state.get("occluded",False)==options.occluded
            def point(name):
                p = state[name]
                return {"x": int(p["x"] * frame["image_width"] / target["width"]), "y": int(p["y"] * frame["image_height"] / target["height"])}
            results = []
            def act(name, actions):
                result = call("execute", {"desktop_epoch": info["desktop_epoch"], "lease_token": "1", "action_id": name, "interaction": "background", "target_id": target["id"], "frame": frame, "actions": actions})
                results.append({"action": name, "state": result["state"], "error": result.get("error")})
                assert result["state"] == "executed", {"result":results[-1],"interference":interference,"fixture_state":json.loads(state_file.read_text())}
                def expected():
                    observed=json.loads(state_file.read_text())
                    return {"click":observed["clicks"]==1,"sky_click":observed["clicks"]==2,"text":observed["value"]=="Xmesh 中文后台","append":observed["value"]=="Xmesh 中文后台 追加","key":observed["value"]=="验证完成","native_key":observed["value"]=="","drag":observed["drag_complete"],"scroll":observed["scroll_y"]!=state["scroll_y"]}[name]
                deadline=time.monotonic()+1
                while not expected() and not interference and time.monotonic()<deadline:time.sleep(.03)
                assert not interference,{"action":name,"interference":interference,"completed":results[:-1]}
                assert expected(),{"action":name,"fixture_state":json.loads(state_file.read_text()),"completed":results[:-1]}
                print(json.dumps({"background_step":name,"result":"PASS","agent_pointer_or_focus_interference":False}),flush=True)
            if "click" in steps:act("click", [{"type": "click", **point("button")}])
            if "sky_click" in steps:act("sky_click",[{"type":"click","clicks":2,**point("button")}])
            if "text" in steps:act("text", [{"type": "click", **point("text")}, {"type": "text", "text": "Xmesh 中文后台"}])
            if "append" in steps:act("append",[{"type":"text","text":" 追加"}])
            if "key" in steps:act("key", [{"type": "key", "keys": ["Meta", "a"]}, {"type": "text", "text": "验证完成"}])
            if "native_key" in steps:act("native_key",[{"type":"key","keys":["Meta","Backspace"]}])
            destination = point("drag_to")
            if "drag" in steps:act("drag", [{"type": "drag", **point("drag"), "to_x": destination["x"], "to_y": destination["y"], "duration_ms": 300}])
            if "scroll" in steps:act("scroll", [{"type": "scroll", **point("scroll"), "delta_y": 3}])
            time.sleep(.3)
            result = json.loads(state_file.read_text())
            observed = {"clicks": result["clicks"], "value": result["value"], "drag": result["drag_complete"], "scrolled": result["scroll_y"] != state["scroll_y"]}
            assert len(results)==len(steps),observed
            assert not interference and pointer_and_focus() == baseline, "pointer/focus changed"
            if mcp:
                assert tool("renew", {**identity, "ttl_seconds": 30})["structuredContent"]["lease_token"] == identity["lease_token"]
            if options.cancel_drag:
                before=len(result["drag_events"])
                serial+=1
                execute_id=serial
                request={"desktop_epoch":info["desktop_epoch"],"lease_token":"1","action_id":"cancel-drag","interaction":"background","target_id":target["id"],"frame":frame,
                         "actions":[{"type":"drag",**point("drag"),"to_x":destination["x"],"to_y":destination["y"],"duration_ms":1800}]}
                helper.stdin.write(json.dumps({"id":execute_id,"method":"execute","params":request})+'\n');helper.stdin.flush()
                deadline=time.monotonic()+3
                while "drag" not in json.loads(state_file.read_text())["drag_events"][before:]:
                    assert not interference and time.monotonic()<deadline,"native drag did not begin"
                    time.sleep(.015)
                serial+=1
                release_id=serial
                helper.stdin.write(json.dumps({"id":release_id,"method":"release","params":{"desktop_epoch":info["desktop_epoch"],"lease_token":"1"}})+'\n');helper.stdin.flush()
                cancelled={}
                for _ in range(2):
                    response=responses.get(timeout=5)
                    assert "error" not in response,response
                    cancelled[response["id"]]=response["result"]
                assert cancelled[execute_id]["state"]=="unknown",cancelled[execute_id]
                assert cancelled[release_id]["released"]
                deadline=time.monotonic()+1
                while json.loads(state_file.read_text())["drag_events"][-1]!="up":
                    assert time.monotonic()<deadline,"cancelled drag did not release its button"
                    time.sleep(.015)
                assert not interference and pointer_and_focus()==baseline
                print(json.dumps({"background_step":"cancel-drag","result":"PASS","state":cancelled[execute_id]["state"],"button_released":True}),flush=True)
            call("release", {"desktop_epoch": info["desktop_epoch"], "lease_token": "1"})
            if options.reject_other_window_key:
                before=json.loads(state_file.read_text())["value"]
                cover=json.loads(state_file.read_text())["cover_window"]
                other=next(item for item in tool("info",{})["structuredContent"]["targets"] if json.loads(item["layout_version"])[0]==cover)
                session=tool("open",{"mode":"control","interaction":"background","target_id":other["id"],"ttl_seconds":30})["structuredContent"]
                other_identity={key:session[key] for key in ("session_id","desktop_epoch","lease_token")}
                try:
                    other_frame=tool("observe",{**other_identity,"max_width":1280})["structuredContent"]
                    refused=tool("act",{**other_identity,"frame_id":other_frame["frame_id"],"action_id":"wrong-window-key","actions":[{"type":"key","keys":["x"]}]})["structuredContent"]
                    assert refused["state"]=="failed" and refused["executed"]==0 and refused["error"]=="background_action_unsupported",refused
                    time.sleep(.1)
                    assert json.loads(state_file.read_text())["value"]==before,"key reached another window"
                    assert not interference and pointer_and_focus()==baseline
                    print(json.dumps({"background_step":"other-window-key","result":"PASS","state":refused["state"],"executed":0}),flush=True)
                finally:tool("close",other_identity)
            print(json.dumps({"result": "PASS", "fixture": options.fixture,"occluded":options.occluded,"pause_on_user_input":options.pause_on_user_input, "transport": "mcp-gateway-agent-host" if mcp else "native-stdio", "actions": results,"user_activity_events":sum(pid==0 and marker==0 for _,_,pid,marker in mouse_trace.events), "checks": ["single-window-capture",*steps,*( ["cancel-drag-button-release"] if options.cancel_drag else []),*( ["reject-other-window-key"] if options.reject_other_window_key else []), "no-unexplained-system-pointer-movement", "no-target-foreground-activation", "foreground-AX-control-and-window-identity"]}, ensure_ascii=False))
        finally:
            stopped.set()
            mouse_trace.stop()
            observation_file=None


if __name__ == "__main__":
    main()
