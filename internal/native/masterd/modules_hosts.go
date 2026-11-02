//go:build !no_hosts

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/hostsmod"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

func init() { module.Register(hostsmod.New()) }
