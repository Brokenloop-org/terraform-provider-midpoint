package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/jplana/terraform-provider-midpoint/internal/provider"
)

// version is set by the release build via -ldflags.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/jplana/midpoint",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
