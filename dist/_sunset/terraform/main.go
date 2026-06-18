// main.go — entry point for the LiteMLflow Terraform provider.
// Serves the provider using Protocol 6 (terraform-plugin-framework).
package main

import (
	"context"
	"flag"
	"log"

	"github.com/gorevds/litemlflow/terraform/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

// version is overridden at link time via -ldflags when releasing.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "Enable debug mode (attach a debugger)")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/gorevds/litemlflow",
		Debug:   debug,
	}

	if err := providerserver.Serve(context.Background(), provider.New(version), opts); err != nil {
		log.Fatal(err)
	}
}
