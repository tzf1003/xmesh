**Pre-release for hands-on testing. Not for production use.**

This build exists so the real-environment acceptance steps can be run on Windows, Linux and macOS
machines. The steps are in
[docs/validation/real-environment-runbook.md](https://github.com/tzf1003/xmesh/blob/__TAG__/docs/validation/real-environment-runbook.md);
each one is independent and can be skipped. Every archive also contains the runbook and the
`scripts/` it uses, so nothing else needs to be cloned.

## Download and verify

Pick the archive for your system (`linux`, `darwin` or `windows`; `amd64` or `arm64`), then check it
against `SHA256SUMS.txt` from this release:

```
sha256sum --ignore-missing -c SHA256SUMS.txt        # Linux
shasum -a 256 --ignore-missing -c SHA256SUMS.txt    # macOS
```

Windows (PowerShell): `(Get-FileHash .\xmesh-__TAG__-windows-amd64.zip).Hash` must equal the matching
line in `SHA256SUMS.txt`. Then run `xmesh version`; it must print `xmesh __TAG__`.

## Known limits of this build

- The binaries are not code-signed or notarized. macOS: run `xattr -d com.apple.quarantine xmesh` after
  extracting. Windows: SmartScreen may ask for confirmation.
- Windows L2 isolation is not available in production yet: a Windows node configured with
  `agent.policy.isolation: L2` refuses exec and PTY with `sandbox.unavailable`. This is expected, and
  step F of the runbook checks for exactly that refusal.
- There is no desktop UI in this build; everything is driven by the `xmesh` command line.
- OAuth tokens are kept in memory only, so an ingress restart requires authorizing again.

The release commit passed the Go, contract fixture and Headscale interop workflows, including the
Windows native jobs, before these binaries were built.
