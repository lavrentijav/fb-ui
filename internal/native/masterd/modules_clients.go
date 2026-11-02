//go:build !no_clients

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/clientsmod"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

func init() { module.Register(clientsmod.New()) }
