//go:build !no_settings

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/settingsmod"
)

func init() { module.Register(settingsmod.New()) }
