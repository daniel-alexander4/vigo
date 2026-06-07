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
const SENTINEL_PUBKEY: &str = "0000000000000000000000000000000000000000000000000000000000000000";

fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Emit a file_descriptor_set alongside the generated Rust stubs so
    // `prost_reflect::DescriptorPool::decode` can load the schema at
    // runtime (verify.rs uses this to read the `(vigo.signed)` field
    // option without hand-listing signed fields — see
    // `agent/src/checkin/verify.rs::marshal_bundle_content`).
    let out_dir = std::path::PathBuf::from(std::env::var("OUT_DIR")?);
    let descriptor_path = out_dir.join("vigo_descriptor.bin");

    tonic_build::configure()
        .build_server(false) // agent is client-only
        // Generate BTreeMap (sorted iteration) for every proto map<…>
        // field so encode_to_vec is byte-stable across calls. Matches
        // the server's `proto.MarshalOptions{Deterministic: true}` (see
        // server/grpc/bundlesign.go::marshalBundleContent) so that the
        // bytes the agent re-marshals for ed25519 verify match the
        // bytes the server signed — without this both sides hash maps
        // in independent random orders and verify fails end-to-end.
        .btree_map(["."])
        .file_descriptor_set_path(&descriptor_path)
        .compile_protos(
            &["../protobuf/vigo.proto", "../protobuf/vigo_options.proto"],
            &[
                "../protobuf", // vigo.proto, vigo_options.proto
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
