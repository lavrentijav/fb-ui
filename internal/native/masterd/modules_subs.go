//go:build !no_subs

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subsmod"
)

func init() { module.Register(subsmod.New()) }
