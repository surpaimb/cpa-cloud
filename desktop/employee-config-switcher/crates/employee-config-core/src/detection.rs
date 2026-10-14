use std::ffi::OsString;
use std::path::{Path, PathBuf};

use serde::Serialize;

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct ClientDetection {
    pub id: &'static str,
    pub name: &'static str,
    pub detected: bool,
    pub detail: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Platform {
    Windows,
    Macos,
    Linux,
}

pub fn detect_clients(home: &Path) -> Vec<ClientDetection> {
    detect_clients_with_env(home, current_platform(), |name| std::env::var_os(name))
}

fn detect_clients_with_env<F>(home: &Path, platform: Platform, env: F) -> Vec<ClientDetection>
where
    F: Fn(&str) -> Option<OsString>,
{
    let config = env_path(&env, "XDG_CONFIG_HOME", home.join(".config"));
    let candidates: [(&str, &str, Vec<PathBuf>); 10] = [
        ("claude-code", "Claude Code", vec![home.join(".claude")]),
        (
            "codex",
            "Codex CLI",
            vec![env_path(&env, "CODEX_HOME", home.join(".codex"))],
        ),
        (
            "gemini",
            "Gemini CLI",
            vec![env_path(&env, "GEMINI_CLI_HOME", home.join(".gemini"))],
        ),
        (
            "claude-desktop",
            "Claude Desktop",
            claude_desktop_candidates(home, platform, &env),
        ),
        ("grok-build", "Grok Build", vec![home.join(".grok")]),
        ("opencode", "OpenCode", vec![config.join("opencode")]),
        ("openclaw", "OpenClaw", vec![home.join(".openclaw")]),
        ("hermes", "Hermes", vec![hermes_home(home, platform, &env)]),
        (
            "pi",
            "Pi",
            vec![env_path(
                &env,
                "PI_CODING_AGENT_DIR",
                home.join(".pi").join("agent"),
            )],
        ),
        (
            "minimax-code",
            "MiniMax Code",
            vec![env("MINIMAX_DATA_DIR")
                .filter(|value| !value.is_empty())
                .or_else(|| env("MAVIS_DATA_DIR").filter(|value| !value.is_empty()))
                .map(PathBuf::from)
                .unwrap_or_else(|| home.join(".minimax"))],
        ),
    ];

    candidates
        .into_iter()
        .map(|(id, name, paths)| detection(id, name, paths))
        .collect()
}

fn detection(id: &'static str, name: &'static str, paths: Vec<PathBuf>) -> ClientDetection {
    let existing = paths.iter().find(|path| path.exists());
    let detail = existing.map_or_else(
        || {
            paths
                .iter()
                .map(|path| path.display().to_string())
                .collect::<Vec<_>>()
                .join(" | ")
        },
        |path| path.display().to_string(),
    );
    ClientDetection {
        id,
        name,
        detected: existing.is_some(),
        detail,
    }
}

fn env_path<F>(env: &F, name: &str, fallback: PathBuf) -> PathBuf
where
    F: Fn(&str) -> Option<OsString>,
{
    env(name)
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .unwrap_or(fallback)
}

fn claude_desktop_candidates<F>(home: &Path, platform: Platform, env: &F) -> Vec<PathBuf>
where
    F: Fn(&str) -> Option<OsString>,
{
    match platform {
        Platform::Windows => {
            let local = env_path(env, "LOCALAPPDATA", home.join("AppData").join("Local"));
            vec![local.join("Claude"), local.join("Claude-3p")]
        }
        Platform::Macos => {
            let application_support = home.join("Library/Application Support");
            vec![
                application_support.join("Claude"),
                application_support.join("Claude-3p"),
            ]
        }
        Platform::Linux => {
            let config = env_path(env, "XDG_CONFIG_HOME", home.join(".config"));
            vec![config.join("Claude"), config.join("Claude-3p")]
        }
    }
}

fn hermes_home<F>(home: &Path, platform: Platform, env: &F) -> PathBuf
where
    F: Fn(&str) -> Option<OsString>,
{
    if let Some(path) = env("HERMES_HOME").filter(|value| !value.is_empty()) {
        return PathBuf::from(path);
    }
    if platform == Platform::Windows {
        return env_path(env, "LOCALAPPDATA", home.join("AppData").join("Local")).join("hermes");
    }
    home.join(".hermes")
}

#[cfg(target_os = "windows")]
fn current_platform() -> Platform {
    Platform::Windows
}

#[cfg(target_os = "macos")]
fn current_platform() -> Platform {
    Platform::Macos
}

#[cfg(target_os = "linux")]
fn current_platform() -> Platform {
    Platform::Linux
}

#[cfg(not(any(target_os = "windows", target_os = "macos", target_os = "linux")))]
fn current_platform() -> Platform {
    Platform::Linux
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;
    use std::fs;

    fn fake_env(values: HashMap<&'static str, PathBuf>) -> impl Fn(&str) -> Option<OsString> {
        move |name| values.get(name).map(|path| path.as_os_str().to_owned())
    }

    fn by_id<'a>(items: &'a [ClientDetection], id: &str) -> &'a ClientDetection {
        items.iter().find(|item| item.id == id).unwrap()
    }

    #[test]
    fn honors_documented_tool_home_overrides_without_mutating_process_env() {
        let root = tempfile::tempdir().unwrap();
        let codex = root.path().join("custom-codex");
        let gemini = root.path().join("custom-gemini");
        let hermes = root.path().join("custom-hermes");
        let pi = root.path().join("custom-pi");
        for path in [&codex, &gemini, &hermes, &pi] {
            fs::create_dir(path).unwrap();
        }
        let env = fake_env(HashMap::from([
            ("CODEX_HOME", codex.clone()),
            ("GEMINI_CLI_HOME", gemini.clone()),
            ("HERMES_HOME", hermes.clone()),
            ("PI_CODING_AGENT_DIR", pi.clone()),
        ]));
        let items = detect_clients_with_env(root.path(), Platform::Linux, env);
        assert_eq!(by_id(&items, "codex").detail, codex.display().to_string());
        assert!(by_id(&items, "codex").detected);
        assert_eq!(by_id(&items, "gemini").detail, gemini.display().to_string());
        assert!(by_id(&items, "gemini").detected);
        assert_eq!(by_id(&items, "hermes").detail, hermes.display().to_string());
        assert!(by_id(&items, "hermes").detected);
        assert_eq!(by_id(&items, "pi").detail, pi.display().to_string());
        assert!(by_id(&items, "pi").detected);
    }

    #[test]
    fn windows_desktop_uses_local_app_data_and_finds_three_party_layout() {
        let root = tempfile::tempdir().unwrap();
        let local = root.path().join("LocalAppData");
        let three_party = local.join("Claude-3p");
        fs::create_dir_all(&three_party).unwrap();
        let env = fake_env(HashMap::from([("LOCALAPPDATA", local)]));
        let items = detect_clients_with_env(root.path(), Platform::Windows, env);
        let desktop = by_id(&items, "claude-desktop");
        assert!(desktop.detected);
        assert_eq!(desktop.detail, three_party.display().to_string());
    }

    #[test]
    fn macos_and_linux_desktop_find_three_party_layout() {
        let mac_root = tempfile::tempdir().unwrap();
        let mac_three_party = mac_root
            .path()
            .join("Library")
            .join("Application Support")
            .join("Claude-3p");
        fs::create_dir_all(&mac_three_party).unwrap();
        let mac_items = detect_clients_with_env(mac_root.path(), Platform::Macos, |_| None);
        let mac_desktop = by_id(&mac_items, "claude-desktop");
        assert!(mac_desktop.detected);
        assert_eq!(mac_desktop.detail, mac_three_party.display().to_string());

        let linux_root = tempfile::tempdir().unwrap();
        let xdg = linux_root.path().join("xdg");
        let linux_three_party = xdg.join("Claude-3p");
        fs::create_dir_all(&linux_three_party).unwrap();
        let linux_items = detect_clients_with_env(
            linux_root.path(),
            Platform::Linux,
            fake_env(HashMap::from([("XDG_CONFIG_HOME", xdg)])),
        );
        let linux_desktop = by_id(&linux_items, "claude-desktop");
        assert!(linux_desktop.detected);
        assert_eq!(
            linux_desktop.detail,
            linux_three_party.display().to_string()
        );
    }

    #[test]
    fn opencode_honors_xdg_config_home() {
        let root = tempfile::tempdir().unwrap();
        let xdg = root.path().join("xdg");
        fs::create_dir_all(xdg.join("opencode")).unwrap();
        let env = fake_env(HashMap::from([("XDG_CONFIG_HOME", xdg.clone())]));
        let items = detect_clients_with_env(root.path(), Platform::Linux, env);
        let opencode = by_id(&items, "opencode");
        assert!(opencode.detected);
        assert_eq!(opencode.detail, xdg.join("opencode").display().to_string());
    }

    #[test]
    fn empty_override_falls_back_to_default() {
        let root = tempfile::tempdir().unwrap();
        let env = |name: &str| (name == "CODEX_HOME").then(OsString::new);
        let items = detect_clients_with_env(root.path(), Platform::Linux, env);
        assert_eq!(
            by_id(&items, "codex").detail,
            root.path().join(".codex").display().to_string()
        );
    }

    #[test]
    fn all_ten_tools_remain_in_the_first_release_inventory() {
        let root = tempfile::tempdir().unwrap();
        let items = detect_clients_with_env(root.path(), Platform::Macos, |_| None);
        assert_eq!(items.len(), 10);
        assert_eq!(
            items.iter().map(|item| item.id).collect::<Vec<_>>(),
            vec![
                "claude-code",
                "codex",
                "gemini",
                "claude-desktop",
                "grok-build",
                "opencode",
                "openclaw",
                "hermes",
                "pi",
                "minimax-code"
            ]
        );
    }
}
