use employee_config_core::{ClientConfiguration, ClientDetection, DiscoveryRequest};

#[tauri::command]
fn detect_clients() -> Result<Vec<ClientDetection>, String> {
    let home = dirs::home_dir().ok_or_else(|| "无法确定当前用户主目录".to_string())?;
    Ok(employee_config_core::detect_clients(&home))
}

#[tauri::command]
async fn discover_configuration(request: DiscoveryRequest) -> Result<ClientConfiguration, String> {
    employee_config_core::discover_configuration(&request)
        .await
        .map_err(|error| error.to_string())
}

#[tauri::command]
async fn apply_configuration(_request: DiscoveryRequest) -> Result<(), String> {
    Err(
        "安全写入仍处于开发预览：Windows DACL、持久恢复日志和并发回滚门槛尚未全部通过；本次没有写入任何文件"
            .into(),
    )
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .invoke_handler(tauri::generate_handler![
            detect_clients,
            discover_configuration,
            apply_configuration
        ])
        .run(tauri::generate_context!())
        .expect("failed to run CPA Cloud employee configuration client");
}
