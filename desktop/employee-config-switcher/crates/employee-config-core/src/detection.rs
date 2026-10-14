use std::path::{Path, PathBuf};

use serde::Serialize;

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct ClientDetection {
    pub id: &'static str,
    pub name: &'static str,
    pub detected: bool,
    pub detail: String,
}

pub fn detect_clients(home: &Path) -> Vec<ClientDetection> {
    let config = home.join(".config");
    let candidates: [(&str, &str, PathBuf); 10] = [
        ("claude-code", "Claude Code", home.join(".claude")),
        ("codex", "Codex CLI", home.join(".codex")),
        ("gemini", "Gemini CLI", home.join(".gemini")),
        (
            "claude-desktop",
            "Claude Desktop",
            platform_claude_desktop_dir(home),
        ),
        ("grok-build", "Grok Build", home.join(".grok")),
        ("opencode", "OpenCode", config.join("opencode")),
        ("openclaw", "OpenClaw", home.join(".openclaw")),
        ("hermes", "Hermes", home.join(".hermes")),
        (
            "pi",
            "Pi",
            std::env::var_os("PI_CODING_AGENT_DIR")
                .map(PathBuf::from)
                .unwrap_or_else(|| home.join(".pi").join("agent")),
        ),
        (
            "minimax-code",
            "MiniMax Code",
            std::env::var_os("MINIMAX_DATA_DIR")
                .or_else(|| std::env::var_os("MAVIS_DATA_DIR"))
                .map(PathBuf::from)
                .unwrap_or_else(|| home.join(".minimax")),
        ),
    ];
    candidates
        .into_iter()
        .map(|(id, name, path)| ClientDetection {
            id,
            name,
            detected: path.exists(),
            detail: path.display().to_string(),
        })
        .collect()
}

fn platform_claude_desktop_dir(home: &Path) -> PathBuf {
    #[cfg(target_os = "windows")]
    {
        return std::env::var_os("APPDATA")
            .map(PathBuf::from)
            .unwrap_or_else(|| home.join("AppData").join("Roaming"))
            .join("Claude");
    }
    #[cfg(target_os = "macos")]
    {
        return home.join("Library/Application Support/Claude");
    }
    #[cfg(target_os = "linux")]
    {
        return std::env::var_os("XDG_CONFIG_HOME")
            .map(PathBuf::from)
            .unwrap_or_else(|| home.join(".config"))
            .join("Claude");
    }
    #[allow(unreachable_code)]
    home.join(".config").join("Claude")
}
