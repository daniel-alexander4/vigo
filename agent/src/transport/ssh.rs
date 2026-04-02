// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! SSH transport using russh for network device management.

use anyhow::{anyhow, Context, Result};
use russh::keys::key;
use russh::{client, ChannelMsg};
use std::sync::Arc;
use std::time::Duration;
use russh_keys::PublicKeyBase64;
use tracing::debug;

/// Output of a single SSH command execution.
#[derive(Debug, Clone)]
pub struct ExecOutput {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: u32,
}

/// Handler for russh client events.
struct ClientHandler {
    /// Known host key for verification. None = first-connect (TOFU).
    known_key: Option<key::PublicKey>,
    /// Set to true after successful key verification.
    key_accepted: bool,
}

#[async_trait::async_trait]
impl client::Handler for ClientHandler {
    type Error = anyhow::Error;

    async fn check_server_key(
        &mut self,
        server_public_key: &key::PublicKey,
    ) -> Result<bool, Self::Error> {
        if let Some(ref known) = self.known_key {
            // Verify against cached key.
            let known_bytes = known.public_key_base64();
            let server_bytes = server_public_key.public_key_base64();
            if known_bytes != server_bytes {
                return Err(anyhow!(
                    "SSH host key mismatch — possible MITM attack. \
                     Expected {:?}, got {:?}",
                    known.name(),
                    server_public_key.name()
                ));
            }
        }
        // TOFU: accept on first connect.
        self.key_accepted = true;
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
        known_key: Option<key::PublicKey>,
    ) -> Result<Self> {
        let config = Arc::new(client::Config {
            inactivity_timeout: Some(Duration::from_secs(60)),
            ..Default::default()
        });
        let handler = ClientHandler {
            known_key,
            key_accepted: false,
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
        known_key: Option<key::PublicKey>,
    ) -> Result<Self> {
        let config = Arc::new(client::Config {
            inactivity_timeout: Some(Duration::from_secs(60)),
            ..Default::default()
        });
        let handler = ClientHandler {
            known_key,
            key_accepted: false,
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
        known_key: Option<key::PublicKey>,
    ) -> Result<Self> {
        if credential.contains("PRIVATE KEY") {
            Self::connect_key(host, port, username, credential, known_key).await
        } else {
            Self::connect_password(host, port, username, credential, known_key).await
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
