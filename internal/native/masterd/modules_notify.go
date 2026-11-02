//go:build !no_notify

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/notifymod"
)

func init() { module.Register(notifymod.New()) }
