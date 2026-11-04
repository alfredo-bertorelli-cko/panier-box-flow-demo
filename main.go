// Panier Box Flow demo. Server wiring arrives in step 0.3.
package main

import (
	"log"
	"os"
)

func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Fatal(err)
	}
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	// Port only. Never log keys or the Config fields.
	log.Printf("config loaded, port %s", cfg.Port)
}
