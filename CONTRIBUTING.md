# Contributing

The Vigo server, CLI and web UI are licensed under the
[GNU Affero General Public License v3.0](LICENSE). The agent (`agent/`),
Connwaer (`connwaer/`), the protocol definitions (`protobuf/`) and the
documentation (`docs/`) are licensed under the
[Apache License 2.0](agent/LICENSE); each of those directories carries its own
`LICENSE` file. Contributions are accepted under the license of the part they
change — inbound equals outbound — with no contributor license agreement.

## Sign off your commits

Every commit must carry a `Signed-off-by:` line, certifying the
[Developer Certificate of Origin 1.1](https://developercertificate.org/):
that you wrote the change, or otherwise have the right to submit it under
the project's license.

```bash
git commit -s
```

The sign-off must use your real name and an email you can be reached at.

## Before you send a change

- `make lint` and `make test` pass.
- Docs, examples and `docs/reference/` are updated in the same change as the
  code they describe.
- New source files start with the project header:

  ```go
  // Copyright (c) 2026 Alexander4, LLC
  // SPDX-License-Identifier: Apache-2.0
  ```

## Names

Contributing code does not grant use of the Vigo name or logo — see
[TRADEMARKS.md](TRADEMARKS.md).
