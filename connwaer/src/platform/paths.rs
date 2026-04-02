/// Platform-specific default paths for Connwaer.

#[cfg(unix)]
pub const DEFAULT_CONFIG_PATH: &str = "/etc/connwaer/connwaer.yaml";
#[cfg(unix)]
pub const DEFAULT_RESULTS_DIR: &str = "/var/lib/connwaer/results";
#[cfg(unix)]
pub const DEFAULT_TRENDS_DB: &str = "/var/lib/connwaer/trends.db";
#[cfg(unix)]
pub const DEFAULT_LICENSE_DIR: &str = "/srv/vigo/license";
#[cfg(unix)]
pub const DEFAULT_BINARY_PATH: &str = "/usr/local/sbin/connwaer";
#[cfg(unix)]
pub const DEFAULT_DATA_DIR: &str = "/var/lib/connwaer";

#[cfg(windows)]
pub const DEFAULT_CONFIG_PATH: &str = r"C:\ProgramData\Connwaer\connwaer.yaml";
#[cfg(windows)]
pub const DEFAULT_RESULTS_DIR: &str = r"C:\ProgramData\Connwaer\results";
#[cfg(windows)]
pub const DEFAULT_TRENDS_DB: &str = r"C:\ProgramData\Connwaer\trends.db";
#[cfg(windows)]
pub const DEFAULT_LICENSE_DIR: &str = r"C:\ProgramData\Vigo\license";
#[cfg(windows)]
pub const DEFAULT_BINARY_PATH: &str = r"C:\Program Files\Connwaer\connwaer.exe";
#[cfg(windows)]
pub const DEFAULT_DATA_DIR: &str = r"C:\ProgramData\Connwaer";
