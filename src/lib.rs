#[cfg(not(unix))]
compile_error!("Factory v1 supports Unix-like operating systems only");

pub mod config;
pub mod execution;
pub mod github;
pub mod runtime;
pub mod storage;
pub mod workflow;
