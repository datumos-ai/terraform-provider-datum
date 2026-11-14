# A Datum space fully managed from Terraform.
#
#   export DATUM_BASE_URL=https://datum.example.com   # the ORGANISATION host
#   export DATUM_API_KEY=...                            # an org-admin API key (role org-admin)
#   terraform init && terraform apply

terraform {
  required_providers {
    datum = {
      source = "datumos-ai/datum"
    }
  }
}

provider "datum" {}

resource "datum_space" "team" {
  slug         = "tf-team"
  display_name = "Team managed by Terraform"

  # Org-admin decisions about the space itself.
  # namespace_capabilities: what agent runs mount from the Develop grants,
  # each run cut to its person's AD groups (a new space starts ON).
  # namespace_membership: an access-model switch — AD-group coverage admits
  # people as operators; read the coverage report first.
  namespace_capabilities = true
  namespace_membership   = false
  incident_publishers    = []
}

resource "datum_space_manifest" "team" {
  space_id = datum_space.team.id

  # Only the kinds present are managed. Unlisted members/profiles/rows are
  # left alone (reported as orphans) unless `prune = true`.
  spec = jsonencode({
    apiVersion = "datum/v1"

    features = {
      chat         = true
      sandboxTools = true
      knowledge    = null # inherit the organisation default
    }
    monthlyBudgetUsd = 250
    autonomyOptIn    = false

    members = [
      { user = "jdoe", role = "space-admin" },
    ]

    # Delegated settings (space tier). People lists are checked against the
    # space's effective membership — the members above count.
    settings = {
      rosterCoordinatorQueue = jsonencode(["jdoe"])
    }

    # Catalog access: system SLUG → level; null drops a grant, unnamed grants stay.
    systemGrants = {
      billing-api = "develop"
    }
    catalogBaseline = ""
    skillsRepoUrl   = "acme/tf-team-plugins"

    # Credential ROWS only (label + type) — no secret ever travels here. The
    # row is created empty; a person fills it in the panel. Profiles below
    # reference it by label in the same manifest.
    credentials = [
      { label = "sf-robot", type = "web-login" },
    ]

    browserProfiles = [
      {
        name        = "sf-console"
        credentials = ["sf-robot"]
        # The login recipe: steps Datum runs on a fresh cloud browser — no
        # code. A step names a credential by label from `credentials`.
        recipe = [
          { op = "goto", url = "https://login.salesforce.com" },
          { op = "fill", selector = "#username", value = { credential = "sf-robot", field = "username" } },
          { op = "fill", selector = "#password", value = { credential = "sf-robot", field = "password" } },
          { op = "click", selector = "#Login" },
        ]
        enabled = true
      },
    ]
  })
}

output "space_id" {
  value = datum_space.team.id
}
