//go:build !no_access

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/accessmod"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

func init() { module.Register(accessmod.New()) }
