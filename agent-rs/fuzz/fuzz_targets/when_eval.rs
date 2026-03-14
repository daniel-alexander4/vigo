#![no_main]
use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    if let Ok(s) = std::str::from_utf8(data) {
        if let Ok(expr) = hobnob::policy::when::parse(s) {
            let traits = serde_json::json!({
                "os": {"family": "debian", "distro": "ubuntu"},
                "ports": {"listening": [22, 80, 443]}
            });
            let var_fn = |_: &str| false;
            let func_fn = |name: &str, args: &[String]| {
                hobnob::policy::when::evaluate_builtin(name, args, &traits)
            };
            let _ = hobnob::policy::when::eval_with_funcs(&expr, &var_fn, Some(&func_fn));
        }
    }
});
