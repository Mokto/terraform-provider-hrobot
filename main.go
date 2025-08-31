package main

import (
	"context"
	"flag"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/mokto/terraform-provider-hrobot/provider"
)

var (
	version = "dev"
)

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "serve provider with debug support")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.opentofu.org/mokto/hrobot",
		Debug:   debug,
	}

	providerserver.Serve(context.Background(), provider.New(version), opts)
}
