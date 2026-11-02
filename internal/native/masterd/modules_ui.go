//go:build !no_ui

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/uimod"
)

func init() { module.Register(uimod.New()) }
