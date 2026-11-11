// Command erp-payment runs the payment component alone (serve, migrate); the shells compile in payment.Definition().
package main

import (
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/erp-payment/v3/payment"
)

func main() { launcher.Main(payment.Definition()) }
