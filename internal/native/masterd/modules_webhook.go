//go:build !no_webhook

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/webhookmod"
)

func init() { module.Register(webhookmod.New()) }
