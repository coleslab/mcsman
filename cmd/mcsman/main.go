// Command mcsman is the Minecraft server management daemon.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	configPath := flag.String("config", "./mcsman.yaml", "path to config file")
	flag.Parse()
	fmt.Fprintf(os.Stderr, "mcsman: not yet implemented (config=%s)\n", *configPath)
	os.Exit(1)
}
