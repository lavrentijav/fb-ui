//go:build !no_usage

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usagemod"
)

func init() { module.Register(usagemod.New()) }
