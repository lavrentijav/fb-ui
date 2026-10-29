//go:build !no_routing

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/routingmod"
)

func init() { module.Register(routingmod.New()) }
