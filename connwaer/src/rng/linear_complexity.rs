//! NIST SP 800-22 Test 10: Linear Complexity.
//!
//! Divides the sequence into blocks and computes the linear complexity of
//! each using the Berlekamp-Massey algorithm. The distribution of
//! complexity values is compared against the expected distribution.

pub fn test(bits: &[u8]) -> f64 {
    let n = bits.len();
    let m = 500;
    let num_blocks = n / m;
    if num_blocks == 0 {
        return 1.0;
    }

    // mu = M/2 + (9 + (-1)^M)/36 - (M/3 + 2/9)/2^M
    // For M=500, the 2^M term is negligible (effectively 0).
    let mu = m as f64 / 2.0
        + (9.0 + if m % 2 == 0 { -1.0 } else { 1.0 }) / 36.0;

    let mut freq = [0u64; 7]; // K+1 = 7 classes

    for i in 0..num_blocks {
        let block = &bits[i * m..(i + 1) * m];
        let lc = berlekamp_massey(block);
        let t = lc as f64 - mu;
        let class = if t <= -2.5 {
            0
        } else if t <= -1.5 {
            1
        } else if t <= -0.5 {
            2
        } else if t <= 0.5 {
            3
        } else if t <= 1.5 {
            4
        } else if t <= 2.5 {
            5
        } else {
            6
        };
        freq[class] += 1;
    }

    let pi = [0.010417, 0.03125, 0.125, 0.5, 0.25, 0.0625, 0.020833];
    let mut chi_sq = 0.0;
    for k in 0..7 {
        let expected = num_blocks as f64 * pi[k];
        if expected > 0.0 {
            chi_sq += (freq[k] as f64 - expected).powi(2) / expected;
        }
    }

    super::igamc(3.0, chi_sq / 2.0)
}

fn berlekamp_massey(bits: &[u8]) -> usize {
    let n = bits.len();
    let mut c = vec![0u8; n + 1];
    let mut b = vec![0u8; n + 1];
    c[0] = 1;
    b[0] = 1;
    let mut l = 0usize;
    let mut m = -1i64;

    for i in 0..n {
        let mut d = bits[i];
        for j in 1..=l {
            d ^= c[j] & bits[i - j];
        }
        if d == 1 {
            let t = c.clone();
            let shift = (i as i64 - m) as usize;
            for j in 0..n + 1 - shift {
                c[j + shift] ^= b[j];
            }
            if 2 * l <= i {
                l = i + 1 - l;
                m = i as i64;
                b = t;
            }
        }
    }
    l
}
