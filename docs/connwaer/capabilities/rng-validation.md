
# RNG Statistical Validation

| | |
|---|---|
| **Machine name** | `rng_validation` |
| **Controls** | NGC-TS-4.2, MGA-RGR-6.1 |
| **Platforms** | All |
| **Timeout** | 10m |

## What it verifies

Reads a sample from a random number generator and runs the full NIST SP 800-22 statistical test suite against it. This proves that the RNG source produces output that is statistically indistinguishable from true randomness -- a requirement for iGaming regulators (Nevada Gaming Commission, Malta Gaming Authority) and any system relying on cryptographic-quality entropy. Results are recorded to the trends database for drift detection across runs.

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `source` | string | `"/dev/urandom"` | Path to the RNG source. Can be a device file (e.g., `/dev/urandom`, `/dev/hwrng`) or a regular file containing random data. |
| `sample_size_bytes` | integer | `125000` | Number of bytes to read from the source. The default of 125,000 bytes yields 1,000,000 bits -- the minimum recommended by NIST for the full test suite. |

## Thresholds

| Threshold | Type | Default | Description |
|-----------|------|---------|-------------|
| `alpha` | float | `0.01` | Significance level for each statistical test. A test passes when its p-value is >= `alpha`. The NIST-recommended default of 0.01 means a 1% chance of a false failure per test. |

## Verification logic

1. **Read sample** -- Open the configured `source` and read `sample_size_bytes` bytes. For device files (which may not support regular `read`), falls back to `read_exact` to pull exactly the requested number of bytes. The raw bytes are converted to a bitstream (MSB first).

2. **Run NIST SP 800-22 test suite** -- Execute up to 15 standard tests on the bitstream. Each test produces a p-value. A test passes if its p-value >= `alpha`. The tests are:

   | # | Test | Min bits |
   |---|------|----------|
   | 1 | Frequency (Monobit) | 100 |
   | 2 | Frequency within a Block | 100 |
   | 3 | Runs | 100 |
   | 4 | Longest Run of Ones in a Block | 128 |
   | 5 | Binary Matrix Rank | 38,912 |
   | 6 | Discrete Fourier Transform (Spectral, FFT) | 1,000 |
   | 7 | Non-overlapping Template Matching | 1,000 |
   | 8 | Overlapping Template Matching | 1,000,000 |
   | 9 | Maurer's Universal Statistical | 387,840 |
   | 10 | Linear Complexity | 1,000,000 |
   | 11 | Serial (two sub-tests) | 1,000 |
   | 12 | Approximate Entropy | 1,000 |
   | 13 | Cumulative Sums (forward and reverse) | 100 |
   | 14 | Random Excursions | 1,000,000 |
   | 15 | Random Excursions Variant | 1,000,000 |

   Tests whose minimum bit requirement exceeds the sample size are skipped automatically. With the default 1,000,000-bit sample, all 15 tests run.

3. **Evaluate results** -- Count passed and failed tests. Per-test p-values and pass/fail status are recorded as evidence. Failed test names are listed explicitly.

4. **Record to trends** -- The passed count, total count, and duration are written to the trends database for cross-run drift analysis.

### Result

- **Pass** -- All executed tests passed (p-value >= `alpha`).

- **Warn** -- More than 80% of tests passed but at least one failed.
- **Fail** -- 20% or more of tests failed.

## Example config

```yaml
capabilities:
  rng_validation:
    schedule: "daily"
    params:
      source: "/dev/urandom"
      sample_size_bytes: 125000
    thresholds:
      alpha: 0.01
```

Test a hardware RNG device with a larger sample:

```yaml
capabilities:
  rng_validation:
    schedule: "daily"
    params:
      source: "/dev/hwrng"
      sample_size_bytes: 250000
    thresholds:
      alpha: 0.01
```

Test a pre-generated random data file:

```yaml
capabilities:
  rng_validation:
    schedule: "weekly"
    params:
      source: "/var/lib/rng-samples/latest.bin"
      sample_size_bytes: 125000
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
