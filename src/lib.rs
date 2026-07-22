#[cfg(not(unix))]
compile_error!("Factory v1 supports Unix-like operating systems only");

pub mod approval;
pub mod clone;
pub mod config;
pub mod daemon;
pub mod docker;
pub mod execution;
pub mod github;
pub mod init;
pub mod inspection;
pub mod runtime;
pub mod source;
pub mod storage;
pub mod workflow;
pub mod workspace;
