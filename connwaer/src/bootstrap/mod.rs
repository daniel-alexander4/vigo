#[cfg(target_os = "linux")]
pub mod systemd;
#[cfg(windows)]
pub mod windows_service;

use anyhow::Result;

pub fn install_service() -> Result<()> {
    #[cfg(target_os = "linux")]
    {
        return systemd::install();
    }
    #[cfg(windows)]
    {
        return windows_service::install();
    }
    #[cfg(not(any(target_os = "linux", windows)))]
    {
        anyhow::bail!("install-service not supported on this platform");
    }
}
