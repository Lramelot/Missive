use std::path::PathBuf;

fn main() {
    println!("cargo:rerun-if-changed=build.rs");
    if std::env::var("CARGO_CFG_TARGET_OS").as_deref() != Ok("windows") {
        return;
    }
    let manifest_dir = PathBuf::from(std::env::var("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR"));
    let icon = manifest_dir.join("..").join("packaging").join("missive.ico");
    println!("cargo:rerun-if-changed={}", icon.display());
    let out_dir = PathBuf::from(std::env::var("OUT_DIR").expect("OUT_DIR"));
    let script = out_dir.join("missive.rc");
    let icon_path = icon.display().to_string().replace('\\', "\\\\");
    std::fs::write(&script, format!("1 ICON \"{icon_path}\"\n")).expect("write missive.rc");
    embed_resource::compile(&script, embed_resource::NONE)
        .manifest_optional()
        .expect("embed the Missive icon");
}
