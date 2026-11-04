// Temporary: blank imports keep the pinned SDK and godotenv in go.mod
// before real code uses them. Delete this file once config.go (step 0.2)
// and cko.go (step 2.1) import both modules.
package main

import (
	_ "github.com/checkout/checkout-sdk-go/v3"
	_ "github.com/joho/godotenv"
)
