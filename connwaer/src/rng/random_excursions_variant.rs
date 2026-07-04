//! NIST SP 800-22 Test 15: Random Excursions Variant.
//!
//! Counts the total number of times the random walk visits each state
//! in {-9, ..., -1, 1, ..., 9}. The deviation from the expected count
//! (equal to the number of cycles) is tested using the erfc function.
//! Requires at least 500 cycles (zero crossings).

pub fn test(bits: &[u8]) -> Vec<(i32, f64)> {
    let n = bits.len();

    // Build cumulative sum.
    let mut s = vec![0i64; n + 1];
    for i in 0..n {
        s[i + 1] = s[i] + 2 * bits[i] as i64 - 1;
    }

    // Count zero crossings (cycles).
    let j = s[1..].iter().filter(|&&v| v == 0).count();
    if j < 500 {
        return vec![];
    }

    let mut results = Vec::new();
    for state in -9i32..=9 {
        if state == 0 {
            continue;
        }
        let xi = s.iter().filter(|&&v| v == state as i64).count() as f64;
        let z = (xi - j as f64)
            / (2.0 * j as f64 * (4.0 * state.unsigned_abs() as f64 - 2.0)).sqrt();
        let p = super::erfc(z.abs() / std::f64::consts::SQRT_2);
        results.push((state, p));
    }

    results
}
