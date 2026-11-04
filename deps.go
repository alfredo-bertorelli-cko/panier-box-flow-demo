// Temporary: a blank import keeps the pinned SDK in go.mod before real code
// uses it. Delete this file once cko.go (step 2.1) imports the SDK.
package main

import (
	_ "github.com/checkout/checkout-sdk-go/v3"
)
