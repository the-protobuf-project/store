// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

//! The library example: prost messages at `crate::pb`, as protoc-gen-service's
//! Rust target names them, and the generated repositories mounted beside them.

use std::sync::LazyLock;

use prost_reflect::DescriptorPool;

/// The prost messages of every proto package, by package path.
pub mod pb {
    /// `library`.
    pub mod library {
        /// `library.v1`.
        pub mod v1 {
            include!(concat!(env!("OUT_DIR"), "/library.v1.rs"));
        }
    }
}

/// The generated repositories (lang=rust target=repository), mounted from the
/// generated crate's root.
#[path = "../../generated/rust/repository/library/lib.rs"]
pub mod repository;

/// Every descriptor the messages reflect through, imports included.
pub static DESCRIPTOR_POOL: LazyLock<DescriptorPool> = LazyLock::new(|| {
    DescriptorPool::decode(include_bytes!("../library.binpb").as_ref())
        .expect("library.binpb is a valid descriptor set")
});
