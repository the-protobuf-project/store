// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

//! Compiles the library protos into prost messages that derive
//! `prost_reflect::ReflectMessage`, from the committed descriptor set (no
//! protoc): regenerate it with
//! `buf build --path examples/proto/library/v1/library.proto --exclude-source-info -o examples/library-rust/library.binpb`.

use prost::Message;
use prost_types::FileDescriptorSet;

fn main() -> std::io::Result<()> {
    println!("cargo:rerun-if-changed=library.binpb");
    let bytes = std::fs::read("library.binpb")?;
    let fds = FileDescriptorSet::decode(bytes.as_slice())?;
    let mut config = prost_build::Config::new();
    // prost compiles every file; only library.v1's messages reflect, and only
    // they are included (src/lib.rs).
    for file in fds.file.iter().filter(|f| f.package() == "library.v1") {
        for message in &file.message_type {
            let name = format!("{}.{}", file.package(), message.name());
            config
                .type_attribute(&name, "#[derive(::prost_reflect::ReflectMessage)]")
                .type_attribute(
                    &name,
                    format!(r#"#[prost_reflect(message_name = "{name}")]"#),
                )
                .type_attribute(
                    &name,
                    r#"#[prost_reflect(descriptor_pool = "crate::DESCRIPTOR_POOL")]"#,
                );
        }
    }
    config.compile_fds(fds)
}
