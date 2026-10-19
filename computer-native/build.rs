fn main() {
    if std::env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("macos") {
        build_macos_background();
    }
    println!("cargo:rerun-if-changed=windows.manifest");
    if std::env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("windows")
        && std::env::var("CARGO_CFG_TARGET_ENV").as_deref() == Ok("msvc")
    {
        let manifest = std::path::Path::new(&std::env::var("CARGO_MANIFEST_DIR").unwrap())
            .join("windows.manifest");
        println!("cargo:rustc-link-arg-bin=xmesh-computer=/MANIFEST:EMBED");
        println!(
            "cargo:rustc-link-arg-bin=xmesh-computer=/MANIFESTINPUT:{}",
            manifest.display()
        );
    }
}

fn build_macos_background() {
    use std::{path::PathBuf, process::Command};
    let root = PathBuf::from(std::env::var("CARGO_MANIFEST_DIR").unwrap());
    let output = PathBuf::from(std::env::var("OUT_DIR").unwrap());
    let target = match std::env::var("CARGO_CFG_TARGET_ARCH").as_deref() {
        Ok("aarch64") => "arm64-apple-macosx14.0",
        Ok("x86_64") => "x86_64-apple-macosx14.0",
        _ => panic!("unsupported macOS background architecture"),
    };
    let mut compiler = Command::new("xcrun");
    compiler
        .args([
            "swiftc",
            "-swift-version",
            "5",
            "-O",
            "-parse-as-library",
            "-emit-library",
            "-static",
            "-module-name",
            "XmeshBackground",
            "-target",
            target,
            "-o",
        ])
        .arg(output.join("libxmesh_background.a"));
    for file in [
        "Sources/XmeshBridge.swift",
        "Sources/UpstreamKeyboard.swift",
        "Sources/AccessibilityClick.swift",
        "vendor/open-computer-use/Errors.swift",
        "vendor/open-computer-use/SkyLightSPI.swift",
        "vendor/open-computer-use/SkyClickSimulation.swift",
        "vendor/open-computer-use/SkyKeyboardSimulation.swift",
        "vendor/open-computer-use/KeyMapping.swift",
    ] {
        let file = root.join("macos").join(file);
        println!("cargo:rerun-if-changed={}", file.display());
        // Keep upstream snapshots byte-for-byte intact. Stage the synchronous
        // foreground query in Cargo's shared build cache.
        if file.file_name().is_some_and(|name| {
            name == "SkyClickSimulation.swift" || name == "SkyKeyboardSimulation.swift"
        }) {
            let source = std::fs::read_to_string(&file).unwrap();
            let original = "NSWorkspace.shared.frontmostApplication?.processIdentifier";
            assert_eq!(
                source.matches(original).count(),
                1,
                "upstream foreground lookup changed"
            );
            let source = source.replace(original, "xmeshFrontmostPID()");
            let staged = output.join(file.file_name().unwrap());
            std::fs::write(&staged, source).unwrap();
            compiler.arg(staged);
        } else {
            compiler.arg(file);
        }
    }
    let result = compiler
        .output()
        .expect("macOS background backend requires the Xcode Swift compiler");
    assert!(
        result.status.success(),
        "Swift background backend build failed:\n{}",
        String::from_utf8_lossy(&result.stderr)
    );
    let compiler = Command::new("xcrun")
        .args(["--find", "swiftc"])
        .output()
        .expect("locate Swift runtime");
    assert!(compiler.status.success(), "Swift compiler unavailable");
    let compiler = PathBuf::from(String::from_utf8_lossy(&compiler.stdout).trim());
    let runtime = compiler
        .parent()
        .unwrap()
        .parent()
        .unwrap()
        .join("lib/swift/macosx");
    println!("cargo:rustc-link-search=native={}", output.display());
    println!("cargo:rustc-link-search=native={}", runtime.display());
    println!("cargo:rustc-link-lib=static=xmesh_background");
    println!("cargo:rustc-link-lib=dylib=swiftCore");
    println!("cargo:rustc-link-arg=-Wl,-rpath,/usr/lib/swift");
    for framework in [
        "AppKit",
        "Foundation",
        "CoreGraphics",
        "ApplicationServices",
    ] {
        println!("cargo:rustc-link-lib=framework={framework}");
    }
}
