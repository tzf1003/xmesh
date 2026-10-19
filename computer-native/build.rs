fn main() {
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
