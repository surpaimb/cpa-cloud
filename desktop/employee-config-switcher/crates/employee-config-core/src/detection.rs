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
        ("gemini", "Gemini CLI", vec![gemini_home(home, &env)]),
        (
            "claude-desktop",
            "Claude Desktop",
            claude_desktop_candidates(home, platform, &env),
        ),
        ("grok-build", "Grok Build", vec![grok_home(home, &env)]),
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
            vec![minimax_detection_home(home, &env)],
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

// Detection intentionally covers only the fixed client's public environment
// precedence and plain default. CLI --data-dir/--profile and migration-aware
// legacy fallback remain unsupported for any future live-write caller.
fn minimax_detection_home<F>(home: &Path, env: &F) -> PathBuf
where
    F: Fn(&str) -> Option<OsString>,
{
    minimax_public_env_path(env, "MINIMAX_DATA_DIR")
        .or_else(|| minimax_public_env_path(env, "MAVIS_DATA_DIR"))
        .unwrap_or_else(|| home.join(".minimax"))
}

fn minimax_public_env_path<F>(env: &F, name: &str) -> Option<PathBuf>
where
    F: Fn(&str) -> Option<OsString>,
{
    env(name).and_then(|value| {
        value
            .to_str()
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .map(PathBuf::from)
    })
}

fn claude_desktop_candidates<F>(home: &Path, platform: Platform, env: &F) -> Vec<PathBuf>
where
    F: Fn(&str) -> Option<OsString>,
{
    match platform {
        Platform::Windows => {
            let roaming = env_path(env, "APPDATA", home.join("AppData").join("Roaming"));
            vec![roaming.join("Claude").join("claude_desktop_config.json")]
        }
        Platform::Macos => {
            let application_support = home.join("Library").join("Application Support");
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

// Mirrors the fixed official resolver: a non-empty GROK_HOME wins verbatim.
fn grok_home<F>(home: &Path, env: &F) -> PathBuf
where
    F: Fn(&str) -> Option<OsString>,
{
    env_path(env, "GROK_HOME", home.join(".grok"))
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

fn gemini_home<F>(home: &Path, env: &F) -> PathBuf
where
    F: Fn(&str) -> Option<OsString>,
{
    env("GEMINI_CLI_HOME")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| home.to_path_buf())
        .join(".gemini")
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
        let gemini_root = root.path().join("custom-gemini-home");
        let gemini = gemini_root.join(".gemini");
        let hermes = root.path().join("custom-hermes");
        let pi = root.path().join("custom-pi");
        for path in [&codex, &gemini, &hermes, &pi] {
            fs::create_dir_all(path).unwrap();
        }
        let env = fake_env(HashMap::from([
            ("CODEX_HOME", codex.clone()),
            ("GEMINI_CLI_HOME", gemini_root),
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
    fn windows_desktop_uses_roaming_app_data_and_requires_the_official_config_file() {
        let root = tempfile::tempdir().unwrap();
        let roaming = root.path().join("RoamingAppData");
        let official = roaming.join("Claude").join("claude_desktop_config.json");
        fs::create_dir_all(official.parent().unwrap()).unwrap();
        fs::write(&official, br#"{"mcpServers":{}}"#).unwrap();
        let env = fake_env(HashMap::from([("APPDATA", roaming)]));
        let items = detect_clients_with_env(root.path(), Platform::Windows, env);
        let desktop = by_id(&items, "claude-desktop");
        assert!(desktop.detected);
        assert_eq!(desktop.detail, official.display().to_string());
    }

    #[test]
    fn windows_desktop_ignores_local_app_data_and_directory_only_false_positives() {
        let root = tempfile::tempdir().unwrap();
        let roaming = root.path().join("RoamingAppData");
        let local = root.path().join("LocalAppData");
        fs::create_dir_all(roaming.join("Claude")).unwrap();
        fs::create_dir_all(local.join("Claude")).unwrap();
        fs::create_dir_all(local.join("Claude-3p")).unwrap();
        let env = fake_env(HashMap::from([
            ("APPDATA", roaming.clone()),
            ("LOCALAPPDATA", local),
        ]));
        let items = detect_clients_with_env(root.path(), Platform::Windows, env);
        let desktop = by_id(&items, "claude-desktop");
        assert!(!desktop.detected);
        assert_eq!(
            desktop.detail,
            roaming
                .join("Claude")
                .join("claude_desktop_config.json")
                .display()
                .to_string()
        );
    }

    #[test]
    fn windows_desktop_falls_back_to_the_home_roaming_path() {
        let root = tempfile::tempdir().unwrap();
        let official = root
            .path()
            .join("AppData")
            .join("Roaming")
            .join("Claude")
            .join("claude_desktop_config.json");
        fs::create_dir_all(official.parent().unwrap()).unwrap();
        fs::write(&official, br#"{"mcpServers":{}}"#).unwrap();
        let items = detect_clients_with_env(root.path(), Platform::Windows, |_| None);
        let desktop = by_id(&items, "claude-desktop");
        assert!(desktop.detected);
        assert_eq!(desktop.detail, official.display().to_string());
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
    fn grok_build_honors_the_non_empty_grok_home_override_verbatim() {
        let root = tempfile::tempdir().unwrap();
        let overridden = root.path().join("custom-grok");
        fs::create_dir_all(&overridden).unwrap();
        let env = fake_env(HashMap::from([("GROK_HOME", overridden.clone())]));
        let items = detect_clients_with_env(root.path(), Platform::Linux, env);
        let grok = by_id(&items, "grok-build");
        assert!(grok.detected);
        assert_eq!(grok.detail, overridden.display().to_string());
    }

    #[test]
    fn grok_build_ignores_an_empty_override_and_uses_the_home_default() {
        let root = tempfile::tempdir().unwrap();
        let default = root.path().join(".grok");
        fs::create_dir_all(&default).unwrap();
        let env = |name: &str| (name == "GROK_HOME").then(OsString::new);
        let items = detect_clients_with_env(root.path(), Platform::Windows, env);
        let grok = by_id(&items, "grok-build");
        assert!(grok.detected);
        assert_eq!(grok.detail, default.display().to_string());
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
    fn empty_gemini_home_override_falls_back_to_home_dot_gemini() {
        let root = tempfile::tempdir().unwrap();
        let env = |name: &str| (name == "GEMINI_CLI_HOME").then(OsString::new);
        let items = detect_clients_with_env(root.path(), Platform::Linux, env);
        assert_eq!(
            by_id(&items, "gemini").detail,
            root.path().join(".gemini").display().to_string()
        );
    }

    #[test]
    fn minimax_detection_uses_the_fixed_public_environment_precedence() {
        let root = tempfile::tempdir().unwrap();
        let primary = root.path().join("primary");
        let legacy = root.path().join("legacy");
        fs::create_dir_all(&primary).unwrap();
        fs::create_dir_all(&legacy).unwrap();
        let env = fake_env(HashMap::from([
            ("MINIMAX_DATA_DIR", primary.clone()),
            ("MAVIS_DATA_DIR", legacy),
        ]));
        let items = detect_clients_with_env(root.path(), Platform::Windows, env);
        assert_eq!(
            by_id(&items, "minimax-code").detail,
            primary.display().to_string()
        );
        assert!(by_id(&items, "minimax-code").detected);
    }

    #[test]
    fn minimax_detection_ignores_empty_overrides_in_order() {
        let root = tempfile::tempdir().unwrap();
        let legacy = root.path().join("legacy");
        let env = |name: &str| match name {
            "MINIMAX_DATA_DIR" => Some(OsString::new()),
            "MAVIS_DATA_DIR" => Some(legacy.clone().into_os_string()),
            _ => None,
        };
        let items = detect_clients_with_env(root.path(), Platform::Linux, env);
        assert_eq!(
            by_id(&items, "minimax-code").detail,
            legacy.display().to_string()
        );

        let empty =
            |name: &str| matches!(name, "MINIMAX_DATA_DIR" | "MAVIS_DATA_DIR").then(OsString::new);
        let items = detect_clients_with_env(root.path(), Platform::Linux, empty);
        assert_eq!(
            by_id(&items, "minimax-code").detail,
            root.path().join(".minimax").display().to_string()
        );
    }

    #[test]
    fn minimax_detection_ignores_whitespace_primary_before_legacy() {
        let root = tempfile::tempdir().unwrap();
        let legacy = root.path().join("legacy");
        let env = |name: &str| match name {
            "MINIMAX_DATA_DIR" => Some(OsString::from(" \t\r\n ")),
            "MAVIS_DATA_DIR" => Some(legacy.clone().into_os_string()),
            _ => None,
        };
        let items = detect_clients_with_env(root.path(), Platform::Linux, env);
        assert_eq!(
            by_id(&items, "minimax-code").detail,
            legacy.display().to_string()
        );
    }

    #[test]
    fn minimax_detection_trims_the_selected_public_override() {
        let root = tempfile::tempdir().unwrap();
        let primary = root.path().join("primary");
        let padded = OsString::from(format!("  {}  ", primary.display()));
        let env = |name: &str| (name == "MINIMAX_DATA_DIR").then(|| padded.clone());
        let items = detect_clients_with_env(root.path(), Platform::Windows, env);
        assert_eq!(
            by_id(&items, "minimax-code").detail,
            primary.display().to_string()
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
