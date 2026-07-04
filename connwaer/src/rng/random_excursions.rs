//! NIST SP 800-22 Test 14: Random Excursions.
//!
//! Converts the sequence to a random walk and counts visits to states
//! {-4, -3, -2, -1, 1, 2, 3, 4} within each cycle (return to zero).
//! The distribution of visit counts is compared against the expected
//! distribution using a chi-squared test. Requires at least 500 cycles.

pub fn test(bits: &[u8]) -> Vec<(i32, f64)> {
    let n = bits.len();
    let x: Vec<i64> = bits.iter().map(|&b| 2 * b as i64 - 1).collect();

    // Build cumulative sum (random walk).
    let mut s = vec![0i64; n + 1];
    for i in 0..n {
        s[i + 1] = s[i] + x[i];
    }

    // Find cycles (returns to zero).
    let mut cycles: Vec<Vec<i64>> = Vec::new();
    let mut cycle_start = 0;
    for i in 1..=n {
        if s[i] == 0 {
            cycles.push(s[cycle_start..=i].to_vec());
            cycle_start = i;
        }
    }

    let j = cycles.len();
    if j < 500 {
        return vec![]; // not enough cycles
    }

    let states: [i32; 8] = [-4, -3, -2, -1, 1, 2, 3, 4];
    let mut results = Vec::new();

    for &state in &states {
        let mut freq = [0u64; 6]; // visits: 0, 1, 2, 3, 4, 5+
        for cycle in &cycles {
            let visits = cycle.iter().filter(|&&v| v == state as i64).count().min(5);
            freq[visits] += 1;
        }

        let pi = excursion_pi(state);
        let mut chi_sq = 0.0;
        for k in 0..6 {
            let expected = j as f64 * pi[k];
            if expected > 0.0 {
                chi_sq += (freq[k] as f64 - expected).powi(2) / expected;
            }
        }
        let p = super::igamc(2.5, chi_sq / 2.0);
        results.push((state, p));
    }

    results
}

fn excursion_pi(state: i32) -> [f64; 6] {
    let x = state.unsigned_abs() as f64;
    let mut pi = [0.0f64; 6];
    pi[0] = 1.0 - 1.0 / (2.0 * x);
    pi[1] = 1.0 / (4.0 * x * x);
    pi[2] = 1.0 / (4.0 * x * x) * (1.0 - 1.0 / (2.0 * x));
    pi[3] = 1.0 / (4.0 * x * x) * (1.0 - 1.0 / (2.0 * x)).powi(2);
    pi[4] = 1.0 / (4.0 * x * x) * (1.0 - 1.0 / (2.0 * x)).powi(3);
    pi[5] = 1.0 / (4.0 * x * x) * (1.0 - 1.0 / (2.0 * x)).powi(4);
    pi
}
