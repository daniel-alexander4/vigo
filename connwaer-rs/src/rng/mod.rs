/// NIST SP 800-22 Statistical Test Suite for Random Number Generators.
///
/// Implements the 15 standard tests. Each test accepts a bitstream and returns
/// a p-value. A p-value >= alpha (typically 0.01) means the sequence passes
/// that test (i.e., is consistent with randomness).
///
/// Reference: https://csrc.nist.gov/publications/detail/sp/800-22/rev-1a/final

pub mod frequency;
pub mod block_frequency;
pub mod runs;
pub mod longest_run;
pub mod matrix_rank;
pub mod dft;
pub mod non_overlapping;
pub mod overlapping;
pub mod universal;
pub mod linear_complexity;
pub mod serial;
pub mod approximate_entropy;
pub mod cumulative_sums;
pub mod random_excursions;
pub mod random_excursions_variant;

use anyhow::Result;
use serde::{Deserialize, Serialize};

/// Result of a single NIST test.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TestResult {
    pub name: String,
    pub p_value: f64,
    pub passed: bool,
}

/// Run all 15 NIST SP 800-22 tests on a bitstream.
/// Returns per-test results and an overall pass/fail.
pub fn run_suite(bits: &[u8], alpha: f64) -> Result<Vec<TestResult>> {
    let n = bits.len();
    if n < 100 {
        anyhow::bail!("bitstream too short: {n} bits, need at least 100");
    }

    let mut results = Vec::new();

    // Test 1: Frequency (Monobit).
    let p = frequency::test(bits);
    results.push(TestResult { name: "frequency".to_string(), p_value: p, passed: p >= alpha });

    // Test 2: Frequency within a Block.
    let block_size = 128.min(n / 10).max(20);
    let p = block_frequency::test(bits, block_size);
    results.push(TestResult { name: "block_frequency".to_string(), p_value: p, passed: p >= alpha });

    // Test 3: Runs.
    let p = runs::test(bits);
    results.push(TestResult { name: "runs".to_string(), p_value: p, passed: p >= alpha });

    // Test 4: Longest Run of Ones in a Block.
    if n >= 128 {
        let p = longest_run::test(bits);
        results.push(TestResult { name: "longest_run".to_string(), p_value: p, passed: p >= alpha });
    }

    // Test 5: Binary Matrix Rank.
    if n >= 38912 {
        let p = matrix_rank::test(bits);
        results.push(TestResult { name: "matrix_rank".to_string(), p_value: p, passed: p >= alpha });
    }

    // Test 6: Discrete Fourier Transform (Spectral).
    if n >= 1000 {
        let p = dft::test(bits);
        results.push(TestResult { name: "dft".to_string(), p_value: p, passed: p >= alpha });
    }

    // Test 7: Non-overlapping Template Matching.
    if n >= 1000 {
        let p = non_overlapping::test(bits);
        results.push(TestResult { name: "non_overlapping_template".to_string(), p_value: p, passed: p >= alpha });
    }

    // Test 8: Overlapping Template Matching.
    if n >= 1000000 {
        let p = overlapping::test(bits);
        results.push(TestResult { name: "overlapping_template".to_string(), p_value: p, passed: p >= alpha });
    }

    // Test 9: Maurer's Universal Statistical.
    if n >= 387840 {
        let p = universal::test(bits);
        results.push(TestResult { name: "universal".to_string(), p_value: p, passed: p >= alpha });
    }

    // Test 10: Linear Complexity.
    if n >= 1000000 {
        let p = linear_complexity::test(bits);
        results.push(TestResult { name: "linear_complexity".to_string(), p_value: p, passed: p >= alpha });
    }

    // Test 11: Serial.
    if n >= 1000 {
        let (p1, p2) = serial::test(bits);
        results.push(TestResult { name: "serial_1".to_string(), p_value: p1, passed: p1 >= alpha });
        results.push(TestResult { name: "serial_2".to_string(), p_value: p2, passed: p2 >= alpha });
    }

    // Test 12: Approximate Entropy.
    if n >= 1000 {
        let p = approximate_entropy::test(bits);
        results.push(TestResult { name: "approximate_entropy".to_string(), p_value: p, passed: p >= alpha });
    }

    // Test 13: Cumulative Sums.
    let (p_fwd, p_rev) = cumulative_sums::test(bits);
    results.push(TestResult { name: "cumulative_sums_fwd".to_string(), p_value: p_fwd, passed: p_fwd >= alpha });
    results.push(TestResult { name: "cumulative_sums_rev".to_string(), p_value: p_rev, passed: p_rev >= alpha });

    // Tests 14/15: Random Excursions and Variant require long sequences.
    if n >= 1000000 {
        let re_results = random_excursions::test(bits);
        for (state, p) in &re_results {
            results.push(TestResult {
                name: format!("random_excursions_{state}"),
                p_value: *p,
                passed: *p >= alpha,
            });
        }

        let rev_results = random_excursions_variant::test(bits);
        for (state, p) in &rev_results {
            results.push(TestResult {
                name: format!("random_excursions_variant_{state}"),
                p_value: *p,
                passed: *p >= alpha,
            });
        }
    }

    Ok(results)
}

/// Convert a byte slice to a bit array (MSB first).
pub fn bytes_to_bits(data: &[u8]) -> Vec<u8> {
    let mut bits = Vec::with_capacity(data.len() * 8);
    for byte in data {
        for i in (0..8).rev() {
            bits.push((byte >> i) & 1);
        }
    }
    bits
}

/// Complementary error function (erfc) — used by multiple tests.
/// Approximation from Abramowitz and Stegun 7.1.26.
pub fn erfc(x: f64) -> f64 {
    let t = 1.0 / (1.0 + 0.5 * x.abs());
    let tau = t * (-x * x - 1.26551223
        + t * (1.00002368
        + t * (0.37409196
        + t * (0.09678418
        + t * (-0.18628806
        + t * (0.27886807
        + t * (-1.13520398
        + t * (1.48851587
        + t * (-0.82215223
        + t * 0.17087277)))))))))
    .exp();
    if x >= 0.0 { tau } else { 2.0 - tau }
}

/// Normal CDF Phi(x).
pub fn normal_cdf(x: f64) -> f64 {
    0.5 * erfc(-x / std::f64::consts::SQRT_2)
}

/// Incomplete gamma function ratio: igamc(a, x) = Q(a, x) = 1 - P(a, x).
/// Used for chi-squared p-value computation.
pub fn igamc(a: f64, x: f64) -> f64 {
    if x <= 0.0 || a <= 0.0 { return 1.0; }
    if x < 1.0 + a { return 1.0 - igam_series(a, x); }
    igamc_cf(a, x)
}

fn igam_series(a: f64, x: f64) -> f64 {
    let mut sum = 1.0 / a;
    let mut term = sum;
    for n in 1..200 {
        term *= x / (a + n as f64);
        sum += term;
        if term.abs() < sum.abs() * 1e-14 { break; }
    }
    sum * (-x + a * x.ln() - lgamma(a)).exp()
}

fn igamc_cf(a: f64, x: f64) -> f64 {
    let mut f = 1e-30_f64;
    let mut c = f;
    let mut d = 0.0_f64;
    for i in 1..200 {
        let an = if i % 2 == 1 {
            ((i as f64 + 1.0) / 2.0) as f64 - a
        } else {
            (i as f64) / 2.0
        };
        let an = if i == 1 { 1.0 } else { an * (i as f64 / 2.0) };
        let bn = x + (i as f64) - a;
        d = bn + an * d;
        if d.abs() < 1e-30 { d = 1e-30; }
        c = bn + an / c;
        if c.abs() < 1e-30 { c = 1e-30; }
        d = 1.0 / d;
        let delta = c * d;
        f *= delta;
        if (delta - 1.0).abs() < 1e-14 { break; }
    }
    f * (-x + a * x.ln() - lgamma(a)).exp()
}

fn lgamma(x: f64) -> f64 {
    // Stirling approximation for ln(Gamma(x)).
    if x <= 0.0 { return 0.0; }
    let coeffs = [
        76.18009172947146, -86.50532032941677, 24.01409824083091,
        -1.231739572450155, 0.1208650973866179e-2, -0.5395239384953e-5,
    ];
    let mut y = x;
    let tmp = x + 5.5 - (x + 0.5) * (x + 5.5).ln();
    let mut ser = 1.000000000190015_f64;
    for &c in &coeffs {
        y += 1.0;
        ser += c / y;
    }
    -tmp + (2.5066282746310005 * ser / x).ln()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn bytes_to_bits_works() {
        let bits = bytes_to_bits(&[0b10110001]);
        assert_eq!(bits, vec![1, 0, 1, 1, 0, 0, 0, 1]);
    }

    #[test]
    fn erfc_values() {
        assert!((erfc(0.0) - 1.0).abs() < 1e-6);
        assert!(erfc(3.0) < 0.01);
        assert!((erfc(-3.0) - 2.0 + erfc(3.0)).abs() < 1e-6);
    }

    #[test]
    fn suite_on_random_data() {
        // Generate pseudo-random bits using a simple LCG.
        let mut state: u64 = 12345;
        let mut data = Vec::with_capacity(125000); // 1M bits
        for _ in 0..125000 {
            state = state.wrapping_mul(6364136223846793005).wrapping_add(1);
            data.push((state >> 33) as u8);
        }
        let bits = bytes_to_bits(&data);
        let results = run_suite(&bits, 0.01).unwrap();
        // A decent PRNG should pass most tests.
        let passed = results.iter().filter(|r| r.passed).count();
        assert!(passed > results.len() / 2, "expected most tests to pass, got {}/{}", passed, results.len());
    }

    #[test]
    fn suite_rejects_all_zeros() {
        let bits = vec![0u8; 10000];
        let results = run_suite(&bits, 0.01).unwrap();
        // All-zero sequence should fail frequency test at minimum.
        let freq = results.iter().find(|r| r.name == "frequency").unwrap();
        assert!(!freq.passed, "all-zeros should fail frequency test");
    }

    #[test]
    fn suite_rejects_all_ones() {
        let bits = vec![1u8; 10000];
        let results = run_suite(&bits, 0.01).unwrap();
        let freq = results.iter().find(|r| r.name == "frequency").unwrap();
        assert!(!freq.passed, "all-ones should fail frequency test");
    }

    #[test]
    fn too_short_rejected() {
        assert!(run_suite(&[0u8; 50], 0.01).is_err());
    }
}
