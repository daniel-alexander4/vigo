fn main() -> Result<(), Box<dyn std::error::Error>> {
    tonic_build::configure()
        .build_server(false) // agent is client-only
        .compile_protos(
            &["../protobuf/vigo.proto"],
            &[
                "../protobuf",    // vigo.proto
                "proto",          // google/api stubs
            ],
        )?;
    Ok(())
}
