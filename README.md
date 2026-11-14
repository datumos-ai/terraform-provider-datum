# terraform-provider-datum

The official Terraform provider for [Datum](https://datumos.ai). Thin by
design: two resources, one transport. Every rule about what a space may
contain lives server-side in the space manifest engine, so a new Datum feature reaches Terraform users
with a Datum release — this binary does not change.

| Resource | What it manages |
| --- | --- |
| `datum_space` | The space's existence and identity: `slug` (renamed in place — the old slug stays a permanent alias of the space), `display_name`, `status` (`active` / `suspended`). Destroy = suspend — Datum never deletes a space. |
| `datum_space_manifest` | The space's configuration as one JSON document (`spec`, use `jsonencode`), applied by the server; `prune` deletes unlisted list entries (default off). Plan = server-side dry run; drift = the server's projected observation. With `space_id` = the org-admin mode on the organisation host; without it = the space-admin mode on the space's host with a space-bound key (space-tier kinds only). |

Authentication: an **org-admin** API key on the **organisation** host
(`DATUM_BASE_URL`, `DATUM_API_KEY`, or the `host` / `api_key` provider
attributes). A key of role `admin` is refused by every space mutation —
mint the key with role `org-admin` (Settings → API keys on the org host).

## Build and run locally

```bash
go build -o bin/terraform-provider-datum .
```

Point Terraform at the binary with a `dev_overrides` block (no `terraform
init` needed for the provider):

```hcl
# ~/.terraformrc
provider_installation {
  dev_overrides {
    "datumos-ai/datum" = "/absolute/path/to/terraform-provider/bin"
  }
  direct {}
}
```

Then, from a directory with a configuration (see [`examples/space`](examples/space/main.tf)):

```bash
export DATUM_BASE_URL=https://datum.example.com
export DATUM_API_KEY=...   # never commit it
terraform plan
terraform apply
```

The `infrastructure-as-code` feature must be ON for the organisation
(Settings → General → Features) — otherwise `/api/manifest` answers 403.

## Layout

- `main.go` — provider server (`registry.terraform.io/datumos-ai/datum`).
- `internal/provider/provider.go` — provider schema and configuration.
- `internal/provider/client.go` — the HTTP client (spaces + manifest calls).
- `internal/provider/resource_space.go`, `resource_space_manifest.go` — the resources.
- `examples/space/` — a complete configuration.

## Releases

A `v*` tag builds the provider for every platform with GoReleaser, signs
the checksums with the release GPG key and attaches everything to a GitHub
release (`.github/workflows/release.yml`) — the layout the Terraform
Registry reads. The workflow needs two repository secrets,
`GPG_PRIVATE_KEY` and `GPG_PASSPHRASE`; the key's public half is registered
with the Registry namespace.

## License

Apache-2.0 — see `LICENSE` and `NOTICE`. `Datum` and `DatumOS` are
trademarks; the license does not grant rights to them.
