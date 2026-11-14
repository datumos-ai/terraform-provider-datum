// terraform-provider-datum — the official Terraform provider for Datum.
//
// The provider is deliberately THIN: it knows two resources
// (`datum_space`, `datum_space_manifest`) and one transport. Every rule about
// what a space may contain lives server-side in the space manifest engine, so a new Datum feature reaches Terraform users
// through a server release — this binary does not change.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/datumos-ai/terraform-provider-datum/internal/provider"
)

// version is stamped by the release build (-ldflags "-X main.version=…").
var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "run the provider with debugger support")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/datumos-ai/datum",
		Debug:   *debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
