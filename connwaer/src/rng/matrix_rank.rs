//! NIST SP 800-22 Test 5: Binary Matrix Rank.
//!
//! Forms 32x32 binary matrices from consecutive bits and computes the rank
//! of each via Gauss elimination over GF(2). The distribution of ranks is
//! compared against the expected distribution using a chi-squared statistic.

pub fn test(bits: &[u8]) -> f64 {
    let m = 32;
    let q = 32;
    let bits_per_matrix = m * q;
    let num_matrices = bits.len() / bits_per_matrix;
    if num_matrices == 0 {
        return 0.0;
    }

    let mut f_m = 0u64; // full rank
    let mut f_m1 = 0u64; // rank m-1

    for i in 0..num_matrices {
        let start = i * bits_per_matrix;
        let matrix_bits = &bits[start..start + bits_per_matrix];
        let rank = binary_matrix_rank(matrix_bits, m, q);
        if rank == m {
            f_m += 1;
        } else if rank == m - 1 {
            f_m1 += 1;
        }
    }

    let n = num_matrices as f64;
    let f_other = n - f_m as f64 - f_m1 as f64;

    // Expected probabilities for 32x32 binary matrices.
    let p_m = 0.2888; // P(rank = M)
    let p_m1 = 0.5776; // P(rank = M-1)
    let p_other = 0.1336; // P(rank < M-1)

    let chi_sq = (f_m as f64 - n * p_m).powi(2) / (n * p_m)
        + (f_m1 as f64 - n * p_m1).powi(2) / (n * p_m1)
        + (f_other - n * p_other).powi(2) / (n * p_other);

    (-chi_sq / 2.0).exp()
}

fn binary_matrix_rank(bits: &[u8], rows: usize, cols: usize) -> usize {
    // Gauss elimination over GF(2).
    let mut matrix: Vec<Vec<u8>> = (0..rows)
        .map(|r| bits[r * cols..(r + 1) * cols].to_vec())
        .collect();

    let mut rank = 0;
    for col in 0..cols {
        // Find pivot.
        let Some(pivot_row) = (rank..rows).find(|&row| matrix[row][col] == 1) else {
            continue;
        };
        matrix.swap(rank, pivot_row);
        // Eliminate (pivot row cloned so the borrow checker allows XOR-ing
        // it into every other row).
        let pivot_vals = matrix[rank].clone();
        for (row, row_vals) in matrix.iter_mut().enumerate() {
            if row != rank && row_vals[col] == 1 {
                for (cell, p) in row_vals.iter_mut().zip(&pivot_vals) {
                    *cell ^= *p;
                }
            }
        }
        rank += 1;
    }
    rank
}
