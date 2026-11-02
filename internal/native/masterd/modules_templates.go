//go:build !no_templates

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/templatesmod"
)

func init() { module.Register(templatesmod.New()) }
