//! NIST SP 800-22 Test 4: Longest Run of Ones in a Block.
//!
//! Determines whether the longest run of ones within M-bit blocks is
//! consistent with what would be expected in a random sequence.
//! Block size M and expected distribution depend on sequence length.

pub fn test(bits: &[u8]) -> f64 {
    let n = bits.len();
    let (m, k, pi, v_values): (usize, usize, &[f64], &[usize]) = if n < 6272 {
        (
            8,
            3,
            &[0.2148, 0.3672, 0.2305, 0.1875],
            &[1, 2, 3, 4],
        )
    } else if n < 750000 {
        (
            128,
            5,
            &[0.1174, 0.2430, 0.2493, 0.1752, 0.1027, 0.1124],
            &[4, 5, 6, 7, 8, 9],
        )
    } else {
        (
            10000,
            6,
            &[0.0882, 0.2092, 0.2483, 0.1933, 0.1208, 0.0675, 0.0727],
            &[10, 11, 12, 13, 14, 15, 16],
        )
    };
    let num_blocks = n / m;
    let mut freq = vec![0u64; k + 1];
    for i in 0..num_blocks {
        let block = &bits[i * m..(i + 1) * m];
        let longest = longest_run_of_ones(block);
        let class = v_values
            .iter()
            .position(|&v| longest <= v)
            .unwrap_or(k);
        freq[class] += 1;
    }
    let mut chi_sq = 0.0;
    for j in 0..=k {
        let expected = num_blocks as f64 * pi[j];
        if expected > 0.0 {
            chi_sq += (freq[j] as f64 - expected).powi(2) / expected;
        }
    }
    super::igamc(k as f64 / 2.0, chi_sq / 2.0)
}

fn longest_run_of_ones(block: &[u8]) -> usize {
    let mut max_run = 0;
    let mut current = 0;
    for &b in block {
        if b == 1 {
            current += 1;
            max_run = max_run.max(current);
        } else {
            current = 0;
        }
    }
    max_run
}
