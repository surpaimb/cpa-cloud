use std::path::{Component, Path, PathBuf};

use serde::Serialize;
use zeroize::Zeroizing;

use crate::live::engine::{digest, TransactionError};
use crate::AppError;

#[derive(Clone, Serialize, PartialEq, Eq)]
pub struct FilePlan {
    pub path: PathBuf,
    pub expected_pre_digest: Option<String>,
    pub desired_digest: String,
    #[serde(skip)]
    pub desired_bytes: Zeroizing<Vec<u8>>,
}

impl std::fmt::Debug for FilePlan {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("FilePlan")
            .field("path", &self.path)
            .field("expected_pre_digest", &self.expected_pre_digest)
            .field("desired_digest", &self.desired_digest)
            .field("desired_bytes", &"[REDACTED]")
            .finish()
    }
}

pub(crate) fn file_plan(path: &Path, previous: Option<&[u8]>, desired_bytes: Vec<u8>) -> FilePlan {
    FilePlan {
        path: path.to_path_buf(),
        expected_pre_digest: digest(previous),
        desired_digest: digest(Some(&desired_bytes)).expect("bytes always have a digest"),
        desired_bytes: Zeroizing::new(desired_bytes),
    }
}

pub(crate) fn read_optional(path: &Path) -> Result<Option<Vec<u8>>, AppError> {
    match std::fs::read(path) {
        Ok(bytes) => Ok(Some(bytes)),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(err) => Err(AppError::io(path, err)),
    }
}

pub(crate) fn validate_transaction_relative(path: &Path, tool: &str) -> Result<(), AppError> {
    if path.as_os_str().is_empty()
        || path.components().any(|part| {
            matches!(
                part,
                Component::Prefix(_)
                    | Component::RootDir
                    | Component::ParentDir
                    | Component::CurDir
            )
        })
    {
        return Err(AppError::Config(format!(
            "{tool} 安全写入只接受配置根目录内的相对路径"
        )));
    }
    Ok(())
}

pub(crate) fn transaction_error(tool: &str, error: TransactionError) -> AppError {
    match error {
        TransactionError::Collision(path)
        | TransactionError::LinkedPath(path)
        | TransactionError::RecoveryConflict(path) => AppError::Conflict(format!(
            "{tool} 安全写入检测到文件冲突：{}；未覆盖任何已有配置",
            path.display()
        )),
        other => AppError::Config(format!("{tool} 安全写入失败：{other}")),
    }
}

pub(crate) fn normalize_cpa_base_url(input: &str) -> Result<String, AppError> {
    let mut url = crate::discovery::discovery_endpoint(input)?;
    let path = url
        .path()
        .trim_end_matches("/client-configuration")
        .to_owned();
    url.set_path(&path);
    Ok(url.to_string().trim_end_matches('/').to_string())
}
