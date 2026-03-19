---
title: Business Associate Agreement Template
---

# Business Associate Agreement (BAA) Template

This template covers the key provisions required when Hobnob is used to manage systems containing electronic Protected Health Information (ePHI). Customize this for your organization and have legal counsel review before execution.

---

## BUSINESS ASSOCIATE AGREEMENT

This Business Associate Agreement ("BAA") is entered into by and between:

**Covered Entity:** \_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_ ("Covered Entity")

**Business Associate:** \_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_ ("Business Associate")

Effective Date: \_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_

### 1. Definitions

Terms used in this BAA shall have the meanings set forth in the HIPAA Rules (45 CFR Parts 160 and 164), as amended.

- **Protected Health Information (PHI):** Any individually identifiable health information transmitted or maintained in any form.
- **Electronic Protected Health Information (ePHI):** PHI transmitted or maintained in electronic media.
- **HIPAA Rules:** The Privacy Rule, Security Rule, Breach Notification Rule, and Enforcement Rule.
- **Configuration Management System:** The Hobnob software used to enforce desired state on managed systems.

### 2. Obligations of Business Associate

Business Associate agrees to:

**(a) Safeguards.** Implement administrative, physical, and technical safeguards that reasonably and appropriately protect the confidentiality, integrity, and availability of ePHI, including:

- Encrypt all data in transit using mTLS (TLS 1.3 minimum)
- Encrypt secrets at rest using AES-256-GCM
- Enforce authentication on all administrative access
- Implement automatic session timeout (15-minute idle, 24-hour absolute)
- Maintain tamper-evident audit trails with SHA-256 hash chain integrity
- Enforce password complexity requirements (12+ characters, mixed character classes)

**(b) Reporting.** Report to Covered Entity any use or disclosure of PHI not provided for by this BAA, including any Security Incident or Breach, within 60 days of discovery.

**(c) Subcontractors.** Ensure that any subcontractors that create, receive, maintain, or transmit ePHI on behalf of Business Associate agree to the same restrictions and conditions.

**(d) Access.** Make available PHI in a Designated Record Set to the Covered Entity as necessary to satisfy Covered Entity's obligations under 45 CFR 164.524.

**(e) Amendment.** Make available PHI for amendment and incorporate any amendments to PHI in a Designated Record Set as directed by Covered Entity.

**(f) Accounting.** Make available information required to provide an accounting of disclosures as necessary to satisfy Covered Entity's obligations under 45 CFR 164.528.

**(g) Availability.** Make internal practices, books, and records relating to the use and disclosure of PHI available to the Secretary of HHS for purposes of determining Covered Entity's compliance.

**(h) Return/Destruction.** Upon termination, return or destroy all PHI received from Covered Entity, or that was created or received by Business Associate on behalf of Covered Entity.

### 3. Permitted Uses and Disclosures

Business Associate may use or disclose PHI only as necessary to:

- Perform configuration management, compliance enforcement, and security scanning services as described in the service agreement
- Carry out its legal responsibilities
- Provide data aggregation services relating to the health care operations of Covered Entity (if applicable)

Business Associate shall not use or disclose PHI in a manner that would violate the HIPAA Rules if done by Covered Entity.

### 4. Technical Safeguards Implemented by Hobnob

| HIPAA Requirement | Implementation |
|-------------------|----------------|
| 164.312(a)(1) Access control | Role-based access (admin/viewer), authentication required |
| 164.312(a)(2)(i) Unique user identification | Per-user accounts with unique IDs |
| 164.312(a)(2)(ii) Emergency access | Localhost trust with audited emergency access procedure |
| 164.312(a)(2)(iii) Automatic logoff | 15-minute idle session timeout |
| 164.312(a)(2)(iv) Encryption/decryption | AES-256-GCM for secrets, mTLS for transport |
| 164.312(b) Audit controls | Tamper-evident SHA-256 hash chain audit trail |
| 164.312(c)(1) Integrity | ED25519 signature verification on all agent communication |
| 164.312(d) Authentication | Basic (bcrypt), OIDC, or isowebauth (SSH key signing) |
| 164.312(e)(1) Transmission security | mTLS (TLS 1.3) on all gRPC and REST traffic |
| 164.312(e)(2)(ii) Encryption | All gRPC traffic encrypted via mTLS, no plaintext ever |

### 5. Term and Termination

**(a) Term.** This BAA is effective as of the Effective Date and terminates when all PHI is destroyed or returned.

**(b) Termination for Cause.** Either party may terminate this BAA if the other party materially breaches this BAA and fails to cure the breach within 30 days of written notice.

**(c) Effect of Termination.** Upon termination, Business Associate shall return or destroy all PHI. If return or destruction is not feasible, Business Associate shall extend the protections of this BAA to the PHI and limit further uses and disclosures to those purposes that make return or destruction infeasible.

### 6. Miscellaneous

**(a) Regulatory References.** Any reference to a regulatory provision means that provision as amended from time to time.

**(b) Amendment.** This BAA may be amended only by written agreement signed by both parties. The parties agree to negotiate in good faith any amendments necessary to ensure compliance with changes to HIPAA Rules.

**(c) Survival.** The obligations of Business Associate under Section 5(c) shall survive the termination of this BAA.

**(d) Interpretation.** Any ambiguity in this BAA shall be resolved in favor of a meaning that permits Covered Entity to comply with the HIPAA Rules.

---

**COVERED ENTITY**

Signature: \_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_

Name: \_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_

Title: \_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_

Date: \_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_

**BUSINESS ASSOCIATE**

Signature: \_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_

Name: \_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_

Title: \_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_

Date: \_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_

---

*This template is provided as a starting point. Have qualified legal counsel review and customize it for your specific use case before execution.*
