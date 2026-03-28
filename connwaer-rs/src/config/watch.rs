use anyhow::Result;
use std::path::{Path, PathBuf};
use tokio::sync::mpsc;

/// Watches a config file for modifications and sends a notification on change.
pub struct ConfigWatcher {
    path: PathBuf,
    tx: mpsc::Sender<()>,
}

impl ConfigWatcher {
    pub fn new(path: &Path, tx: mpsc::Sender<()>) -> Self {
        Self {
            path: path.to_path_buf(),
            tx,
        }
    }

    /// Start watching. Runs until the sender is dropped.
    pub async fn watch(&self) -> Result<()> {
        self.poll_watch().await
    }

    /// Fallback: poll-based watcher that checks mtime every 5 seconds.
    /// Works on all platforms. Can be replaced with inotify/ReadDirectoryChangesW later.
    async fn poll_watch(&self) -> Result<()> {
        let mut last_modified = std::fs::metadata(&self.path)
            .and_then(|m| m.modified())
            .ok();

        loop {
            tokio::time::sleep(std::time::Duration::from_secs(5)).await;

            let current = std::fs::metadata(&self.path)
                .and_then(|m| m.modified())
                .ok();

            if current != last_modified {
                last_modified = current;
                if self.tx.send(()).await.is_err() {
                    break;
                }
            }
        }

        Ok(())
    }
}
