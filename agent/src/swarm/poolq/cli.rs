// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! `vigo swarm poolq <verb>` — ADR-029 founder + consumer CLI.
//!
//! Read verbs (`topics` / `subscribe` / `read` / `tail` / `seek` / `offset`)
//! go through the public REST endpoints at `/api/v1/swarm/poolq/...`; the
//! write verb (`post`) signs via the per-user puddle session helper and
//! writes the message file under `~/.vigo-poolq/` (no helper-daemon hop —
//! see the "No root helper daemon" line in ADR-029).
//!
//! Topic references accept two forms anywhere a `<topic>` argument is taken:
//!
//! - **64-hex `topic_id`** — always works (the hex form `topic_id =
//!   sha256(author_pubkey ‖ topic_name)` is self-certifying like a curator
//!   `artifact_id` or a gitback `project_id`).
//! - **bare `<topic_name>`** — resolved against the local user's puddle
//!   pubkey. The right shape for "I'm reading the topic I founded" and
//!   "I'm posting to my own topic." Cross-puddle reads use the hex form.
//!
//! Friendly `poolq://<puddle-name>/<topic>` resolution lands when the
//! ADR-022 fleet name map integration ships (Slice 5 docs add the
//! recipe — until then operators use the hex form for cross-puddle
//! reads, same papercut gitback had before 0.49.0).

use std::io::Read;
use std::path::{Path, PathBuf};

use anyhow::{anyhow, bail, Context, Result};
use serde::Deserialize;

use super::message::{Message, MessageBody, ID_LEN, SIG_LEN};
use super::post::{publish, PublishParams};
use super::subscriptions::{load as load_subs, save as save_subs, Subscription};
use crate::auth;
use crate::swarm::puddle;
use crate::update::TlsCerts;

/// Top-level dispatch for `vigo swarm poolq …`. Called from
/// `agent/src/main.rs`.
pub fn run(args: Vec<String>) -> Result<()> {
    let verb = args.first().map(String::as_str).unwrap_or("");
    match verb {
        "" | "--help" | "-h" | "help" => {
            print_usage();
            Ok(())
        }
        "post" => with_poolq_authz(|| cmd_post(&args[1..])),
        "topics" => cmd_topics(&args[1..]),
        "subscribe" => cmd_subscribe(&args[1..]),
        "read" => cmd_read(&args[1..]),
        "tail" => cmd_tail(&args[1..]),
        "seek" => cmd_seek(&args[1..]),
        "offset" => cmd_offset(&args[1..]),
        "status" => cmd_status(&args[1..]),
        other => bail!(
            "unknown subcommand `vigo swarm poolq {}`. \
             Run `vigo swarm poolq --help` for usage.",
            other
        ),
    }
}

/// Refuse early if the invoking user lacks `poolq: true` in their usercrate.
/// Same posture as `vigo swarm curator`'s `with_curator_authz`: front-load
/// the error so the operator sees it before the CLI builds + signs a
/// message that would be rejected. Skipped for root.
fn with_poolq_authz<F, T>(f: F) -> Result<T>
where
    F: FnOnce() -> Result<T>,
{
    let caller = resolve_caller()?;
    if !caller.is_root {
        crate::swarm::policy::require_poolq(&caller.username)?;
    }
    f()
}

fn print_usage() {
    eprintln!(
        "vigo swarm poolq — ADR-029 P2P ordered-log queue CLI

USAGE:
    vigo swarm poolq <verb> [args]

PUBLISH (founder):
    post <topic-name> --body <text>     Post a message (UTF-8 text).
    post <topic-name> --stdin           Read body from stdin.

READ (consumer):
    topics                              Every topic the fleet knows about.
    subscribe <topic>                   Record an offset=0 subscription so
                                        'read' has somewhere to advance.
    read <topic> [--limit N]            Pull messages from offset+1 to head,
                                        print them, advance the offset.
    tail <topic> [--limit N]            Print the latest N messages (head −
                                        N+1 to head) without touching the
                                        offset. v1: one-shot, no follow.
    seek <topic> <seq>                  Set the local offset to <seq>−1 so
                                        the next 'read' returns from <seq>.
                                        seek <topic> 0 replays the full log.
    offset <topic>                      Print the current offset.
    status                              One-line poolq posture summary.

TOPIC REFS:
    <64-hex>                            A topic_id; always resolves.
    <topic-name>                        Treated as 'topic founded by THIS
                                        user' — topic_id = sha256(local
                                        puddle pubkey ‖ topic-name).

State:    ~/.vigo-poolq/<topic_id>/messages/  (founder; trait-collected)
          ~/.vigo-poolq/<topic_id>/allocator.json  (founder, flock-protected)
          ~/.vigo-poolq/subscriptions.json    (consumer offsets, per-user,
                                               never replicated)
"
    );
}

// =========================================================================
// post — local, signs via puddle session helper, writes under ~/.vigo-poolq/
// =========================================================================

fn cmd_post(args: &[String]) -> Result<()> {
    let mut topic_name: Option<String> = None;
    let mut body: Option<String> = None;
    let mut from_stdin = false;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--body" => {
                if i + 1 >= args.len() {
                    bail!("--body requires a value");
                }
                body = Some(args[i + 1].clone());
                i += 2;
            }
            "--stdin" => {
                from_stdin = true;
                i += 1;
            }
            a if !a.starts_with("--") && topic_name.is_none() => {
                topic_name = Some(a.to_string());
                i += 1;
            }
            other => bail!("unknown post arg: {other}"),
        }
    }
    let topic = topic_name
        .ok_or_else(|| anyhow!("usage: vigo swarm poolq post <topic-name> --body <text> | --stdin"))?;
    if body.is_some() == from_stdin {
        bail!("exactly one of --body or --stdin is required");
    }
    let body = if from_stdin {
        let mut s = String::new();
        std::io::stdin()
            .read_to_string(&mut s)
            .context("reading body from stdin")?;
        s
    } else {
        body.unwrap()
    };

    let caller = resolve_caller()?;
    let author_pubkey = local_puddle_pubkey_bytes(&caller.home)?;
    let max_msg_bytes = crate::swarm::runtime_limits::poolq_max_msg_bytes();
    let posted = publish(
        PublishParams {
            home: &caller.home,
            author_puddle_pubkey: author_pubkey,
            topic_name: &topic,
            body: &body,
            posted_at: now_millis(),
            max_msg_bytes: Some(max_msg_bytes),
        },
        sign_with_session,
    )?;
    println!(
        "Posted to {}/{} as seq {} (msg_id {})",
        short_hex(&posted.topic_id_hex),
        topic,
        posted.seq,
        short_hex(&posted.msg_id_hex)
    );
    Ok(())
}

// =========================================================================
// topics — fleet-wide list, server REST
// =========================================================================

fn cmd_topics(args: &[String]) -> Result<()> {
    let as_json = args.iter().any(|a| a == "--json");
    let client = build_rest()?;
    let resp: TopicsResponse = client.get_json("/api/v1/swarm/poolq/topics")?;
    if as_json {
        let bytes = serde_json::to_vec_pretty(&resp).context("serializing topics response")?;
        println!("{}", String::from_utf8_lossy(&bytes));
        return Ok(());
    }
    if resp.topics.is_empty() {
        println!("No poolq topics on the fleet.");
        return Ok(());
    }
    println!(
        "Topics — generated {}",
        resp.generated_at.as_deref().unwrap_or("?")
    );
    println!();
    println!(
        "{:<13}  {:<24}  {:>10}  {:>5}  {:<12}",
        "TOPIC_ID", "NAME", "HEAD_SEQ", "COUNT", "FOUNDER"
    );
    for t in &resp.topics {
        println!(
            "{:<13}  {:<24}  {:>10}  {:>5}  {:<12}",
            short_hex(&t.topic_id_hex),
            truncate(&t.topic_name, 24),
            t.head_seq,
            t.count,
            short_hex(&t.founder_puddle_pubkey),
        );
    }
    Ok(())
}

// =========================================================================
// subscribe — purely local subscriptions.json mutation
// =========================================================================

fn cmd_subscribe(args: &[String]) -> Result<()> {
    if args.is_empty() {
        bail!("usage: vigo swarm poolq subscribe <topic>");
    }
    let caller = resolve_caller()?;
    let (topic_id_hex, topic_name) = resolve_topic_ref(&args[0], &caller.home)?;
    let mut subs = load_subs(&caller.home)?;
    let existing = subs.subscriptions.get(&topic_id_hex).cloned().unwrap_or_default();
    let entry = Subscription {
        topic_name: topic_name.unwrap_or(existing.topic_name),
        offset_seq: existing.offset_seq,
        filter: existing.filter,
    };
    subs.subscriptions.insert(topic_id_hex.clone(), entry.clone());
    save_subs(&caller.home, &subs)?;
    println!(
        "Subscribed to {} (offset {}, name {:?})",
        short_hex(&topic_id_hex),
        entry.offset_seq,
        entry.topic_name
    );
    Ok(())
}

// =========================================================================
// read — pulls new messages, advances offset
// =========================================================================

fn cmd_read(args: &[String]) -> Result<()> {
    let (topic_arg, limit, as_json) = parse_read_args(args, "read")?;
    let caller = resolve_caller()?;
    let (topic_id_hex, topic_name) = resolve_topic_ref(&topic_arg, &caller.home)?;

    let mut subs = load_subs(&caller.home)?;
    let entry = subs.subscriptions.entry(topic_id_hex.clone()).or_default();
    if let Some(name) = topic_name.as_ref() {
        if entry.topic_name.is_empty() {
            entry.topic_name = name.clone();
        }
    }
    let from = entry.offset_seq + 1;

    let client = build_rest()?;
    let path = format!(
        "/api/v1/swarm/poolq/topic/{}/range?from={}",
        topic_id_hex, from
    );
    let resp: RangeResponse = client.get_json(&path)?;
    let mut msgs = resp.messages;
    if let Some(n) = limit {
        msgs.truncate(n);
    }
    if msgs.is_empty() {
        println!("No new messages (offset {}, head was already reached).", entry.offset_seq);
        save_subs(&caller.home, &subs)?;
        return Ok(());
    }
    let last = msgs.last().unwrap().body.seq;
    if as_json {
        let bytes = serde_json::to_vec_pretty(&msgs).context("serializing messages")?;
        println!("{}", String::from_utf8_lossy(&bytes));
    } else {
        render_messages(&msgs);
    }
    entry.offset_seq = last;
    save_subs(&caller.home, &subs)?;
    Ok(())
}

// =========================================================================
// tail — head-N to head, doesn't touch the offset
// =========================================================================

fn cmd_tail(args: &[String]) -> Result<()> {
    let (topic_arg, limit, as_json) = parse_read_args(args, "tail")?;
    let limit = limit.unwrap_or(10);
    let caller = resolve_caller()?;
    let (topic_id_hex, _) = resolve_topic_ref(&topic_arg, &caller.home)?;
    let client = build_rest()?;
    let head: TopicSummaryResponse =
        client.get_json(&format!("/api/v1/swarm/poolq/topic/{}/head", topic_id_hex))?;
    let from = head.head_seq.saturating_sub(limit as u64 - 1).max(1);
    let resp: RangeResponse = client.get_json(&format!(
        "/api/v1/swarm/poolq/topic/{}/range?from={}",
        topic_id_hex, from
    ))?;
    if resp.messages.is_empty() {
        println!("Topic is empty.");
        return Ok(());
    }
    if as_json {
        let bytes = serde_json::to_vec_pretty(&resp.messages).context("serializing messages")?;
        println!("{}", String::from_utf8_lossy(&bytes));
    } else {
        render_messages(&resp.messages);
    }
    Ok(())
}

// =========================================================================
// seek / offset — local subscription edits
// =========================================================================

fn cmd_seek(args: &[String]) -> Result<()> {
    if args.len() != 2 {
        bail!("usage: vigo swarm poolq seek <topic> <seq>");
    }
    let seq: u64 = args[1]
        .parse()
        .context("seq must be a non-negative integer")?;
    let caller = resolve_caller()?;
    let (topic_id_hex, topic_name) = resolve_topic_ref(&args[0], &caller.home)?;
    let mut subs = load_subs(&caller.home)?;
    let new_offset = seq.saturating_sub(1);
    {
        let entry = subs.subscriptions.entry(topic_id_hex.clone()).or_default();
        if let Some(name) = topic_name {
            if entry.topic_name.is_empty() {
                entry.topic_name = name;
            }
        }
        // `seek 5` means "next read returns from seq 5", so we store seq-1.
        entry.offset_seq = new_offset;
    }
    save_subs(&caller.home, &subs)?;
    println!(
        "Offset for {} set to {} — next read returns from seq {}",
        short_hex(&topic_id_hex),
        new_offset,
        new_offset + 1
    );
    Ok(())
}

fn cmd_offset(args: &[String]) -> Result<()> {
    if args.len() != 1 {
        bail!("usage: vigo swarm poolq offset <topic>");
    }
    let caller = resolve_caller()?;
    let (topic_id_hex, _) = resolve_topic_ref(&args[0], &caller.home)?;
    let subs = load_subs(&caller.home)?;
    match subs.subscriptions.get(&topic_id_hex) {
        Some(s) => println!(
            "{} offset={} (next read from seq {})",
            short_hex(&topic_id_hex),
            s.offset_seq,
            s.offset_seq + 1
        ),
        None => println!(
            "{} (no subscription — `vigo swarm poolq subscribe {}` to start)",
            short_hex(&topic_id_hex),
            args[0]
        ),
    }
    Ok(())
}

// =========================================================================
// status — quick envoy posture
// =========================================================================

fn cmd_status(_args: &[String]) -> Result<()> {
    let caller = resolve_caller()?;
    let subs = load_subs(&caller.home)?;
    println!("poolq — user {}", caller.username);
    println!("  state dir:        {}", super::state::user_state_dir(&caller.home).display());
    println!("  subscriptions:    {}", subs.subscriptions.len());
    println!("  max_msg_bytes:    {} (server-configured cap)", crate::swarm::runtime_limits::poolq_max_msg_bytes());
    println!("  retention_secs:   {} (server-configured)", crate::swarm::runtime_limits::poolq_retention_secs());
    Ok(())
}

// =========================================================================
// shared helpers
// =========================================================================

/// Resolve a topic reference (64-hex or bare topic_name) into a
/// `(topic_id_hex, topic_name?)` pair. Bare names derive `topic_id` from
/// the local user's puddle pubkey, so "post my own topic" and "read my
/// own topic" both just work with the friendly form; cross-puddle reads
/// pass the hex.
fn resolve_topic_ref(arg: &str, home: &Path) -> Result<(String, Option<String>)> {
    if is_hex64(arg) {
        return Ok((arg.to_ascii_lowercase(), None));
    }
    // Treat as bare topic_name owned by this user.
    super::message::validate_topic_name(arg).context("topic name")?;
    let pk = local_puddle_pubkey_bytes(home)?;
    let id = super::message::topic_id_for(&pk, arg);
    Ok((hex::encode(id), Some(arg.to_string())))
}

fn is_hex64(s: &str) -> bool {
    s.len() == 64 && s.bytes().all(|b| b.is_ascii_hexdigit())
}

fn parse_read_args(args: &[String], verb: &str) -> Result<(String, Option<usize>, bool)> {
    let mut topic: Option<String> = None;
    let mut limit: Option<usize> = None;
    let mut as_json = false;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--limit" => {
                if i + 1 >= args.len() {
                    bail!("--limit requires a value");
                }
                limit = Some(args[i + 1].parse().context("--limit must be a non-negative integer")?);
                i += 2;
            }
            "--json" => {
                as_json = true;
                i += 1;
            }
            a if !a.starts_with("--") && topic.is_none() => {
                topic = Some(a.to_string());
                i += 1;
            }
            other => bail!("unknown {verb} arg: {other}"),
        }
    }
    Ok((
        topic.ok_or_else(|| anyhow!("usage: vigo swarm poolq {verb} <topic> [--limit N] [--json]"))?,
        limit,
        as_json,
    ))
}

fn render_messages(msgs: &[Message]) {
    for m in msgs {
        let posted = m.body.posted_at;
        println!(
            "seq={} posted_at={} msg_id={} body={}",
            m.body.seq,
            posted,
            m.msg_id_hex().unwrap_or_else(|_| "?".into()),
            single_line(&m.body.body)
        );
    }
}

fn single_line(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        if c == '\n' {
            out.push_str("\\n");
        } else if c == '\r' {
            out.push_str("\\r");
        } else {
            out.push(c);
        }
    }
    if out.len() > 200 {
        out.truncate(197);
        out.push_str("...");
    }
    out
}

fn short_hex(s: &str) -> String {
    if s.len() <= 12 {
        s.to_string()
    } else {
        s[..12].to_string()
    }
}

fn truncate(s: &str, max: usize) -> String {
    if s.chars().count() <= max {
        return s.to_string();
    }
    let mut out: String = s.chars().take(max - 1).collect();
    out.push('…');
    out
}

fn now_millis() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

/// Resolved identity of the invoking Unix user — `getuid()` is the source
/// of truth (publish verbs run as the user). Mirrors curator/cli.rs's
/// `CallerIdentity`; the two modules don't share it because there's no
/// natural place for a cross-subsystem "current user" struct.
struct CallerIdentity {
    home: PathBuf,
    is_root: bool,
    username: String,
}

fn resolve_caller() -> Result<CallerIdentity> {
    #[cfg(unix)]
    {
        use nix::unistd::{Uid, User};
        let uid = Uid::current();
        let user = User::from_uid(uid)
            .ok()
            .flatten()
            .context("cannot resolve current Unix user")?;
        Ok(CallerIdentity {
            username: user.name,
            home: user.dir,
            is_root: uid.is_root(),
        })
    }
    #[cfg(not(unix))]
    {
        bail!("poolq CLI is Unix-only")
    }
}

/// Read the caller's local puddle pubkey as raw 32 bytes. Same shape
/// curator's CLI uses; the file format is `ed25519:<64-hex>`.
fn local_puddle_pubkey_bytes(home: &Path) -> Result<[u8; ID_LEN]> {
    let text = puddle::local_puddle_pubkey(home)?.ok_or_else(|| {
        anyhow!(
            "puddle is not initialized for this user. \
             Run `vigo swarm puddle init` (or `join --from <host>`) first."
        )
    })?;
    let hexpart = text
        .strip_prefix("ed25519:")
        .ok_or_else(|| anyhow!("malformed puddle pubkey: missing ed25519: prefix"))?;
    if hexpart.len() != ID_LEN * 2 {
        bail!("puddle pubkey is wrong length: {}", hexpart);
    }
    let bytes = hex::decode(hexpart).context("decode puddle pubkey hex")?;
    let mut out = [0u8; ID_LEN];
    out.copy_from_slice(&bytes);
    Ok(out)
}

/// Sign `payload` with the user's puddle key via the session helper. Same
/// shape gitback's `sign_with_session` and curator's `sign_with_session`
/// use — the session helper is the only place the private key lives.
fn sign_with_session(payload: &[u8]) -> Result<[u8; SIG_LEN]> {
    let sig = puddle::session::request_sign(payload).map_err(|e| {
        anyhow!(
            "could not sign with puddle identity: {}\n\
             Run `vigo swarm puddle unlock` to start the session helper.",
            e
        )
    })?;
    if sig.len() != SIG_LEN {
        bail!(
            "session helper returned a {}-byte signature, expected {}",
            sig.len(),
            SIG_LEN
        );
    }
    let mut out = [0u8; SIG_LEN];
    out.copy_from_slice(&sig);
    Ok(out)
}

// =========================================================================
// REST client — mTLS GET helper, mirrors curator/resolver.rs's build_agent
// =========================================================================

struct RestClient {
    agent: ureq::Agent,
    api_addr: String,
}

impl RestClient {
    fn get_json<T: for<'de> Deserialize<'de>>(&self, path: &str) -> Result<T> {
        let url = format!("{}{}", self.api_addr.trim_end_matches('/'), path);
        let resp = self.agent.get(&url).call().map_err(|e| anyhow!("GET {url}: {e}"))?;
        if resp.status() != 200 {
            bail!("GET {url}: status {}", resp.status());
        }
        let body = resp
            .into_body()
            .read_to_vec()
            .with_context(|| format!("reading {url}"))?;
        serde_json::from_slice(&body).with_context(|| format!("parsing {url}"))
    }
}

fn build_rest() -> Result<RestClient> {
    let data_dir = crate::platform::paths::default_data_dir();
    let cfg = crate::config::load(&data_dir);
    let api_addr = cfg
        .get("api_addr")
        .cloned()
        .unwrap_or_else(|| "https://localhost:8443".to_string());
    if !auth::keypair::has_bootstrap_certs(&data_dir) {
        bail!("agent has no bootstrap TLS material — cannot reach the server");
    }
    let tls = TlsCerts {
        ca_cert: auth::keypair::ca_cert_path(&data_dir).to_string_lossy().to_string(),
        client_cert: auth::keypair::tls_cert_path(&data_dir).to_string_lossy().to_string(),
        client_key: auth::keypair::tls_key_path(&data_dir).to_string_lossy().to_string(),
    };
    let agent = build_ureq_agent(&tls)?;
    Ok(RestClient { agent, api_addr })
}

fn build_ureq_agent(tls: &TlsCerts) -> Result<ureq::Agent> {
    use ureq::tls::{parse_pem, PemItem};
    let ca_pem = std::fs::read(&tls.ca_cert).context("read CA cert")?;
    let cert_pem = std::fs::read(&tls.client_cert).context("read client cert")?;
    let key_pem = std::fs::read(&tls.client_key).context("read client key")?;
    let mut ca_certs = Vec::new();
    for item in parse_pem(&ca_pem) {
        if let Ok(PemItem::Certificate(c)) = item {
            ca_certs.push(c);
        }
    }
    let mut client_certs = Vec::new();
    for item in parse_pem(&cert_pem) {
        if let Ok(PemItem::Certificate(c)) = item {
            client_certs.push(c);
        }
    }
    let mut client_key = None;
    for item in parse_pem(&key_pem) {
        if let Ok(PemItem::PrivateKey(k)) = item {
            client_key = Some(k);
            break;
        }
    }
    let key = client_key.context("no private key found in client key PEM")?;
    let tls_cfg = ureq::tls::TlsConfig::builder()
        .root_certs(ureq::tls::RootCerts::new_with_certs(&ca_certs))
        .client_cert(Some(ureq::tls::ClientCert::new_with_certs(&client_certs, key)))
        .build();
    Ok(ureq::Agent::new_with_config(
        ureq::config::Config::builder()
            .tls_config(tls_cfg)
            .timeout_global(Some(std::time::Duration::from_secs(15)))
            .build(),
    ))
}

// =========================================================================
// wire response shapes — mirror server/api/swarm_poolq.go
// =========================================================================

#[derive(Debug, Deserialize, serde::Serialize)]
struct TopicsResponse {
    #[serde(default)]
    generated_at: Option<String>,
    topics: Vec<TopicSummaryResponse>,
    #[serde(default)]
    participants: Vec<String>,
}

#[derive(Debug, Deserialize, serde::Serialize)]
struct TopicSummaryResponse {
    topic_id_hex: String,
    topic_name: String,
    founder_puddle_pubkey: String,
    head_seq: u64,
    count: i64,
    first_posted_at: i64,
    last_posted_at: i64,
}

#[derive(Debug, Deserialize)]
struct RangeResponse {
    #[allow(dead_code)]
    topic_id_hex: String,
    #[allow(dead_code)]
    from: u64,
    #[allow(dead_code)]
    to: u64,
    messages: Vec<Message>,
}

// MessageBody isn't directly referenced — it round-trips through Message.
// Re-exported below so the wire-shape names line up if a future test
// asserts against them.
#[allow(dead_code)]
type _PinMessageBody = MessageBody;

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    #[test]
    fn resolve_topic_ref_accepts_hex() {
        let dir = TempDir::new().unwrap();
        let hex = "a".repeat(64);
        let (id, name) = resolve_topic_ref(&hex, dir.path()).unwrap();
        assert_eq!(id, hex);
        assert!(name.is_none());
    }

    #[test]
    fn resolve_topic_ref_rejects_bad_name() {
        let dir = TempDir::new().unwrap();
        // No puddle pubkey in the tempdir → falls through to the bare-name
        // path, hits validate_topic_name first for the dot-prefix and bails
        // before touching the puddle.
        assert!(resolve_topic_ref(".hidden", dir.path()).is_err());
    }
}
