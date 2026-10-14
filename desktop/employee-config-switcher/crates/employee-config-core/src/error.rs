use std::path::{Path, PathBuf};

#[derive(Debug, thiserror::Error)]
pub enum AppError {
    #[error("{0}")]
    Config(String),
    #[error("{0}")]
    Conflict(String),
    #[error("{0}")]
    Message(String),
    #[error("IO error at {path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: std::io::Error,
    },
    #[error("network request failed")]
    Network(#[from] reqwest::Error),
    #[error("CPA 地址无效；远程地址必须使用 HTTPS，HTTP 仅允许 loopback")]
    InvalidAddress,
}

impl AppError {
    pub fn localized(_code: &str, zh: impl Into<String>, _en: impl Into<String>) -> Self {
        Self::Config(zh.into())
    }

    pub fn io(path: &Path, source: std::io::Error) -> Self {
        Self::Io {
            path: path.to_path_buf(),
            source,
        }
    }
}
