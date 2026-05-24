// Agent build script.
//
// Compiles the gRPC stubs from ../protobuf/vigo.proto AND injects the
// agent's baked-in ED25519 update-verification pubkey via
// VIGO_AGENT_SIGNING_PUBKEY.
//
// The pubkey is sourced from the same SIGN_KEY that signs distributed
// binaries (see Makefile), so the build pipeline can't produce an
// agent whose baked-in pubkey disagrees with the .sig files it'll
// receive — they derive from one key.
//
// When VIGO_AGENT_SIGNING_PUBKEY is unset or malformed (cargo build /
// cargo test without a SIGN_KEY in scope), the sentinel — 32 zero
// bytes hex-encoded — is baked in instead. The build succeeds (so
// cargo workflows aren't broken), but the runtime check in
// update::mod refuses to apply any update against the sentinel and
// emits an operator-actionable error pointing at `make build-agent
// SIGN_KEY=<path>`. Attackers can't trigger the sentinel path at
// runtime because the pubkey is a compile-time const.
const SENTINEL_PUBKEY: &str =
    "0000000000000000000000000000000000000000000000000000000000000000";

fn main() -> Result<(), Box<dyn std::error::Error>> {
    tonic_build::configure()
        .build_server(false) // agent is client-only
        .compile_protos(
            &["../protobuf/vigo.proto"],
            &[
                "../protobuf", // vigo.proto
                "proto",       // google/api stubs
            ],
        )?;

    println!("cargo:rerun-if-env-changed=VIGO_AGENT_SIGNING_PUBKEY");
    let pubkey = match std::env::var("VIGO_AGENT_SIGNING_PUBKEY") {
        Ok(s) if s.len() == 64 && s.chars().all(|c| c.is_ascii_hexdigit()) => s,
        Ok(s) if s.is_empty() => SENTINEL_PUBKEY.to_string(),
        Ok(s) => {
            println!(
                "cargo:warning=VIGO_AGENT_SIGNING_PUBKEY is not a 64-char hex string ({} chars) — falling back to sentinel pubkey; agent will refuse updates at runtime",
                s.len()
            );
            SENTINEL_PUBKEY.to_string()
        }
        Err(_) => SENTINEL_PUBKEY.to_string(),
    };
    println!("cargo:rustc-env=VIGO_AGENT_SIGNING_PUBKEY={}", pubkey);

    Ok(())
}
