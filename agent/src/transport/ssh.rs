// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! SSH transport using russh for network device management.

use anyhow::{anyhow, Context, Result};
use russh::keys::key;
use russh::{client, ChannelMsg};
use std::sync::Arc;
use std::time::Duration;
use russh_keys::PublicKeyBase64;
use std::sync::OnceLock;
use tracing::debug;

use crate::state::StateStore;

/// Output of a single SSH command execution.
#[derive(Debug, Clone)]
pub struct ExecOutput {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: u32,
}

/// The host-key pin store, installed once at startup from the agent's state
/// store (`install_host_key_store`). Proxied-device SSH refuses to connect
/// without it rather than fall back to accepting any key.
static HOST_KEY_STORE: OnceLock<Arc<StateStore>> = OnceLock::new();

/// Install the store that pins proxied-device SSH host keys. Called once by
/// the daemon (and `vigo run`) after the state store opens.
pub fn install_host_key_store(store: Arc<StateStore>) {
    let _ = HOST_KEY_STORE.set(store);
}

/// Outcome of a host-key check that did not refuse the connection.
#[derive(Debug, PartialEq, Eq)]
pub enum HostKeyCheck {
    /// First connection to this host:port — the presented key is now pinned.
    Pinned,
    /// The presented key matches the pinned one.
    Matched,
}

/// The pin record for a key: "<algorithm> <base64>".
fn host_key_record(key: &key::PublicKey) -> String {
    format!("{} {}", key.name(), key.public_key_base64())
}

/// check_and_pin_host_key is trust-on-first-use with the first use
/// remembered: the first key seen for `key_id` ("host:port") is stored in the
/// state store, and every later connection must present exactly that key.
/// A different key is refused — it is what an on-path attacker impersonating
/// the device presents — and the only way past it is an operator running
/// `vigo ssh-hostkey forget <host:port>` on the gateway envoy.
pub fn check_and_pin_host_key(
    store: &StateStore,
    key_id: &str,
    presented: &str,
) -> Result<HostKeyCheck> {
    let pinned = {
        let rtxn = store.read_txn()?;
        store.ssh_keys.load(&rtxn, key_id)?
    };
    match pinned {
        Some(p) if p == presented => Ok(HostKeyCheck::Matched),
        Some(p) => {
            let algo = |r: &str| r.split_whitespace().next().unwrap_or("").to_string();
            Err(anyhow!(
                "SSH host key for {key_id} does not match the key pinned on first connect \
                 (pinned {}, presented {}) — refusing to connect: possible man-in-the-middle. \
                 If the device's host key was legitimately replaced, run \
                 `vigo ssh-hostkey forget {key_id}` on this gateway envoy; the next \
                 connection pins the new key.",
                algo(&p),
                algo(presented)
            ))
        }
        None => {
            let mut wtxn = store.write_txn()?;
            store.ssh_keys.store(&mut wtxn, key_id, presented)?;
            wtxn.commit()?;
            tracing::info!(host = %key_id, key = %presented.split_whitespace().next().unwrap_or(""), "pinned SSH host key on first connect");
            Ok(HostKeyCheck::Pinned)
        }
    }
}

/// Handler for russh client events.
struct ClientHandler {
    /// "host:port" — the pin-store key for this device.
    key_id: String,
}

#[async_trait::async_trait]
impl client::Handler for ClientHandler {
    type Error = anyhow::Error;

    async fn check_server_key(
        &mut self,
        server_public_key: &key::PublicKey,
    ) -> Result<bool, Self::Error> {
        let store = HOST_KEY_STORE.get().ok_or_else(|| {
            anyhow!(
                "SSH host-key pin store unavailable (the agent state store did not open) — \
                 refusing to connect to {} without host-key verification",
                self.key_id
            )
        })?;
        check_and_pin_host_key(store, &self.key_id, &host_key_record(server_public_key))?;
        Ok(true)
    }
}

/// An established SSH session to a network device.
pub struct SshSession {
    handle: client::Handle<ClientHandler>,
}

impl SshSession {
    /// Connect and authenticate with password.
    pub async fn connect_password(
        host: &str,
        port: u16,
        username: &str,
        password: &str,
    ) -> Result<Self> {
        let config = Arc::new(client::Config {
            inactivity_timeout: Some(Duration::from_secs(60)),
            ..Default::default()
        });
        let handler = ClientHandler {
            key_id: format!("{}:{}", host, port),
        };

        let mut handle = client::connect(config, (host, port), handler)
            .await
            .with_context(|| format!("SSH connect to {}:{}", host, port))?;

        let auth_ok = handle
            .authenticate_password(username, password)
            .await
            .with_context(|| format!("SSH password auth to {}:{}", host, port))?;
        if !auth_ok {
            return Err(anyhow!("SSH password authentication failed for {}@{}:{}", username, host, port));
        }

        debug!(host, port, username, "SSH session established (password)");
        Ok(Self { handle })
    }

    /// Connect and authenticate with an SSH private key (PEM-encoded).
    pub async fn connect_key(
        host: &str,
        port: u16,
        username: &str,
        key_pem: &str,
    ) -> Result<Self> {
        let config = Arc::new(client::Config {
            inactivity_timeout: Some(Duration::from_secs(60)),
            ..Default::default()
        });
        let handler = ClientHandler {
            key_id: format!("{}:{}", host, port),
        };

        let key_pair = russh_keys::decode_secret_key(key_pem, None)
            .with_context(|| "parsing SSH private key")?;

        let mut handle = client::connect(config, (host, port), handler)
            .await
            .with_context(|| format!("SSH connect to {}:{}", host, port))?;

        let auth_ok = handle
            .authenticate_publickey(username, Arc::new(key_pair))
            .await
            .with_context(|| format!("SSH key auth to {}:{}", host, port))?;
        if !auth_ok {
            return Err(anyhow!("SSH key authentication failed for {}@{}:{}", username, host, port));
        }

        debug!(host, port, username, "SSH session established (key)");
        Ok(Self { handle })
    }

    /// Connect using a credential string. If it looks like a PEM private key,
    /// uses key auth; otherwise treats it as a password.
    pub async fn connect(
        host: &str,
        port: u16,
        username: &str,
        credential: &str,
    ) -> Result<Self> {
        if credential.contains("PRIVATE KEY") {
            Self::connect_key(host, port, username, credential).await
        } else {
            Self::connect_password(host, port, username, credential).await
        }
    }

    /// Execute a single command, return output with timeout.
    pub async fn exec(&self, command: &str, timeout: Duration) -> Result<ExecOutput> {
        let mut channel = self
            .handle
            .channel_open_session()
            .await
            .context("opening SSH channel")?;

        channel.exec(true, command).await.context("SSH exec")?;

        let mut stdout = Vec::new();
        let mut stderr = Vec::new();
        let mut exit_code: u32 = 0;

        let result = tokio::time::timeout(timeout, async {
            loop {
                match channel.wait().await {
                    Some(ChannelMsg::Data { data }) => {
                        stdout.extend_from_slice(&data);
                    }
                    Some(ChannelMsg::ExtendedData { data, ext }) => {
                        if ext == 1 {
                            // stderr
                            stderr.extend_from_slice(&data);
                        }
                    }
                    Some(ChannelMsg::ExitStatus { exit_status }) => {
                        exit_code = exit_status;
                    }
                    Some(ChannelMsg::Eof | ChannelMsg::Close) | None => break,
                    _ => {}
                }
            }
        })
        .await;

        if result.is_err() {
            return Err(anyhow!(
                "SSH command timed out after {:?}: {}",
                timeout,
                command
            ));
        }

        Ok(ExecOutput {
            stdout: String::from_utf8_lossy(&stdout).to_string(),
            stderr: String::from_utf8_lossy(&stderr).to_string(),
            exit_code,
        })
    }

    /// Interactive CLI session for devices requiring enable/config mode.
    /// Sends commands sequentially, waiting for the prompt between each.
    pub async fn exec_interactive(
        &self,
        commands: &[&str],
        prompt_pattern: &str,
        timeout: Duration,
    ) -> Result<String> {
        let mut channel = self
            .handle
            .channel_open_session()
            .await
            .context("opening SSH channel")?;

        // Request a PTY for interactive mode.
        channel
            .request_pty(false, "vt100", 0, 0, 0, 0, &[])
            .await
            .context("requesting PTY")?;
        channel
            .request_shell(false)
            .await
            .context("requesting shell")?;

        let mut output = String::new();

        for cmd in commands {
            // Send command.
            channel
                .data(format!("{}\n", cmd).as_bytes())
                .await
                .context("sending command")?;

            // Read until prompt appears.
            let read_result = tokio::time::timeout(timeout, async {
                loop {
                    match channel.wait().await {
                        Some(ChannelMsg::Data { data }) => {
                            let chunk = String::from_utf8_lossy(&data);
                            output.push_str(&chunk);
                            if output.contains(prompt_pattern) {
                                break;
                            }
                        }
                        Some(ChannelMsg::Eof | ChannelMsg::Close) | None => break,
                        _ => {}
                    }
                }
            })
            .await;

            if read_result.is_err() {
                return Err(anyhow!(
                    "SSH interactive command timed out after {:?}: {}",
                    timeout,
                    cmd
                ));
            }
        }

        Ok(output)
    }

    /// Close the SSH session.
    pub async fn close(self) -> Result<()> {
        self.handle
            .disconnect(russh::Disconnect::ByApplication, "done", "en")
            .await
            .context("SSH disconnect")?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// First connect pins the key; the same key passes; a different key is
    /// refused and stays refused (it never overwrites the pin) until the
    /// operator forgets the pin. Before this, every connection accepted any
    /// key and nothing was stored.
    #[test]
    fn host_key_pinned_on_first_connect_and_mismatch_refused() {
        let dir = tempfile::tempdir().unwrap();
        let store = StateStore::open(dir.path()).unwrap();
        let id = "10.0.0.7:22";
        let real = "ssh-ed25519 AAAAREAL";
        let mitm = "ssh-ed25519 AAAAMITM";

        assert_eq!(check_and_pin_host_key(&store, id, real).unwrap(), HostKeyCheck::Pinned);
        assert_eq!(check_and_pin_host_key(&store, id, real).unwrap(), HostKeyCheck::Matched);

        let err = check_and_pin_host_key(&store, id, mitm).unwrap_err();
        assert!(format!("{err}").contains("vigo ssh-hostkey forget 10.0.0.7:22"), "got: {err}");
        // The refused key did not replace the pin.
        assert_eq!(check_and_pin_host_key(&store, id, real).unwrap(), HostKeyCheck::Matched);
        assert!(check_and_pin_host_key(&store, id, mitm).is_err());

        // Pins are per host:port.
        assert_eq!(check_and_pin_host_key(&store, "10.0.0.7:2222", mitm).unwrap(), HostKeyCheck::Pinned);

        // Forgetting the pin lets the next connection pin the new key.
        let mut wtxn = store.write_txn().unwrap();
        assert!(store.ssh_keys.delete(&mut wtxn, id).unwrap());
        wtxn.commit().unwrap();
        assert_eq!(check_and_pin_host_key(&store, id, mitm).unwrap(), HostKeyCheck::Pinned);
    }

    #[test]
    fn test_exec_output_display() {
        let output = ExecOutput {
            stdout: "hello\n".to_string(),
            stderr: String::new(),
            exit_code: 0,
        };
        assert_eq!(output.exit_code, 0);
        assert_eq!(output.stdout, "hello\n");
    }

    #[test]
    fn test_credential_detection() {
        // Password credential.
        assert!(!("mypassword".contains("PRIVATE KEY")));
        // Key credential.
        assert!("-----BEGIN OPENSSH PRIVATE KEY-----\ndata\n-----END OPENSSH PRIVATE KEY-----".contains("PRIVATE KEY"));
    }
}
