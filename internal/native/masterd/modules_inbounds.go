//go:build !no_inbounds

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/inboundsmod"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

func init() { module.Register(inboundsmod.New()) }
