#[cfg(not(unix))]
compile_error!("Factory v1 supports Unix-like operating systems only");

pub mod approval;
pub mod clone;
pub mod config;
pub mod daemon;
pub mod execution;
pub mod github;
mod hash;
pub mod init;
pub mod inspection;
pub mod runtime;
pub mod sandbox;
pub mod source;
pub mod storage;
pub mod workflow;
pub mod workspace;
