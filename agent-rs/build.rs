fn main() -> Result<(), Box<dyn std::error::Error>> {
    tonic_build::configure()
        .build_server(false) // agent is client-only
        .compile_protos(
            &["../proto/hobnob.proto"],
            &[
                "../proto",       // hobnob.proto
                "proto",          // google/api stubs
            ],
        )?;
    Ok(())
}
